package l4proxy

/*
	pool_custom.go (custom fork)

	Destination pools for Layer 4 rules, load balanced the same way as HTTP
	proxy rules (mod/dynamicproxy/loadbalance):
	- each destination has a weight (0-100) and can be turned off; weight 0
	  makes it a backup, used only when no destination with a weight above 0
	  is healthy
	- weighted (the default): a new TCP connection or UDP client session goes
	  to a random healthy destination, in proportion to its weight
	- leastconn: to the healthy destination with the fewest open connections
	  (TCP) or sessions (UDP) for its weight
	- sticky sessions: a client IP address keeps going to the destination it
	  was given while that destination is healthy (remembered for a day
	  after its last connection), like the HTTP sticky session cookie

	Health checks are run by mod/healthcheck from the main package, which
	reports each destination's state with Manager.SetUpstreamOnline.

	TCP: when connecting to the chosen destination fails, the others are
	tried before the client connection is given up. With no healthy
	destination the client connection is closed at once. UDP: a new client
	session goes to the first candidate; with no healthy destination its
	packets are dropped.

	A connection failure (refused, unreachable, timed out; for UDP an ICMP
	port unreachable) is reported to passive monitoring.

	With CloseOnUpstreamDown, connections and sessions to a destination are
	closed when health checks take it out of load balancing.

	A rule given only an address list (Upstreams or ProxyTargetAddr, as the
	edit API and hand-written files may) gets one destination per address
	with weight 1 (normalizePool).
*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/utils"
)

// Load balancing methods, the same as HTTP proxy rules.
const (
	LBWeighted  = "weighted"
	LBLeastConn = "leastconn"
)

// MaxUpstreams is the most destination servers one Layer 4 rule can have.
const MaxUpstreams = 32

// MaxWeight is the highest destination weight (as for HTTP proxy rules).
const MaxWeight = 100

const upstreamDialTimeout = 5 * time.Second

// Sticky sessions: how long a client IP address is remembered after its
// last connection, and how many are remembered per rule.
const (
	stickyTTL = 24 * time.Hour
	stickyMax = 65536
)

// Destination is one destination server of a Layer 4 rule.
type Destination struct {
	Address string `json:"address"` // host:port
	Weight  int    `json:"weight"`  // 0-100; 0: backup only
	Enabled bool   `json:"enabled"` // off: kept but not used
}

// upstreamStats counts one upstream's use by one rule.
type upstreamStats struct {
	active atomic.Int64
	total  atomic.Uint64
	failed atomic.Uint64
	open   map[io.Closer]struct{} // guarded by upstreamPool.mu
}

type upstreamPool struct {
	mu     sync.Mutex
	stats  map[string]*upstreamStats
	sticky map[string]stickyEntry // client IP -> destination (guarded by mu)
}

type stickyEntry struct {
	addr string
	seen time.Time
}

func (c *ProxyRelayInstance) getPool() *upstreamPool {
	c.poolOnce.Do(func() {
		c.pool = &upstreamPool{stats: map[string]*upstreamStats{}, sticky: map[string]stickyEntry{}}
	})
	return c.pool
}

func (p *upstreamPool) get(addr string) *upstreamStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats[addr]
	if s == nil {
		s = &upstreamStats{open: map[io.Closer]struct{}{}}
		p.stats[addr] = s
	}
	return s
}

// HealthRuleID is this rule's ID for health checks ("l4:<uuid>").
func (c *ProxyRelayInstance) HealthRuleID() string {
	return "l4:" + strings.ToLower(c.UUID)
}

// normalizePool fills Destinations from a plain address list, sets an
// unknown load balancing method to weighted, and keeps Upstreams and
// ProxyTargetAddr in step with Destinations. Call with poolMu held for
// writing (or before the rule is shared). Reports whether anything changed.
func (c *ProxyRelayInstance) normalizePool() bool {
	changed := false
	if len(c.Destinations) == 0 {
		addrs := c.Upstreams
		if len(addrs) == 0 && strings.TrimSpace(c.ProxyTargetAddr) != "" {
			addrs = []string{strings.TrimSpace(c.ProxyTargetAddr)}
		}
		for _, a := range addrs {
			c.Destinations = append(c.Destinations, Destination{Address: a, Weight: 1, Enabled: true})
		}
		changed = len(addrs) > 0
	}
	switch c.LoadBalancing {
	case LBWeighted, LBLeastConn:
	default:
		c.LoadBalancing = LBWeighted
		changed = true
	}
	c.syncAddresses()
	return changed
}

// syncAddresses mirrors Destinations into Upstreams (every address, for
// the list view) and ProxyTargetAddr (the first enabled
// destination).
func (c *ProxyRelayInstance) syncAddresses() {
	if len(c.Destinations) == 0 {
		return
	}
	addrs := make([]string, 0, len(c.Destinations))
	first := ""
	for _, d := range c.Destinations {
		addrs = append(addrs, d.Address)
		if first == "" && d.Enabled {
			first = d.Address
		}
	}
	if first == "" {
		first = addrs[0]
	}
	c.Upstreams = addrs
	c.ProxyTargetAddr = first
}

// DestinationList is a copy of the rule's destinations, enabled or not.
func (c *ProxyRelayInstance) DestinationList() []Destination {
	c.poolMu.RLock()
	defer c.poolMu.RUnlock()
	if len(c.Destinations) > 0 {
		return append([]Destination{}, c.Destinations...)
	}
	// A rule that has not been normalised yet (tests, older callers)
	out := []Destination{}
	addrs := c.Upstreams
	if len(addrs) == 0 && strings.TrimSpace(c.ProxyTargetAddr) != "" {
		addrs = []string{strings.TrimSpace(c.ProxyTargetAddr)}
	}
	for _, a := range addrs {
		out = append(out, Destination{Address: a, Weight: 1, Enabled: true})
	}
	return out
}

// UpstreamList is the addresses of the rule's enabled destinations: the
// ones that are load balanced and health checked.
func (c *ProxyRelayInstance) UpstreamList() []string {
	var out []string
	for _, d := range c.DestinationList() {
		if d.Enabled {
			out = append(out, d.Address)
		}
	}
	return out
}

func (c *ProxyRelayInstance) lbSettings() (mode string, sticky bool) {
	c.poolMu.RLock()
	defer c.poolMu.RUnlock()
	if c.LoadBalancing == LBLeastConn {
		return LBLeastConn, c.StickySessions
	}
	return LBWeighted, c.StickySessions
}

func (c *ProxyRelayInstance) upstreamOnline(addr string) bool {
	if c.parent == nil {
		return true
	}
	return c.parent.UpstreamOnline(addr)
}

// candidates returns the healthy enabled destinations in the order to try
// them for a new connection or session from clientIP: the chosen one
// first, then the other destinations with a weight above 0, then backups.
func (c *ProxyRelayInstance) candidates(clientIP string) []string {
	mode, sticky := c.lbSettings()
	var primary, backup []Destination
	for _, d := range c.DestinationList() {
		if !d.Enabled || !c.upstreamOnline(d.Address) {
			continue
		}
		if d.Weight > 0 {
			primary = append(primary, d)
		} else {
			backup = append(backup, d)
		}
	}
	if len(primary) == 0 {
		// Only backups are healthy: they share the load evenly
		primary, backup = backup, nil
		for i := range primary {
			primary[i].Weight = 1
		}
	}
	if len(primary) == 0 {
		return nil
	}
	pool := c.getPool()
	ordered := c.order(primary, mode)
	if sticky && clientIP != "" {
		if addr, ok := pool.stickyGet(clientIP); ok {
			for i, d := range ordered {
				if d.Address == addr {
					ordered = append([]Destination{d}, append(ordered[:i:i], ordered[i+1:]...)...)
					break
				}
			}
		}
		pool.stickySet(clientIP, ordered[0].Address)
	}
	out := make([]string, 0, len(ordered)+len(backup))
	for _, d := range ordered {
		out = append(out, d.Address)
	}
	rand.Shuffle(len(backup), func(i, j int) { backup[i], backup[j] = backup[j], backup[i] })
	for _, d := range backup {
		out = append(out, d.Address)
	}
	return out
}

// order sorts destinations (all with weight > 0) for a new connection:
// leastconn by open connections per weight, weighted by repeated weighted
// random draws, so the first one is picked in proportion to its weight.
func (c *ProxyRelayInstance) order(list []Destination, mode string) []Destination {
	list = append([]Destination{}, list...)
	if mode == LBLeastConn {
		pool := c.getPool()
		rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] }) // even ties
		load := func(d Destination) float64 {
			return float64(pool.get(d.Address).active.Load()+1) / float64(d.Weight)
		}
		sort.SliceStable(list, func(i, j int) bool { return load(list[i]) < load(list[j]) })
		return list
	}
	out := make([]Destination, 0, len(list))
	for len(list) > 0 {
		total := 0
		for _, d := range list {
			total += d.Weight
		}
		r := rand.Intn(total)
		for i, d := range list {
			if r < d.Weight {
				out = append(out, d)
				list = append(list[:i], list[i+1:]...)
				break
			}
			r -= d.Weight
		}
	}
	return out
}

func (p *upstreamPool) stickyGet(client string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.sticky[client]
	if !ok || time.Since(e.seen) > stickyTTL {
		return "", false
	}
	return e.addr, true
}

func (p *upstreamPool) stickySet(client, addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sticky[client]; !ok && len(p.sticky) >= stickyMax {
		now := time.Now()
		for k, e := range p.sticky {
			if now.Sub(e.seen) > stickyTTL {
				delete(p.sticky, k)
			}
		}
		// Still full: forget a quarter (map order is random)
		for k := range p.sticky {
			if len(p.sticky) < stickyMax*3/4 {
				break
			}
			delete(p.sticky, k)
		}
	}
	p.sticky[client] = stickyEntry{addr: addr, seen: time.Now()}
}

// forgetSticky drops the remembered clients (when the destinations change).
func (p *upstreamPool) forgetSticky() {
	p.mu.Lock()
	p.sticky = map[string]stickyEntry{}
	p.mu.Unlock()
}

// track counts a connection or session to addr and remembers it for
// CloseOnUpstreamDown. The returned function undoes it.
func (c *ProxyRelayInstance) track(addr string, conn io.Closer) func() {
	pool := c.getPool()
	s := pool.get(addr)
	s.active.Add(1)
	s.total.Add(1)
	pool.mu.Lock()
	s.open[conn] = struct{}{}
	pool.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.active.Add(-1)
			pool.mu.Lock()
			delete(s.open, conn)
			pool.mu.Unlock()
		})
	}
}

func (c *ProxyRelayInstance) reportConnectFailure(addr string, err error) {
	c.getPool().get(addr).failed.Add(1)
	if c.parent != nil && c.parent.Options.ObserveConnect != nil {
		c.parent.Options.ObserveConnect(c.HealthRuleID(), addr, err)
	}
}

// dialUpstream connects a new TCP client to the first candidate that
// accepts the connection.
func (c *ProxyRelayInstance) dialUpstream(client net.Conn) (net.Conn, string, error) {
	clientIP := ""
	if a, ok := client.RemoteAddr().(*net.TCPAddr); ok {
		clientIP = a.IP.String()
	}
	cands := c.candidates(clientIP)
	if len(cands) == 0 {
		if len(c.UpstreamList()) == 0 {
			return nil, "", errors.New("no destination configured")
		}
		return nil, "", errors.New("no healthy destination")
	}
	var lastErr error
	for _, addr := range cands {
		target, err := net.DialTimeout("tcp", addr, upstreamDialTimeout)
		if err == nil {
			return target, addr, nil
		}
		lastErr = err
		c.LogMsg("[x] connect to destination ["+addr+"] failed: "+err.Error(), nil)
		c.reportConnectFailure(addr, err)
	}
	return nil, "", fmt.Errorf("all destinations failed (last: %v)", lastErr)
}

// closeUpstreamConns closes this rule's open connections and sessions to
// addr (CloseOnUpstreamDown).
func (c *ProxyRelayInstance) closeUpstreamConns(addr string) int {
	pool := c.getPool()
	pool.mu.Lock()
	s := pool.stats[addr]
	var conns []io.Closer
	if s != nil {
		for conn := range s.open {
			conns = append(conns, conn)
		}
	}
	pool.mu.Unlock()
	for _, conn := range conns {
		conn.Close()
	}
	return len(conns)
}

/* Manager: upstream health */

