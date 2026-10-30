// Package capture records proxied traffic, as the proxies read and write
// it, into pcapng files: HTTPS decrypted, on the client side, the upstream
// side or both. One capture runs at a time, with a time and size limit;
// files are deleted after the retention time.
package capture

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"imuslab.com/zoraxy/mod/utils"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sides.
const (
	SideClient   = "client"   // client to Waymark
	SideUpstream = "upstream" // Waymark to the upstream server
	SideBoth     = "both"
)

// Scopes.
const (
	ScopeAll  = "all"
	ScopeHTTP = "http"
	ScopeL4   = "l4"
)

// Limits.
const (
	MaxDurationSeconds = 3600
	MaxSizeMB          = 1024
	MaxBodyKB          = 16384
	segmentSize        = 16000 // payload bytes per synthesized TCP packet
)

// Config describes what to capture.
type Config struct {
	Scope           string   `json:"scope"`   // all, http, l4
	Sides           string   `json:"sides"`   // client, upstream, both
	Hosts           []string `json:"hosts"`   // HTTP: host names (wildcards like *.example.com); empty for all
	L4Rules         []string `json:"l4Rules"` // Layer 4 proxy rule UUIDs; empty for all
	Clients         []string `json:"clients"` // client IP addresses or CIDR ranges; empty for all
	DurationSeconds int      `json:"durationSeconds"`
	MaxMB           int      `json:"maxMb"`
	MaxBodyKB       int      `json:"maxBodyKb"` // HTTP: body bytes kept per request or response
	Redact          bool     `json:"redact"`    // HTTP: hide Authorization, Cookie and Set-Cookie values
	Note            string   `json:"note"`      // optional label
}

var (
	hostPatternRe = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*$|^\*$`)
	noteRe        = regexp.MustCompile(`^[\p{L}\p{N} ._,:()/+#@-]{0,64}$`)
	uuidRe        = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)
)

// Validate checks and normalises a config.
func (c *Config) Validate() error {
	switch c.Scope {
	case "":
		c.Scope = ScopeAll
	case ScopeAll, ScopeHTTP, ScopeL4:
	default:
		return errors.New("unknown scope")
	}
	switch c.Sides {
	case "":
		c.Sides = SideBoth
	case SideClient, SideUpstream, SideBoth:
	default:
		return errors.New("sides must be client, upstream or both")
	}
	hosts := []string{}
	for _, h := range c.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostPatternRe.MatchString(h) && net.ParseIP(h) == nil {
			return fmt.Errorf("%q is not a host name (wildcards: *.example.com)", h)
		}
		hosts = append(hosts, h)
	}
	if len(hosts) > 64 {
		return errors.New("at most 64 host names")
	}
	c.Hosts = hosts
	rules := []string{}
	for _, r := range c.L4Rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !uuidRe.MatchString(r) {
			return errors.New("invalid Layer 4 proxy rule ID")
		}
		rules = append(rules, strings.ToLower(r))
	}
	c.L4Rules = rules
	clients := []string{}
	for _, cl := range c.Clients {
		cl = strings.TrimSpace(cl)
		if cl == "" {
			continue
		}
		if _, err := parseClient(cl); err != nil {
			return err
		}
		clients = append(clients, cl)
	}
	if len(clients) > 64 {
		return errors.New("at most 64 client addresses")
	}
	c.Clients = clients
	if c.DurationSeconds < 10 || c.DurationSeconds > MaxDurationSeconds {
		return fmt.Errorf("duration must be between 10 seconds and %d minutes", MaxDurationSeconds/60)
	}
	if c.MaxMB < 1 || c.MaxMB > MaxSizeMB {
		return fmt.Errorf("size limit must be between 1 and %d MB", MaxSizeMB)
	}
	if c.MaxBodyKB == 0 {
		c.MaxBodyKB = 1024
	}
	if c.MaxBodyKB < 1 || c.MaxBodyKB > MaxBodyKB {
		return fmt.Errorf("body limit must be between 1 and %d KB", MaxBodyKB)
	}
	c.Note = strings.TrimSpace(c.Note)
	if !noteRe.MatchString(c.Note) {
		return errors.New("note: up to 64 letters, digits, spaces and . _ , : ( ) / + # @ -")
	}
	return nil
}

