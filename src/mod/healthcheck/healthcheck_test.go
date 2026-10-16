package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

/* ---------- helpers ---------- */

type recorder struct {
	mu      sync.Mutex
	calls   []string
	logs    []string
	current map[string]bool
}

func newRecorder() *recorder { return &recorder{current: map[string]bool{}} }

func (r *recorder) notify(addr string, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("%s=%v", addr, online))
	r.current[addr] = online
}

func (r *recorder) log(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, s)
}

func (r *recorder) online(addr string) (bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.current[addr]
	return v, ok
}

func (r *recorder) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// blockingProber never returns, so tests drive results by hand.
func blockingProber(ctx context.Context, _ Upstream, _ *Config) ProbeResult {
	<-ctx.Done()
	return ProbeResult{}
}

func newTestManager() (*Manager, *recorder, *fakeClock) {
	rec := newRecorder()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := NewManager(rec.notify, rec.log)
	m.prober = blockingProber
	m.now = clk.now
	return m, rec, clk
}

func activeCfg() Config {
	c := DefaultConfig()
	c.ActiveEnabled = true
	c.Rise, c.Fall = 2, 3
	return *c
}

func passiveCfg() Config {
	c := DefaultConfig()
	c.PassiveEnabled = true
	c.Threshold, c.WindowSeconds = 3, 10
	return *c
}

func stateOf(m *Manager, rule, addr string) *state {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[Target{RuleID: rule, Upstream: Upstream{Addr: addr}}.key()]
}

/* ---------- config ---------- */

func TestValidate(t *testing.T) {
	ok := DefaultConfig()
	ok.ActiveEnabled = true
	ok.Path = "healthz"
	n, err := ok.Validate()
	if err != nil || n.Path != "/healthz" {
		t.Fatalf("valid config rejected or path not normalized: %v %q", err, n.Path)
	}
	bad := []func(c *Config){
		func(c *Config) { c.Type = "smtp" },
		func(c *Config) { c.TimeoutSeconds = 10; c.IntervalSeconds = 10 },
		func(c *Config) { c.IntervalSeconds = 1 },
		func(c *Config) { c.ExpectedStatus = "200-abc" },
		func(c *Config) { c.ExpectedStatus = "600" },
		func(c *Config) { c.Path = "/a b" },
		func(c *Config) { c.HostHeader = "evil\r\nX-Injected: 1" },
		func(c *Config) { c.Rise = 0 },
		func(c *Config) { c.Fall = 21 },
		func(c *Config) {
			c.PassiveEnabled = true
			c.CountServerErrors, c.CountTimeouts, c.CountConnectionErrors = false, false, false
		},
		func(c *Config) { c.PassiveRecoverySeconds = 1 },
	}
	for i, mutate := range bad {
		c := DefaultConfig()
		mutate(c)
		if _, err := c.Validate(); err == nil {
			t.Errorf("case %d: invalid config accepted: %+v", i, c)
		}
	}
}

func TestParseStatusRanges(t *testing.T) {
	r, err := ParseStatusRanges("200, 204,301-302")
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[int]bool{200: true, 204: true, 301: true, 302: true, 201: false, 303: false, 500: false} {
		if got := statusMatches(code, r); got != want {
			t.Errorf("%d: got %v want %v", code, got, want)
		}
	}
	if _, err := ParseStatusRanges("300-200"); err == nil {
		t.Error("reversed range accepted")
	}
}

/* ---------- classification ---------- */

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	cases := []struct {
		code int
		err  error
		want ErrorKind
	}{
		{200, nil, NoError},
		{404, nil, NoError},
		{502, nil, ServerError},
		{503, nil, ServerError},
		{0, context.Canceled, NoError}, // client went away
		{0, timeoutErr{}, Timeout},
		{0, fmt.Errorf("wrap: %w", context.DeadlineExceeded), Timeout},
		{0, errors.New("net/http: timeout awaiting response headers"), Timeout},
		{0, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, ConnectionError},
		{0, errors.New("EOF"), ConnectionError},
	}
	for _, c := range cases {
		if got, _ := Classify(c.code, c.err); got != c.want {
			t.Errorf("Classify(%d, %v) = %v, want %v", c.code, c.err, got, c.want)
		}
	}
}