// UpstreamOnline reports whether health checks let addr receive traffic.
func (m *Manager) UpstreamOnline(addr string) bool {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	return !m.offline[addr]
}

// SetUpstreamOnline is called by health checks when an upstream address
// enters or leaves load balancing.
func (m *Manager) SetUpstreamOnline(addr string, online bool) {
	m.healthMu.Lock()
	if m.offline == nil {
		m.offline = map[string]bool{}
	}
	if online {
		delete(m.offline, addr)
	} else {
		m.offline[addr] = true
	}
	m.healthMu.Unlock()
	if online {
		return
	}
	for _, c := range m.List() {
		c.poolMu.RLock()
		closeOnDown, name := c.CloseOnUpstreamDown, c.Name
		c.poolMu.RUnlock()
		if !closeOnDown {
			continue
		}
		for _, u := range c.UpstreamList() {
			if u == addr {
				if n := c.closeUpstreamConns(addr); n > 0 {
					m.logf(fmt.Sprintf("Closed %d connection(s) of Layer 4 rule %s to %s (destination is down)", n, name, addr), nil)
				}
			}
		}
	}
}

func (m *Manager) changed() {
	if m.Options.OnChange != nil {
		m.Options.OnChange()
	}
}

/* Settings */

// ValidateUpstreams checks and normalises a list of upstream addresses
// (host:port, host an IP address or host name).
func ValidateUpstreams(list []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range list {
		a := strings.TrimSpace(raw)
		if a == "" {
			continue
		}
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			return nil, fmt.Errorf("%q: use host:port, e.g. 10.0.0.5:5432 or [fd00::5]:53", a)
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("%q: port must be 1-65535", a)
		}
		if net.ParseIP(host) == nil && !validHostname(host) {
			return nil, fmt.Errorf("%q: not an IP address or host name", a)
		}
		a = net.JoinHostPort(host, port)
		if seen[a] {
			return nil, fmt.Errorf("%s is listed twice", a)
		}
		seen[a] = true
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, errors.New("add at least one destination server")
	}
	if len(out) > MaxUpstreams {
		return nil, fmt.Errorf("at most %d destination servers", MaxUpstreams)
	}
	return out, nil
}