func parseClient(s string) (*net.IPNet, error) {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR range", s)
		}
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("%q is not an IP address or CIDR range", s)
	}
	bits := 128
	if ip.To4() != nil {
		ip, bits = ip.To4(), 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// hostMatches reports whether host (no port) matches pattern.
func hostMatches(pattern, host string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(host, pattern[1:]) && len(host) > len(pattern)-1
	}
	return host == pattern
}

/* A running capture */

// Capture is one capture in progress.
type Capture struct {
	cfg     Config
	nets    []*net.IPNet
	file    string
	f       *os.File
	bw      *bufio.Writer
	pw      *pcapWriter
	mu      sync.Mutex // guards the writer and counters below
	packets int64
	flows   int64
	exchgs  int64
	started time.Time
	limit   int64
	stopped atomic.Bool // no more packets are written
	ending  atomic.Bool // stop has begun
	reason  string
	timer   *time.Timer
	ipID    atomic.Uint32
	port    atomic.Uint32
	open    map[*TCPFlow]struct{}
	pending map[*httpExchange]struct{}
	mgr     *Manager
}

func (c *Capture) wantsClient(ip net.IP) bool {
	if len(c.nets) == 0 {
		return true
	}
	for _, n := range c.nets {
		if ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// SideClient reports whether client-to-Waymark traffic is recorded.
func (c *Capture) SideClient() bool { return c.cfg.Sides != SideUpstream }

// SideUpstream reports whether Waymark-to-upstream traffic is recorded.
func (c *Capture) SideUpstream() bool { return c.cfg.Sides != SideClient }

// WantsHTTP reports whether an HTTP request for host from client is recorded.
func (c *Capture) WantsHTTP(host string, client net.IP) bool {
	if c.stopped.Load() || c.cfg.Scope == ScopeL4 || !c.wantsClient(client) {
		return false
	}
	if len(c.cfg.Hosts) == 0 {
		return true
	}
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	for _, p := range c.cfg.Hosts {
		if hostMatches(p, host) {
			return true
		}
	}
	return false
}

// WantsL4 reports whether a Layer 4 proxy connection on rule from
// client is recorded.
func (c *Capture) WantsL4(ruleUUID string, client net.IP) bool {
	if c.stopped.Load() || c.cfg.Scope == ScopeHTTP || !c.wantsClient(client) {
		return false
	}
	if len(c.cfg.L4Rules) == 0 {
		return true
	}
	ruleUUID = strings.ToLower(ruleUUID)
	for _, r := range c.cfg.L4Rules {
		if r == ruleUUID {
			return true
		}
	}
	return false
}

// syntheticPort returns a port for connections whose real port cannot be
// used (several HTTP/2 or HTTP/3 requests on one connection).
func (c *Capture) syntheticPort() uint16 {
	return uint16(49152 + c.port.Add(1)%16000)
}

// write records one packet; past the size limit the capture stops.
func (c *Capture) write(ts time.Time, pkt []byte, comment string) {
	if c.stopped.Load() {
		return
	}
	c.mu.Lock()
	if c.pw == nil {
		c.mu.Unlock()
		return
	}
	err := c.pw.packet(ts, pkt, comment)
	c.packets++
	over := c.pw.n >= c.limit
	c.mu.Unlock()
	if err != nil {
		go c.mgr.stop(c, "writing the file failed: "+err.Error())
	} else if over {
		go c.mgr.stop(c, fmt.Sprintf("size limit of %d MB reached", c.cfg.MaxMB))
	}
}

func (c *Capture) nextID() uint16 { return uint16(c.ipID.Add(1)) }

/* TCP conversations */

// TCPFlow is one synthesized TCP connection from A (the side that
// connected) to B.
type TCPFlow struct {
	c       *Capture
	a, b    Endpoint
	seqA    uint32
	seqB    uint32
	mu      sync.Mutex
	closed  bool
	comment string
}

// NewTCPFlow starts a connection from a to b (handshake packets) with an
// optional comment on its first packet.
func (c *Capture) NewTCPFlow(a, b Endpoint, comment string) *TCPFlow {
	return c.newTCPFlowAt(time.Now(), a, b, comment)
}

func (c *Capture) newTCPFlowAt(ts time.Time, a, b Endpoint, comment string) *TCPFlow {
	f := &TCPFlow{c: c, a: a, b: b, seqA: 1000, seqB: 5000}
	c.mu.Lock()
	c.flows++
	if c.open != nil {
		c.open[f] = struct{}{}
	}
	c.mu.Unlock()
	c.write(ts, tcpPacket(a, b, f.seqA-1, 0, tcpSYN, nil, c.nextID()), comment)
	c.write(ts, tcpPacket(b, a, f.seqB-1, f.seqA, tcpSYN|tcpACK, nil, c.nextID()), "")
	c.write(ts, tcpPacket(a, b, f.seqA, f.seqB, tcpACK, nil, c.nextID()), "")
	return f
}

// Send records data sent by A (fromA) or by B.
func (f *TCPFlow) Send(fromA bool, p []byte) {
	f.sendAt(time.Now(), fromA, p)
}

func (f *TCPFlow) sendAt(ts time.Time, fromA bool, p []byte) {
	if f == nil || len(p) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	for len(p) > 0 {
		n := len(p)
		if n > segmentSize {
			n = segmentSize
		}
		if fromA {
			f.c.write(ts, tcpPacket(f.a, f.b, f.seqA, f.seqB, tcpACK|tcpPSH, p[:n], f.c.nextID()), "")
			f.seqA += uint32(n)
		} else {
			f.c.write(ts, tcpPacket(f.b, f.a, f.seqB, f.seqA, tcpACK|tcpPSH, p[:n], f.c.nextID()), "")
			f.seqB += uint32(n)
		}
		p = p[n:]
	}
}

// Close records the end of the connection.
func (f *TCPFlow) Close() { f.closeAt(time.Now()) }

func (f *TCPFlow) closeAt(ts time.Time) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.c.write(ts, tcpPacket(f.a, f.b, f.seqA, f.seqB, tcpFIN|tcpACK, nil, f.c.nextID()), "")
	f.c.write(ts, tcpPacket(f.b, f.a, f.seqB, f.seqA+1, tcpFIN|tcpACK, nil, f.c.nextID()), "")
	f.c.write(ts, tcpPacket(f.a, f.b, f.seqA+1, f.seqB+1, tcpACK, nil, f.c.nextID()), "")
	f.mu.Unlock()
	f.c.mu.Lock()
	delete(f.c.open, f)
	f.c.mu.Unlock()
}

// UDP records one datagram.
func (c *Capture) UDP(src, dst Endpoint, p []byte, comment string) {
	if len(p) > 65000 {
		p = p[:65000]
	}
	c.write(time.Now(), udpPacket(src, dst, p, c.nextID()), comment)
}

/* Manager */

// Status describes the running capture.
type Status struct {
	Config   Config `json:"config"`
	File     string `json:"file"`
	Started  int64  `json:"started"`
	Ends     int64  `json:"ends"`
	Bytes    int64  `json:"bytes"`
	Packets  int64  `json:"packets"`
	Flows    int64  `json:"connections"`
	Requests int64  `json:"requests"`
}

// FileInfo is a stored capture file.
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
	Running bool   `json:"running"`
	Note    string `json:"note,omitempty"`
	Reason  string `json:"stopReason,omitempty"`
}