/* ---------- active state machine ---------- */

func TestActiveRiseFall(t *testing.T) {
	m, rec, _ := newTestManager()
	defer m.Stop()
	m.SetTargets([]Target{{RuleID: "app.example.com", Upstream: Upstream{Addr: "10.0.0.5:8080"}, Config: activeCfg()}})
	if on, ok := rec.online("10.0.0.5:8080"); !ok || !on {
		t.Fatalf("new target should be pushed online, got %v %v", on, ok)
	}
	st := stateOf(m, "app.example.com", "10.0.0.5:8080")

	fail := ProbeResult{Detail: "status 503 (expected 200-399)"}
	pass := ProbeResult{OK: true, Detail: "status 200"}

	m.recordActive(st, fail)
	m.recordActive(st, fail)
	if on, _ := rec.online("10.0.0.5:8080"); !on {
		t.Fatal("disabled before reaching fall threshold")
	}
	m.recordActive(st, pass) // resets the failure streak
	m.recordActive(st, fail)
	m.recordActive(st, fail)
	if on, _ := rec.online("10.0.0.5:8080"); !on {
		t.Fatal("a success in between should reset the failure count")
	}
	m.recordActive(st, fail)
	if on, _ := rec.online("10.0.0.5:8080"); on {
		t.Fatal("should be disabled after 3 consecutive failures")
	}
	logs := rec.logText()
	if !strings.Contains(logs, "Upstream 10.0.0.5:8080 on rule app.example.com DISABLED: HTTP GET / failed 3 time(s) in a row (last: status 503") {
		t.Errorf("disable reason not logged clearly:\n%s", logs)
	}

	m.recordActive(st, pass)
	if on, _ := rec.online("10.0.0.5:8080"); on {
		t.Fatal("re-enabled before reaching rise threshold")
	}
	m.recordActive(st, pass)
	if on, _ := rec.online("10.0.0.5:8080"); !on {
		t.Fatal("should be re-enabled after 2 consecutive successes")
	}
	if !strings.Contains(rec.logText(), "ENABLED: HTTP GET / passed 2 time(s) in a row (status 200)") {
		t.Errorf("enable reason not logged:\n%s", rec.logText())
	}
	if ev := m.Events("APP.example.com", 0); len(ev) != 2 || ev[0].Enabled != true || ev[1].Enabled != false {
		t.Errorf("events not recorded newest-first: %+v", ev)
	}
}

/* ---------- passive ---------- */