func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// PoolSettings are the pool settings of one rule, as the UI sends them.
type PoolSettings struct {
	Destinations        []Destination       `json:"destinations"`
	Upstreams           []string            `json:"upstreams,omitempty"` // older clients: addresses, each with weight 1
	LoadBalancing       string              `json:"loadBalancing"`       // weighted (default) or leastconn
	StickySessions      bool                `json:"stickySessions"`
	HealthCheck         *healthcheck.Config `json:"healthCheck"` // nil: no checks
	CloseOnUpstreamDown bool                `json:"closeOnUpstreamDown"`
}

// Validate checks settings for a rule using the given protocols.
func (p *PoolSettings) Validate(useTCP, useUDP bool) error {
	if len(p.Destinations) == 0 {
		for _, a := range p.Upstreams {
			p.Destinations = append(p.Destinations, Destination{Address: a, Weight: 1, Enabled: true})
		}
	}
	addrs := make([]string, 0, len(p.Destinations))
	for _, d := range p.Destinations {
		addrs = append(addrs, d.Address)
	}
	ups, err := ValidateUpstreams(addrs)
	if err != nil {
		return err
	}
	if len(ups) != len(p.Destinations) {
		return errors.New("a destination address is empty")
	}
	for i := range p.Destinations {
		p.Destinations[i].Address = ups[i]
		if w := p.Destinations[i].Weight; w < 0 || w > MaxWeight {
			return fmt.Errorf("%s: weight must be a whole number from 0 to %d", ups[i], MaxWeight)
		}
	}
	p.Upstreams = nil
	switch p.LoadBalancing {
	case "":
		p.LoadBalancing = LBWeighted
	case LBWeighted, LBLeastConn:
	default:
		return errors.New("unknown load balancing method")
	}
	if p.HealthCheck != nil {
		cfg, err := p.HealthCheck.Validate()
		if err != nil {
			return err
		}
		switch cfg.Type {
		case healthcheck.CheckTCP:
			if !useTCP {
				return errors.New("a TCP health check needs TCP on this rule; for UDP use a UDP check or ping")
			}
		case healthcheck.CheckUDP:
			if !useUDP {
				return errors.New("a UDP health check needs UDP on this rule")
			}
		case healthcheck.CheckICMP:
		default:
			return errors.New("Layer 4 rules support TCP, UDP and ping health checks")
		}
		// Layer 4 connections have no HTTP status codes
		cfg.CountServerErrors = false
		if cfg.PassiveEnabled && !cfg.CountTimeouts && !cfg.CountConnectionErrors {
			return errors.New("passive monitoring needs connection failures or timeouts to count")
		}
		if !cfg.Enabled() {
			cfg = nil
		}
		p.HealthCheck = cfg
	}
	return nil
}