// Manager owns the capture folder and the running capture.
type Manager struct {
	dir       string
	node      func() string
	logf      func(string)
	retention time.Duration
	maxFiles  int
	mu        sync.Mutex
	active    atomic.Pointer[Capture]
	notes     map[string]string // file -> note
	reasons   map[string]string // file -> why it stopped
}

var defaultManager atomic.Pointer[Manager]

// Active returns the running capture, or nil.
func Active() *Capture {
	m := defaultManager.Load()
	if m == nil {
		return nil
	}
	return m.active.Load()
}

var fileNameRe = regexp.MustCompile(`^waymark-[a-z0-9-]{1,32}-\d{8}-\d{6}(-\d{1,3})?\.pcapng$`)

// NewManager creates the capture manager using dir (mode 0700) for files.
func NewManager(dir string, retention time.Duration, node func() string, logf func(string)) (*Manager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	os.Chmod(dir, 0700)
	if logf == nil {
		logf = func(string) {}
	}
	m := &Manager{dir: dir, node: node, logf: logf, retention: retention, maxFiles: 50, notes: map[string]string{}, reasons: map[string]string{}}
	m.loadIndex()
	m.cleanup()
	go func() {
		for range time.Tick(10 * time.Minute) {
			m.cleanup()
		}
	}()
	defaultManager.Store(m)
	return m, nil
}