func TestPassiveThresholdWindowAndRecovery(t *testing.T) {
	m, rec, clk := newTestManager()
	defer m.Stop()
	cfg := passiveCfg()
	cfg.CountServerErrors = false  // only timeouts and connection errors
	cfg.PassiveRecoverySeconds = 0 // fire immediately in tests
	m.SetTargets([]Target{{RuleID: "app", Upstream: Upstream{Addr: "b:80"}, Config: cfg}})

	m.ObserveResult("app", "b:80", 503, nil) // not counted
	m.ObserveResult("app", "b:80", 503, nil)
	m.ObserveResult("app", "b:80", 503, nil)
	if on, _ := rec.online("b:80"); !on {
		t.Fatal("5xx should not count when CountServerErrors is off")
	}

	m.ObserveResult("app", "b:80", 0, timeoutErr{})
	m.ObserveResult("app", "b:80", 0, timeoutErr{})
	clk.advance(11 * time.Second) // both fall out of the 10s window
	m.ObserveResult("app", "b:80", 0, timeoutErr{})
	m.ObserveResult("app", "b:80", 0, errors.New("EOF"))
	if on, _ := rec.online("b:80"); !on {
		t.Fatal("errors outside the window must not count")
	}
	m.ObserveResult("app", "b:80", 0, context.Canceled) // client hang-up, ignored
	m.ObserveResult("APP", "b:80", 0, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	if on, _ := rec.online("b:80"); on {
		t.Fatal("3 counted errors within the window should disable")
	}
	if !strings.Contains(rec.logText(), "DISABLED: 3 proxied request error(s) within 10s (last: connection refused)") {
		t.Errorf("passive reason not logged:\n%s", rec.logText())
	}

	// No active check configured: recovery timer re-enables
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if on, _ := rec.online("b:80"); on {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if on, _ := rec.online("b:80"); !on {
		t.Fatal("passive-only upstream should be re-enabled after the recovery time")
	}
	if !strings.Contains(rec.logText(), "passive recovery time") {
		t.Errorf("recovery not logged:\n%s", rec.logText())
	}
}

func TestPassiveWithActiveNeedsRise(t *testing.T) {
	m, rec, _ := newTestManager()
	defer m.Stop()
	cfg := activeCfg()
	cfg.PassiveEnabled = true
	cfg.Threshold = 2
	m.SetTargets([]Target{{RuleID: "app", Upstream: Upstream{Addr: "b:80"}, Config: cfg}})
	st := stateOf(m, "app", "b:80")
	ok := ProbeResult{OK: true, Detail: "status 200"}
	m.recordActive(st, ok)
	m.recordActive(st, ok) // healthy streak before the passive trip
	m.ObserveResult("app", "b:80", 500, nil)
	m.ObserveResult("app", "b:80", 500, nil)
	if on, _ := rec.online("b:80"); on {
		t.Fatal("passive threshold should disable")
	}
	m.recordActive(st, ok)
	if on, _ := rec.online("b:80"); on {
		t.Fatal("old success streak must not count; needs a fresh rise")
	}
	m.recordActive(st, ok)
	if on, _ := rec.online("b:80"); !on {
		t.Fatal("rise successes after the passive trip should re-enable")
	}
}

/* ---------- multiple rules, removal ---------- */

func TestSharedBackendAndRemoval(t *testing.T) {
	m, rec, _ := newTestManager()
	defer m.Stop()
	a := Target{RuleID: "a.example.com", Upstream: Upstream{Addr: "shared:80"}, Config: activeCfg()}
	b := Target{RuleID: "b.example.com", Upstream: Upstream{Addr: "shared:80"}, Config: activeCfg()}
	b.Config.Path = "/other"
	m.SetTargets([]Target{a, b})
	fail := ProbeResult{Detail: "connection refused"}
	pass := ProbeResult{OK: true, Detail: "status 200"}
	sa, sb := stateOf(m, "a.example.com", "shared:80"), stateOf(m, "b.example.com", "shared:80")
	for i := 0; i < 3; i++ {
		m.recordActive(sa, fail)
	}
	if on, _ := rec.online("shared:80"); on {
		t.Fatal("one rule failing should take the shared backend out")
	}
	if !strings.Contains(rec.logText(), "removed from load balancing (unhealthy for: a.example.com)") {
		t.Errorf("load balancer change not logged:\n%s", rec.logText())
	}
	m.recordActive(sb, pass)
	m.recordActive(sb, pass)
	if on, _ := rec.online("shared:80"); on {
		t.Fatal("must stay out while any rule reports it unhealthy")
	}

	// Disabling health checks on the failing rule hands the backend back
	m.SetTargets([]Target{b})
	if on, _ := rec.online("shared:80"); !on {
		t.Fatal("removing the failing rule's checks should restore traffic")
	}
	m.SetTargets(nil)
	if m.Manages("http://shared:80") {
		t.Error("should no longer manage the address")
	}
}

func TestConfigChangeKeepsDownState(t *testing.T) {
	m, rec, _ := newTestManager()
	defer m.Stop()
	tg := Target{RuleID: "app", Upstream: Upstream{Addr: "b:80"}, Config: activeCfg()}
	m.SetTargets([]Target{tg})
	st := stateOf(m, "app", "b:80")
	for i := 0; i < 3; i++ {
		m.recordActive(st, ProbeResult{Detail: "x"})
	}
	tg.Config.Path = "/healthz"
	m.SetTargets([]Target{tg})
	if on, _ := rec.online("b:80"); on {
		t.Fatal("editing settings must not silently re-enable a failed upstream")
	}
	if st2 := stateOf(m, "app", "b:80"); st2 == st || st2.consecFail != 0 {
		t.Error("changed config should get a fresh state with reset counters")
	}
}

func TestStatuses(t *testing.T) {
	m, _, _ := newTestManager()
	defer m.Stop()
	m.SetTargets([]Target{
		{RuleID: "b", Upstream: Upstream{Addr: "y:80"}, Config: passiveCfg()},
		{RuleID: "a", Upstream: Upstream{Addr: "x:80"}, Config: activeCfg()},
	})
	s := m.Statuses("")
	if len(s) != 2 || s[0].RuleID != "a" || s[0].Check != "HTTP GET /" || s[1].Check != "" || !s[0].ReceivesTraffic {
		t.Errorf("unexpected statuses: %+v", s)
	}
	if len(m.Statuses("A")) != 1 {
		t.Error("rule filter should be case-insensitive")
	}
}

/* ---------- real probes ---------- */

func TestProbeHTTP(t *testing.T) {
	var gotHost, gotMethod, gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost.Store(r.Host)
		gotMethod.Store(r.Method)
		gotPath.Store(r.URL.RequestURI())
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(200)
		case "/redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "/slow":
			time.Sleep(1500 * time.Millisecond)
		default:
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	up := Upstream{Addr: addr}
	cfg := DefaultConfig()
	cfg.TimeoutSeconds = 1

	cfg.Path, cfg.HostHeader = "/ok?probe=1", "app.internal"
	if r := DefaultProber(context.Background(), up, cfg); !r.OK || r.Detail != "status 200" {
		t.Errorf("GET /ok: %+v", r)
	}
	if gotHost.Load() != "app.internal" || gotPath.Load() != "/ok?probe=1" || gotMethod.Load() != "GET" {
		t.Errorf("request not built as configured: host=%v path=%v method=%v", gotHost.Load(), gotPath.Load(), gotMethod.Load())
	}

	cfg.Type, cfg.HostHeader = CheckHTTPHead, ""
	if r := DefaultProber(context.Background(), up, cfg); !r.OK || gotMethod.Load() != "HEAD" {
		t.Errorf("HEAD /ok: %+v method=%v", r, gotMethod.Load())
	}

	cfg.Type, cfg.Path = CheckHTTPGet, "/redirect"
	if r := DefaultProber(context.Background(), up, cfg); !r.OK || r.Detail != "status 302" {
		t.Errorf("redirect should be judged, not followed: %+v", r)
	}
	cfg.ExpectedStatus = "200"
	if r := DefaultProber(context.Background(), up, cfg); r.OK {
		t.Errorf("302 should fail when only 200 is expected: %+v", r)
	}

	cfg.ExpectedStatus, cfg.Path = "200-399", "/down"
	if r := DefaultProber(context.Background(), up, cfg); r.OK || !strings.Contains(r.Detail, "status 503") {
		t.Errorf("503 should fail: %+v", r)
	}

	cfg.Path = "/slow"
	if r := DefaultProber(context.Background(), up, cfg); r.OK || r.Detail != "timed out after 1s" {
		t.Errorf("slow upstream should time out: %+v", r)
	}
}

func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	cfg := DefaultConfig()
	cfg.Type, cfg.TimeoutSeconds = CheckTCP, 1
	if r := DefaultProber(context.Background(), Upstream{Addr: addr}, cfg); !r.OK {
		t.Errorf("open port: %+v", r)
	}
	ln.Close()
	if r := DefaultProber(context.Background(), Upstream{Addr: addr}, cfg); r.OK || r.Detail != "connection refused" {
		t.Errorf("closed port: %+v", r)
	}
}

func TestProbeICMP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Type, cfg.TimeoutSeconds = CheckICMP, 2
	r := DefaultProber(context.Background(), Upstream{Addr: "127.0.0.1:8080"}, cfg)
	if !r.OK && strings.Contains(r.Detail, "not permitted") {
		t.Skip("ICMP sockets not permitted in this environment: " + r.Detail)
	}
	if !r.OK || !strings.Contains(r.Detail, "reply from 127.0.0.1") {
		t.Errorf("ping 127.0.0.1: %+v", r)
	}
}

