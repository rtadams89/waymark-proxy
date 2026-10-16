package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Notifier tells the load balancer whether an upstream address may receive
// traffic. It is loadbalance.RouteManager.NotifyHostOnlineState.
type Notifier func(addr string, online bool)

// Target is one upstream of one proxy rule.
type Target struct {
	RuleID   string // the rule's matching domain, lowercased
	Upstream Upstream
	Config   Config // validated
}

func (t Target) key() string { return t.RuleID + "\x00" + t.Upstream.Addr }

// Event is a recorded enable/disable transition.
type Event struct {
	Time    int64  `json:"time"`
	RuleID  string `json:"rule"`
	Addr    string `json:"upstream"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

// Status describes one upstream's current health for the UI.
type Status struct {
	RuleID          string `json:"rule"`
	Addr            string `json:"upstream"`
	Enabled         bool   `json:"enabled"`         // this rule's checks consider it healthy
	ReceivesTraffic bool   `json:"receivesTraffic"` // false if any rule has it disabled
	Since           int64  `json:"since"`
	Reason          string `json:"reason"`
	Check           string `json:"check,omitempty"` // e.g. "HTTP HEAD /healthz"; empty if passive only
	LastCheck       int64  `json:"lastCheck,omitempty"`
	LastDetail      string `json:"lastDetail,omitempty"`
	LastLatencyMs   int64  `json:"lastLatencyMs,omitempty"`
	ConsecutiveOK   int    `json:"consecutiveOk"`
	ConsecutiveFail int    `json:"consecutiveFail"`
	PassiveErrors   int    `json:"passiveErrors"` // counted errors in the current window
}

type state struct {
	target     Target
	up         bool
	since      time.Time
	reason     string
	consecOK   int
	consecFail int
	lastCheck  time.Time
	lastRes    ProbeResult
	passive    []time.Time
	cancel     context.CancelFunc
	recovery   *time.Timer
	// set when the upstream is disabled; cleared after each active check is
	// reported, so a brief outage between two checks is not lost
	downSinceCheck bool
}

// Manager runs health checks for all managed upstreams.
type Manager struct {
	mu        sync.Mutex
	states    map[string]*state
	notified  map[string]bool // addr -> last value sent to the load balancer
	events    []Event
	notify    Notifier
	logf      func(string)
	prober    Prober
	now       func() time.Time
	maxEvents int

	// OnCheck, if set, is called after every active check (outside the
	// lock). offline is true if the upstream was out of rotation at any
	// point since the previous check, or is now.
	OnCheck func(t Target, res ProbeResult, offline bool)
}

// NewManager returns an idle manager. Call SetTargets to start checking.
func NewManager(notify Notifier, logf func(string)) *Manager {
	if logf == nil {
		logf = func(string) {}
	}
	return &Manager{
		states:    map[string]*state{},
		notified:  map[string]bool{},
		notify:    notify,
		logf:      logf,
		prober:    DefaultProber,
		now:       time.Now,
		maxEvents: 200,
	}
}

type pendingNotify struct {
	addr   string
	online bool
}

// SetTargets replaces the managed set. Unchanged targets keep their state
// and counters; changed ones keep their enabled/disabled state but restart
// their counters with the new settings.
func (m *Manager) SetTargets(targets []Target) {
	m.mu.Lock()
	var pending []pendingNotify
	touched := map[string]bool{}

	wanted := map[string]Target{}
	for _, t := range targets {
		wanted[t.key()] = t
	}

	for key, st := range m.states {
		if _, ok := wanted[key]; !ok {
			m.stopLocked(st)
			delete(m.states, key)
			touched[st.target.Upstream.Addr] = true
		}
	}

	for key, t := range wanted {
		touched[t.Upstream.Addr] = true
		existing, ok := m.states[key]
		if ok && reflect.DeepEqual(existing.target, t) {
			continue
		}
		st := &state{target: t, up: true, since: m.now(), reason: "health checks started"}
		if ok {
			m.stopLocked(existing)
			st.up, st.since, st.reason = existing.up, existing.since, existing.reason
		}
		m.states[key] = st
		m.startLocked(st)
	}

	for addr := range touched {
		if p, ok := m.reconcileLocked(addr); ok {
			pending = append(pending, p)
		}
	}
	m.mu.Unlock()
	m.flush(pending)
}

// Stop ends all checks. Upstreams it disabled are re-enabled.
func (m *Manager) Stop() {
	m.SetTargets(nil)
}

// Manages reports whether an upstream address is controlled by this
// manager. addr may carry an http:// or https:// prefix.
func (m *Manager) Manages(addr string) bool {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.states {
		if st.target.Upstream.Addr == addr {
			return true
		}
	}
	return false
}

func (m *Manager) startLocked(st *state) {
	cfg := st.target.Config
	if cfg.ActiveEnabled {
		ctx, cancel := context.WithCancel(context.Background())
		st.cancel = cancel
		go m.activeLoop(ctx, st)
	} else if !st.up && cfg.PassiveEnabled {
		// Disabled by passive monitoring and nothing else will bring it back
		m.scheduleRecoveryLocked(st)
	}
}

func (m *Manager) stopLocked(st *state) {
	if st.cancel != nil {
		st.cancel()
	}
	if st.recovery != nil {
		st.recovery.Stop()
	}
}

func (m *Manager) activeLoop(ctx context.Context, st *state) {
	cfg := st.target.Config
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		res := m.prober(ctx, st.target.Upstream, &cfg)
		if ctx.Err() != nil {
			return
		}
		m.recordActive(st, res)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) recordActive(st *state, res ProbeResult) {
	m.mu.Lock()
	if m.states[st.target.key()] != st { // replaced or removed meanwhile
		m.mu.Unlock()
		return
	}
	cfg := st.target.Config
	st.lastCheck, st.lastRes = m.now(), res
	var pending []pendingNotify
	if res.OK {
		st.consecOK++
		st.consecFail = 0
		if !st.up && st.consecOK >= cfg.Rise {
			pending = m.transitionLocked(st, true, fmt.Sprintf("%s passed %d time(s) in a row (%s)", cfg.Describe(), st.consecOK, res.Detail))
		}
	} else {
		st.consecFail++
		st.consecOK = 0
		if st.up && st.consecFail >= cfg.Fall {
			pending = m.transitionLocked(st, false, fmt.Sprintf("%s failed %d time(s) in a row (last: %s)", cfg.Describe(), st.consecFail, res.Detail))
		}
	}
	offline := !st.up || st.downSinceCheck
	st.downSinceCheck = false
	target, onCheck := st.target, m.OnCheck
	m.mu.Unlock()
	m.flush(pending)
	if onCheck != nil {
		onCheck(target, res, offline)
	}
}

// ErrorKind classifies a proxied request outcome for passive monitoring.
type ErrorKind int

const (
	NoError ErrorKind = iota
	ServerError
	Timeout
	ConnectionError
)

func (k ErrorKind) String() string {
	switch k {
	case ServerError:
		return "5xx response"
	case Timeout:
		return "timeout"
	case ConnectionError:
		return "connection failure"
	}
	return "none"
}

// Classify maps the proxy's (statusCode, err) for one request to an
// ErrorKind. A client hanging up is not the upstream's fault.
func Classify(statusCode int, err error) (ErrorKind, string) {
	if err == nil {
		if statusCode >= 500 && statusCode <= 599 {
			return ServerError, fmt.Sprintf("status %d %s", statusCode, http.StatusText(statusCode))
		}
		return NoError, ""
	}
	if errors.Is(err, context.Canceled) {
		return NoError, ""
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) ||
		strings.Contains(err.Error(), "timeout awaiting response headers") {
		return Timeout, "upstream timed out"
	}
	return ConnectionError, describeError(err, 0)
}

func (c *Config) counts(kind ErrorKind) bool {
	switch kind {
	case ServerError:
		return c.CountServerErrors
	case Timeout:
		return c.CountTimeouts
	case ConnectionError:
		return c.CountConnectionErrors
	}
	return false
}

// ObserveResult is called by the proxy after each request to an upstream.
func (m *Manager) ObserveResult(ruleID string, addr string, statusCode int, err error) {
	kind, detail := Classify(statusCode, err)
	if kind == NoError {
		return
	}
	key := Target{RuleID: strings.ToLower(ruleID), Upstream: Upstream{Addr: addr}}.key()

	m.mu.Lock()
	st, ok := m.states[key]
	if !ok || !st.target.Config.PassiveEnabled || !st.up || !st.target.Config.counts(kind) {
		m.mu.Unlock()
		return
	}
	cfg := st.target.Config
	now := m.now()
	cutoff := now.Add(-time.Duration(cfg.WindowSeconds) * time.Second)
	kept := st.passive[:0]
	for _, t := range st.passive {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	st.passive = append(kept, now)

	var pending []pendingNotify
	if len(st.passive) >= cfg.Threshold {
		reason := fmt.Sprintf("%d proxied request error(s) within %ds (last: %s %s)", len(st.passive), cfg.WindowSeconds, kind, detail)
		if kind != ServerError {
			reason = fmt.Sprintf("%d proxied request error(s) within %ds (last: %s)", len(st.passive), cfg.WindowSeconds, detail)
		}
		st.passive = nil
		pending = m.transitionLocked(st, false, reason)
		if !cfg.ActiveEnabled {
			m.scheduleRecoveryLocked(st)
		}
	}
	m.mu.Unlock()
	m.flush(pending)
}

func (m *Manager) scheduleRecoveryLocked(st *state) {
	if st.recovery != nil {
		st.recovery.Stop()
	}
	wait := time.Duration(st.target.Config.PassiveRecoverySeconds) * time.Second
	st.recovery = time.AfterFunc(wait, func() {
		m.mu.Lock()
		if m.states[st.target.key()] != st || st.up {
			m.mu.Unlock()
			return
		}
		pending := m.transitionLocked(st, true, fmt.Sprintf("passive recovery time of %ds elapsed (no active check configured)", st.target.Config.PassiveRecoverySeconds))
		m.mu.Unlock()
		m.flush(pending)
	})
}

// transitionLocked records and logs a change of this rule's view of the
// upstream, then reconciles what the load balancer should see.
func (m *Manager) transitionLocked(st *state, up bool, reason string) []pendingNotify {
	st.up, st.since, st.reason = up, m.now(), reason
	st.consecOK, st.consecFail = 0, 0
	if !up {
		st.downSinceCheck = true
	}
	action := "DISABLED"
	if up {
		action = "ENABLED"
	}
	m.logf(fmt.Sprintf("Upstream %s on rule %s %s: %s", st.target.Upstream.Addr, st.target.RuleID, action, reason))
	m.events = append(m.events, Event{Time: st.since.Unix(), RuleID: st.target.RuleID, Addr: st.target.Upstream.Addr, Enabled: up, Reason: reason})
	if len(m.events) > m.maxEvents {
		m.events = m.events[len(m.events)-m.maxEvents:]
	}
	if p, ok := m.reconcileLocked(st.target.Upstream.Addr); ok {
		return []pendingNotify{p}
	}
	return nil
}

// reconcileLocked computes whether an address may receive traffic: only if
// every rule that health-checks it considers it healthy. The load balancer
// tracks state per address, so one rule's verdict applies to all rules
// sharing that backend.
func (m *Manager) reconcileLocked(addr string) (pendingNotify, bool) {
	managed, online := false, true
	var downRules []string
	for _, st := range m.states {
		if st.target.Upstream.Addr != addr {
			continue
		}
		managed = true
		if !st.up {
			online = false
			downRules = append(downRules, st.target.RuleID)
		}
	}
	prev, known := m.notified[addr]
	if !managed {
		if known {
			delete(m.notified, addr)
			if !prev {
				m.logf("Upstream " + addr + " is no longer health checked; it receives traffic again")
				return pendingNotify{addr, true}, true
			}
		}
		return pendingNotify{}, false
	}
	if known && prev == online {
		return pendingNotify{}, false
	}
	m.notified[addr] = online
	if !online && len(downRules) > 0 {
		sort.Strings(downRules)
		m.logf("Upstream " + addr + " removed from load balancing (unhealthy for: " + strings.Join(downRules, ", ") + ")")
	} else if online && known {
		m.logf("Upstream " + addr + " returned to load balancing")
	}
	return pendingNotify{addr, online}, true
}

func (m *Manager) flush(pending []pendingNotify) {
	if m.notify == nil {
		return
	}
	for _, p := range pending {
		m.notify(p.addr, p.online)
	}
}

// Statuses returns the current state of every managed upstream, optionally
// limited to one rule.
func (m *Manager) Statuses(ruleID string) []Status {
	ruleID = strings.ToLower(ruleID)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := []Status{}
	for _, st := range m.states {
		if ruleID != "" && st.target.RuleID != ruleID {
			continue
		}
		cfg := st.target.Config
		s := Status{
			RuleID:          st.target.RuleID,
			Addr:            st.target.Upstream.Addr,
			Enabled:         st.up,
			ReceivesTraffic: m.receivesTrafficLocked(st.target.Upstream.Addr),
			Since:           st.since.Unix(),
			Reason:          st.reason,
			ConsecutiveOK:   st.consecOK,
			ConsecutiveFail: st.consecFail,
		}
		if cfg.ActiveEnabled {
			s.Check = cfg.Describe()
		}
		if !st.lastCheck.IsZero() {
			s.LastCheck = st.lastCheck.Unix()
			s.LastDetail = st.lastRes.Detail
			s.LastLatencyMs = st.lastRes.Latency.Milliseconds()
		}
		cutoff := now.Add(-time.Duration(cfg.WindowSeconds) * time.Second)
		for _, t := range st.passive {
			if t.After(cutoff) {
				s.PassiveErrors++
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].Addr < out[j].Addr
	})
	return out
}

func (m *Manager) receivesTrafficLocked(addr string) bool {
	online, known := m.notified[addr]
	return !known || online
}

// Events returns recent transitions, newest first, optionally for one rule.
func (m *Manager) Events(ruleID string, limit int) []Event {
	ruleID = strings.ToLower(ruleID)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Event{}
	for i := len(m.events) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if ruleID == "" || m.events[i].RuleID == ruleID {
			out = append(out, m.events[i])
		}
	}
	return out
}