// Retention is how long files are kept.
func (m *Manager) Retention() time.Duration { return m.retention }

func (m *Manager) nodeName() string {
	n := "node"
	if m.node != nil {
		n = strings.ToLower(m.node())
	}
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, n)
	if clean == "" || len(clean) > 32 {
		clean = "node"
	}
	return clean
}

// Start begins a capture.
func (m *Manager) Start(cfg Config) (Status, error) {
	if err := cfg.Validate(); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active.Load() != nil {
		return Status{}, errors.New("a capture is already running; stop it first")
	}
	m.cleanupLocked()
	now := time.Now()
	base := "waymark-" + m.nodeName() + "-" + now.Format("20060102-150405")
	name := base + ".pcapng"
	for i := 1; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(m.dir, name)); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d.pcapng", base, i)
	}
	f, err := os.OpenFile(filepath.Join(m.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Status{}, fmt.Errorf("could not create the capture file: %v", err)
	}
	bw := bufio.NewWriterSize(f, 256<<10)
	desc := "Waymark proxy traffic (reconstructed; HTTPS shown decrypted)"
	pw, err := newPcapng(bw, "Waymark", desc)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return Status{}, err
	}
	c := &Capture{cfg: cfg, file: name, f: f, bw: bw, pw: pw, started: now, limit: int64(cfg.MaxMB) << 20,
		open: map[*TCPFlow]struct{}{}, pending: map[*httpExchange]struct{}{}, mgr: m}
	for _, cl := range cfg.Clients {
		n, _ := parseClient(cl)
		c.nets = append(c.nets, n)
	}
	c.timer = time.AfterFunc(time.Duration(cfg.DurationSeconds)*time.Second, func() {
		m.stop(c, fmt.Sprintf("time limit of %s reached", time.Duration(cfg.DurationSeconds)*time.Second))
	})
	m.notes[name] = cfg.Note
	m.saveIndexLocked()
	m.active.Store(c)
	return c.status(), nil
}

// Stop ends the running capture (if any).
func (m *Manager) Stop(reason string) (string, bool) {
	c := m.active.Load()
	if c == nil {
		return "", false
	}
	m.stop(c, reason)
	return c.file, true
}

func (m *Manager) stop(c *Capture, reason string) {
	if !c.ending.CompareAndSwap(false, true) {
		return
	}
	c.timer.Stop()
	m.active.CompareAndSwap(c, nil)
	// Writes the exchanges still open
	c.mu.Lock()
	pending := make([]*httpExchange, 0, len(c.pending))
	for ex := range c.pending {
		pending = append(pending, ex)
	}
	flows := make([]*TCPFlow, 0, len(c.open))
	for f := range c.open {
		flows = append(flows, f)
	}
	c.mu.Unlock()
	for _, ex := range pending {
		ex.flushOnStop()
	}
	for _, f := range flows {
		f.Close()
	}
	c.stopped.Store(true)
	c.mu.Lock()
	c.reason = reason
	c.bw.Flush()
	c.f.Close()
	c.pw = nil
	size := int64(0)
	if st, err := os.Stat(filepath.Join(m.dir, c.file)); err == nil {
		size = st.Size()
	}
	packets, flowCount, exchgs := c.packets, c.flows, c.exchgs
	c.mu.Unlock()
	m.mu.Lock()
	m.reasons[c.file] = reason
	m.saveIndexLocked()
	m.mu.Unlock()
	m.logf(fmt.Sprintf("Traffic capture %s stopped (%s): %d packets, %d connections, %d HTTP requests, %s",
		c.file, reason, packets, flowCount, exchgs, humanBytes(size)))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func (c *Capture) status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	if c.pw != nil {
		n = c.pw.n
	}
	return Status{Config: c.cfg, File: c.file, Started: c.started.Unix(), Ends: c.started.Add(time.Duration(c.cfg.DurationSeconds) * time.Second).Unix(),
		Bytes: n, Packets: c.packets, Flows: c.flows, Requests: c.exchgs}
}