func TestWithDefaultPort(t *testing.T) {
	for in, want := range map[string]string{"10.0.0.1": "10.0.0.1:80", "10.0.0.1:81": "10.0.0.1:81", "host": "host:80", "[::1]": "[::1]:80"} {
		if got := withDefaultPort(in, false); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	if withDefaultPort("h", true) != "h:443" {
		t.Error("TLS default port should be 443")
	}
}

/* ---------- full loop with real probes ---------- */

func TestActiveLoopEndToEnd(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	rec := newRecorder()
	m := NewManager(rec.notify, rec.log)
	defer m.Stop()
	cfg := activeCfg()
	cfg.IntervalSeconds, cfg.TimeoutSeconds, cfg.Rise, cfg.Fall = 1, 1, 1, 2 // faster than the UI allows, for the test
	m.SetTargets([]Target{{RuleID: "app", Upstream: Upstream{Addr: addr}, Config: cfg}})

	waitFor := func(want bool, within time.Duration) bool {
		end := time.Now().Add(within)
		for time.Now().Before(end) {
			if on, _ := rec.online(addr); on == want {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	time.Sleep(200 * time.Millisecond)
	healthy.Store(false)
	if !waitFor(false, 4*time.Second) {
		t.Fatalf("not disabled after the upstream started failing\n%s", rec.logText())
	}
	healthy.Store(true)
	if !waitFor(true, 3*time.Second) {
		t.Fatalf("not re-enabled after recovery\n%s", rec.logText())
	}
	s := m.Statuses("app")
	if len(s) != 1 || s[0].LastDetail != "status 200" || s[0].LastCheck == 0 {
		t.Errorf("status not updated by real checks: %+v", s)
	}
}

/* ---------- OnCheck (feeds the Uptime Monitor) ---------- */

func TestOnCheckReportsOutagesBetweenChecks(t *testing.T) {
	m, _, _ := newTestManager()
	defer m.Stop()
	type call struct {
		ok, offline bool
		code        int
	}
	var calls []call
	m.OnCheck = func(_ Target, res ProbeResult, offline bool) {
		calls = append(calls, call{res.OK, offline, res.StatusCode})
	}

	cfg := activeCfg()
	cfg.Rise, cfg.Fall = 1, 1
	cfg.PassiveEnabled, cfg.Threshold = true, 1
	m.SetTargets([]Target{{RuleID: "app", Upstream: Upstream{Addr: "b:80"}, Config: cfg}})
	st := stateOf(m, "app", "b:80")
	ok := ProbeResult{OK: true, StatusCode: 200}

	m.recordActive(st, ok)
	m.ObserveResult("app", "b:80", 502, nil) // passive disables between checks
	m.recordActive(st, ok)                   // rise=1: re-enabled by this check
	m.recordActive(st, ok)
	m.recordActive(st, ProbeResult{StatusCode: 503}) // fall=1: disabled by this check

	want := []call{{true, false, 200}, {true, true, 200}, {true, false, 200}, {false, true, 503}}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}