// ApplyPoolSettings saves a rule's pool settings and restarts it if it is
// running.
func (m *Manager) ApplyPoolSettings(uuid string, p *PoolSettings) error {
	c, err := m.GetConfigByUUID(uuid)
	if err != nil {
		return err
	}
	v := c.View()
	if err := p.Validate(v.UseTCP, v.UseUDP); err != nil {
		return err
	}
	c.poolMu.Lock()
	c.Destinations = append([]Destination{}, p.Destinations...)
	c.syncAddresses() // Upstreams and ProxyTargetAddr, for the list view
	c.LoadBalancing = p.LoadBalancing
	c.StickySessions = p.StickySessions
	c.HealthCheck = p.HealthCheck
	c.CloseOnUpstreamDown = p.CloseOnUpstreamDown
	c.poolMu.Unlock()
	if !p.StickySessions {
		c.getPool().forgetSticky()
	}
	m.SaveConfigToDatabase()
	m.changed()
	// Running connections keep their destination; new ones use the new list
	return nil
}

// UpstreamInfo is one destination's state for the UI.
type UpstreamInfo struct {
	Address string `json:"address"`
	Weight  int    `json:"weight"`
	Enabled bool   `json:"enabled"`
	Online  bool   `json:"online"` // health checks let it receive traffic
	Active  int64  `json:"active"` // open connections or sessions
	Total   uint64 `json:"total"`
	Failed  uint64 `json:"failed"` // failed connection attempts
}