// Running returns the running capture's status.
func (m *Manager) Running() (Status, bool) {
	c := m.active.Load()
	if c == nil {
		return Status{}, false
	}
	return c.status(), true
}

// Files lists the stored captures, newest first.
func (m *Manager) Files() []FileInfo {
	out := []FileInfo{}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return out
	}
	running := ""
	if c := m.active.Load(); c != nil {
		running = c.file
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range entries {
		if !fileNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfo{Name: e.Name(), Size: info.Size(), Created: info.ModTime().Unix(),
			Expires: info.ModTime().Add(m.retention).Unix(), Running: e.Name() == running,
			Note: m.notes[e.Name()], Reason: m.reasons[e.Name()]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// Path returns the full path of a stored capture file.
func (m *Manager) Path(name string) (string, error) {
	if !fileNameRe.MatchString(name) {
		return "", errors.New("unknown capture file")
	}
	p := filepath.Join(m.dir, name)
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("unknown capture file")
	}
	return p, nil
}

// Delete removes a stored capture (not the running one).
func (m *Manager) Delete(name string) error {
	p, err := m.Path(name)
	if err != nil {
		return err
	}
	if c := m.active.Load(); c != nil && c.file == name {
		return errors.New("stop the capture before deleting its file")
	}
	m.mu.Lock()
	delete(m.notes, name)
	delete(m.reasons, name)
	m.saveIndexLocked()
	m.mu.Unlock()
	return os.Remove(p)
}

func (m *Manager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
}

// cleanupLocked deletes files past the retention time, and the oldest
// beyond maxFiles.
func (m *Manager) cleanupLocked() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	running := ""
	if c := m.active.Load(); c != nil {
		running = c.file
	}
	type file struct {
		name string
		mod  time.Time
	}
	files := []file{}
	for _, e := range entries {
		if !fileNameRe.MatchString(e.Name()) || e.Name() == running {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for i, f := range files {
		if time.Since(f.mod) > m.retention || i >= m.maxFiles {
			if os.Remove(filepath.Join(m.dir, f.name)) == nil {
				delete(m.notes, f.name)
				delete(m.reasons, f.name)
				m.logf("Deleted traffic capture " + f.name + " (retention)")
			}
		}
	}
	m.saveIndexLocked()
}

// The notes and stop reasons of stored files are kept in .index.json.
type fileIndex struct {
	Notes   map[string]string `json:"notes"`
	Reasons map[string]string `json:"reasons"`
}

func (m *Manager) loadIndex() {
	b, err := os.ReadFile(filepath.Join(m.dir, ".index.json"))
	if err != nil {
		return
	}
	var idx fileIndex
	if json.Unmarshal(b, &idx) != nil {
		return
	}
	for k, v := range idx.Notes {
		if fileNameRe.MatchString(k) {
			m.notes[k] = v
		}
	}
	for k, v := range idx.Reasons {
		if fileNameRe.MatchString(k) {
			m.reasons[k] = v
		}
	}
}

func (m *Manager) saveIndexLocked() {
	b, _ := json.Marshal(fileIndex{Notes: m.notes, Reasons: m.reasons})
	utils.WriteFileAtomic(filepath.Join(m.dir, ".index.json"), b, 0600)
}
