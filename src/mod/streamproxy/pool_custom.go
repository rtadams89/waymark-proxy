package streamproxy

/*
	pool_custom.go (custom fork)

	Upstream pools for stream proxies: several upstream servers per rule,
	load balancing between the healthy ones, and health checks (run by
	mod/healthcheck from the main package, which reports each upstream's
	state with Manager.SetUpstreamOnline).

	Load balancing modes:
	- roundrobin: each new TCP connection or UDP client session goes to the
	  next healthy upstream
	- leastconn: to the healthy upstream with the fewest open connections
	  (TCP) or sessions (UDP)
	- sourcehash: the same client IP address always goes to the same
	  upstream while the set of healthy upstreams stays the same
	  (rendezvous hashing, so a change only moves the clients of the
	  upstream that came or went)
	- failover: the first healthy upstream in the list; the others are
	  backups, used in order

	TCP: when connecting to the chosen upstream fails, the next candidate is
	tried before the client connection is given up. With no healthy upstream
	the client connection is closed at once. UDP: a new client session goes
	to the first candidate; with no healthy upstream its packets are dropped.

	A connection failure (refused, unreachable, timed out; for UDP an ICMP
	port unreachable) is reported to passive monitoring.

	With CloseOnUpstreamDown, connections and sessions to an upstream are
	closed when health checks take it out of load balancing.
*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
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

// Load balancing modes.
const (
	LBRoundRobin = "roundrobin"
	LBLeastConn  = "leastconn"
	LBSourceHash = "sourcehash"
	LBFailover   = "failover"
)

// MaxUpstreams is the most upstream servers one stream proxy can have.
const MaxUpstreams = 32

const upstreamDialTimeout = 5 * time.Second

// upstreamStats counts one upstream's use by one rule.
type upstreamStats struct {
	active atomic.Int64
	total  atomic.Uint64
	failed atomic.Uint64
	open   map[io.Closer]struct{} // guarded by upstreamPool.mu
}

type upstreamPool struct {
	mu    sync.Mutex
	stats map[string]*upstreamStats
	rr    atomic.Uint64
}

func (c *ProxyRelayInstance) getPool() *upstreamPool {
	c.poolOnce.Do(func() { c.pool = &upstreamPool{stats: map[string]*upstreamStats{}} })
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

// HealthRuleID is this rule's ID for health checks ("stream:<uuid>").
func (c *ProxyRelayInstance) HealthRuleID() string {
	return "stream:" + strings.ToLower(c.UUID)
}

// UpstreamList is the rule's upstream servers. Rules saved before pools
// existed have only ProxyTargetAddr.
func (c *ProxyRelayInstance) UpstreamList() []string {
	c.poolMu.RLock()
	defer c.poolMu.RUnlock()
	if len(c.Upstreams) > 0 {
		return append([]string{}, c.Upstreams...)
	}
	if t := strings.TrimSpace(c.ProxyTargetAddr); t != "" {
		return []string{t}
	}
	return nil
}

func (c *ProxyRelayInstance) lbMode() string {
	c.poolMu.RLock()
	defer c.poolMu.RUnlock()
	switch c.LoadBalancing {
	case LBLeastConn, LBSourceHash, LBFailover:
		return c.LoadBalancing
	}
	return LBRoundRobin
}

func (c *ProxyRelayInstance) upstreamOnline(addr string) bool {
	if c.parent == nil {
		return true
	}
	return c.parent.UpstreamOnline(addr)
}

// candidates returns the healthy upstreams in the order to try them for a
// new connection or session from clientIP.
func (c *ProxyRelayInstance) candidates(clientIP string) []string {
	all := c.UpstreamList()
	healthy := make([]string, 0, len(all))
	for _, a := range all {
		if c.upstreamOnline(a) {
			healthy = append(healthy, a)
		}
	}
	if len(healthy) <= 1 {
		return healthy
	}
	pool := c.getPool()
	switch c.lbMode() {
	case LBFailover:
		return healthy
	case LBSourceHash:
		score := func(a string) uint64 {
			h := fnv.New64a()
			h.Write([]byte(clientIP))
			h.Write([]byte{0})
			h.Write([]byte(a))
			return h.Sum64()
		}
		sort.SliceStable(healthy, func(i, j int) bool { return score(healthy[i]) > score(healthy[j]) })
		return healthy
	case LBLeastConn:
		start := int(pool.rr.Add(1) % uint64(len(healthy)))
		rotated := append(append([]string{}, healthy[start:]...), healthy[:start]...)
		sort.SliceStable(rotated, func(i, j int) bool {
			return pool.get(rotated[i]).active.Load() < pool.get(rotated[j]).active.Load()
		})
		return rotated
	default:
		start := int((pool.rr.Add(1) - 1) % uint64(len(healthy)))
		return append(append([]string{}, healthy[start:]...), healthy[:start]...)
	}
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
			return nil, "", errors.New("no upstream configured")
		}
		return nil, "", errors.New("no healthy upstream")
	}
	var lastErr error
	for _, addr := range cands {
		target, err := net.DialTimeout("tcp", addr, upstreamDialTimeout)
		if err == nil {
			return target, addr, nil
		}
		lastErr = err
		c.LogMsg("[x] connect to upstream ["+addr+"] failed: "+err.Error(), nil)
		c.reportConnectFailure(addr, err)
	}
	return nil, "", fmt.Errorf("all upstreams failed (last: %v)", lastErr)
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
	for _, c := range m.Configs {
		c.poolMu.RLock()
		closeOnDown := c.CloseOnUpstreamDown
		c.poolMu.RUnlock()
		if !closeOnDown {
			continue
		}
		for _, u := range c.UpstreamList() {
			if u == addr {
				if n := c.closeUpstreamConns(addr); n > 0 {
					m.logf(fmt.Sprintf("Closed %d connection(s) of stream proxy %s to %s (upstream is down)", n, c.Name, addr), nil)
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
		return nil, errors.New("add at least one upstream server")
	}
	if len(out) > MaxUpstreams {
		return nil, fmt.Errorf("at most %d upstream servers", MaxUpstreams)
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
	Upstreams           []string            `json:"upstreams"`
	LoadBalancing       string              `json:"loadBalancing"`
	HealthCheck         *healthcheck.Config `json:"healthCheck"` // nil: no checks
	CloseOnUpstreamDown bool                `json:"closeOnUpstreamDown"`
}

// Validate checks settings for a rule using the given protocols.
func (p *PoolSettings) Validate(useTCP, useUDP bool) error {
	ups, err := ValidateUpstreams(p.Upstreams)
	if err != nil {
		return err
	}
	p.Upstreams = ups
	switch p.LoadBalancing {
	case "":
		p.LoadBalancing = LBRoundRobin
	case LBRoundRobin, LBLeastConn, LBSourceHash, LBFailover:
	default:
		return errors.New("unknown load balancing mode")
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
			return errors.New("stream proxies support TCP, UDP and ping health checks")
		}
		// Stream connections have no HTTP status codes
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
	if err := p.Validate(c.UseTCP, c.UseUDP); err != nil {
		return err
	}
	c.poolMu.Lock()
	c.Upstreams = p.Upstreams
	c.ProxyTargetAddr = p.Upstreams[0] // kept for older versions and the list view
	c.LoadBalancing = p.LoadBalancing
	c.HealthCheck = p.HealthCheck
	c.CloseOnUpstreamDown = p.CloseOnUpstreamDown
	c.poolMu.Unlock()
	m.SaveConfigToDatabase()
	m.changed()
	// Running connections keep their upstream; new ones use the new list
	return nil
}

// UpstreamInfo is one upstream's state for the UI.
type UpstreamInfo struct {
	Address string `json:"address"`
	Online  bool   `json:"online"` // health checks let it receive traffic
	Active  int64  `json:"active"` // open connections or sessions
	Total   uint64 `json:"total"`
	Failed  uint64 `json:"failed"` // failed connection attempts
}

// Upstreams returns each upstream's state for a rule.
func (c *ProxyRelayInstance) UpstreamInfos() []UpstreamInfo {
	out := []UpstreamInfo{}
	pool := c.getPool()
	for _, a := range c.UpstreamList() {
		s := pool.get(a)
		out = append(out, UpstreamInfo{Address: a, Online: c.upstreamOnline(a), Active: s.active.Load(), Total: s.total.Load(), Failed: s.failed.Load()})
	}
	return out
}

/* API */

// HandleSetPool saves a rule's upstreams, load balancing and health checks.
// POST /api/streamprox/config/pool  uuid=<rule>  pool=<json PoolSettings>
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
// GET /api/streamprox/config/upstreams
func (m *Manager) HandleUpstreamStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string][]UpstreamInfo{}
	for _, c := range m.Configs {
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
			c.LogMsg("[UDP] cannot resolve upstream "+addr+": "+err.Error(), nil)
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
	c.LogMsg("[UDP] no healthy upstream for client "+client.String()+", dropping its packets", nil)
	return nil
}