// Upstreams returns each upstream's state for a rule.
func (c *ProxyRelayInstance) UpstreamInfos() []UpstreamInfo {
	out := []UpstreamInfo{}
	pool := c.getPool()
	for _, d := range c.DestinationList() {
		s := pool.get(d.Address)
		out = append(out, UpstreamInfo{Address: d.Address, Weight: d.Weight, Enabled: d.Enabled, Online: c.upstreamOnline(d.Address), Active: s.active.Load(), Total: s.total.Load(), Failed: s.failed.Load()})
	}
	return out
}

/* API */

// HandleSetPool saves a rule's upstreams, load balancing and health checks.
// POST /api/l4proxy/config/pool  uuid=<rule>  pool=<json PoolSettings>
func (m *Manager) HandleSetPool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uuid, err := utils.PostPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "uuid not defined")
		return
	}
	raw, err := utils.PostPara(r, "pool")
	if err != nil {
		utils.SendErrorResponse(w, "pool not defined")
		return
	}
	p := &PoolSettings{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		utils.SendErrorResponse(w, "invalid settings: "+err.Error())
		return
	}
	if err := m.ApplyPoolSettings(uuid, p); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendOK(w)
}

// HandleUpstreamStatus returns every rule's upstream states.
// GET /api/l4proxy/config/upstreams
func (m *Manager) HandleUpstreamStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string][]UpstreamInfo{}
	for _, c := range m.List() {
		out[c.UUID] = c.UpstreamInfos()
	}
	js, _ := json.Marshal(out)
	utils.SendJSONResponse(w, string(js))
}

// newUDPSession opens a session for a new UDP client to the first candidate
// upstream that can be reached (resolved and a socket opened).
func (c *ProxyRelayInstance) newUDPSession(client *net.UDPAddr) *udpClientServerConn {
	for _, addr := range c.candidates(client.IP.String()) {
		srv, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			c.LogMsg("[UDP] cannot resolve destination "+addr+": "+err.Error(), nil)
			c.reportConnectFailure(addr, err)
			continue
		}
		conn := createNewUDPConn(srv, client, c.udpIdleTimeout())
		if conn == nil {
			continue
		}
		conn.upstream = addr
		conn.release = c.track(addr, conn)
		return conn
	}
	c.LogMsg("[UDP] no healthy destination for client "+client.String()+", dropping its packets", nil)
	return nil
}
