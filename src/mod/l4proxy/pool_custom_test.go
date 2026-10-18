package l4proxy

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/info/logger"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	l, _ := logger.NewFmtLogger()
	m, err := NewL4Proxy(&Options{ConfigStore: t.TempDir(), Logger: l, DefaultTimeout: 5})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// nameServer answers each TCP line with its own name.
func nameServer(t *testing.T, name string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					fmt.Fprintf(c, "%s\n", name)
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func freePort(t *testing.T) string {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func ask(t *testing.T, addr string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "dial error: " + err.Error()
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(c, "hi\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "closed"
	}
	return strings.TrimSpace(line)
}

func startRule(t *testing.T, m *Manager, ups []string, mode string) (*ProxyRelayInstance, string) {
	t.Helper()
	listen := freePort(t)
	id := m.NewConfig(&ProxyRelayOptions{Name: "test", ListeningAddr: listen, ProxyAddr: ups[0], UseTCP: true, Timeout: 5})
	if err := m.ApplyPoolSettings(id, &PoolSettings{Upstreams: ups, LoadBalancing: mode}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.GetConfigByUUID(id)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	// Wait for the listener without connecting (a connection would take a
	// load balancing turn)
	time.Sleep(200 * time.Millisecond)
	return c, listen
}

func TestL4RoundRobinAndFailover(t *testing.T) {
	m := newTestManager(t)
	a, stopA := nameServer(t, "A")
	defer stopA()
	b, stopB := nameServer(t, "B")
	defer stopB()
	_, listen := startRule(t, m, []string{a, b}, LBRoundRobin)
	got := map[string]int{}
	for i := 0; i < 6; i++ {
		got[ask(t, listen)]++
	}
	if got["A"] != 3 || got["B"] != 3 {
		t.Fatalf("round robin: %v", got)
	}
	// Health checks take A out: everything goes to B
	m.SetUpstreamOnline(a, false)
	for i := 0; i < 3; i++ {
		if r := ask(t, listen); r != "B" {
			t.Fatalf("A is down, got %q", r)
		}
	}
	// Both down: the client connection is closed at once
	m.SetUpstreamOnline(b, false)
	if r := ask(t, listen); r != "closed" {
		t.Fatalf("all down, got %q", r)
	}
	m.SetUpstreamOnline(a, true)
	m.SetUpstreamOnline(b, true)
}

func TestL4RetryNextUpstream(t *testing.T) {
	m := newTestManager(t)
	dead := freePort(t) // nothing listens here
	b, stopB := nameServer(t, "B")
	defer stopB()
	var observed []string
	var mu sync.Mutex
	m.Options.ObserveConnect = func(rule, addr string, err error) {
		mu.Lock()
		observed = append(observed, addr)
		mu.Unlock()
	}
	c, listen := startRule(t, m, []string{dead, b}, LBFailover)
	for i := 0; i < 3; i++ {
		if r := ask(t, listen); r != "B" {
			t.Fatalf("failover past a dead upstream, got %q", r)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 3 || observed[0] != dead {
		t.Fatalf("connection failures not reported to passive monitoring: %v", observed)
	}
	for _, u := range c.UpstreamInfos() {
		if u.Address == dead && u.Failed != 3 {
			t.Fatalf("failed count: %+v", u)
		}
	}
}

func TestL4SourceHashAndLeastConn(t *testing.T) {
	c := &ProxyRelayInstance{Upstreams: []string{"10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1"}, LoadBalancing: LBSourceHash}
	first := c.candidates("192.0.2.7")[0]
	for i := 0; i < 10; i++ {
		if c.candidates("192.0.2.7")[0] != first {
			t.Fatal("source hash is not stable")
		}
	}
	spread := map[string]bool{}
	for i := 0; i < 50; i++ {
		spread[c.candidates(fmt.Sprintf("192.0.2.%d", i))[0]] = true
	}
	if len(spread) != 3 {
		t.Fatalf("source hash uses %d of 3 upstreams", len(spread))
	}
	c2 := &ProxyRelayInstance{Upstreams: []string{"10.0.0.1:1", "10.0.0.2:1"}, LoadBalancing: LBLeastConn}
	r1 := c2.track("10.0.0.1:1", nopCloser{})
	r2 := c2.track("10.0.0.1:1", nopCloser{})
	for i := 0; i < 4; i++ {
		if got := c2.candidates("x")[0]; got != "10.0.0.2:1" {
			t.Fatalf("least connections picked %s", got)
		}
	}
	r1()
	r2()
	r1() // releasing twice is harmless
	if a := c2.getPool().get("10.0.0.1:1").active.Load(); a != 0 {
		t.Fatalf("active count %d after release", a)
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func TestL4CloseOnUpstreamDown(t *testing.T) {
	m := newTestManager(t)
	a, stopA := nameServer(t, "A")
	defer stopA()
	c, listen := startRule(t, m, []string{a}, LBRoundRobin)
	c.CloseOnUpstreamDown = true
	conn, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "hi\n")
	rd := bufio.NewReader(conn)
	if l, _ := rd.ReadString('\n'); strings.TrimSpace(l) != "A" {
		t.Fatal("no answer")
	}
	m.SetUpstreamOnline(a, false)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := rd.ReadString('\n'); err == nil {
		t.Fatal("connection stayed open after its upstream went down")
	} else if errors.Is(err, errors.New("timeout")) {
		t.Fatal(err)
	}
	m.SetUpstreamOnline(a, true)
}

func TestPoolSettingsValidate(t *testing.T) {
	bad := []PoolSettings{
		{Upstreams: nil},
		{Upstreams: []string{"10.0.0.1"}},
		{Upstreams: []string{"10.0.0.1:0"}},
		{Upstreams: []string{"bad host:80"}},
		{Upstreams: []string{"10.0.0.1:80", "10.0.0.1:80"}},
		{Upstreams: []string{"10.0.0.1:80"}, LoadBalancing: "random"},
		{Upstreams: []string{"10.0.0.1:80"}, HealthCheck: &healthcheck.Config{ActiveEnabled: true, Type: healthcheck.CheckHTTPGet, IntervalSeconds: 10, TimeoutSeconds: 3, Rise: 1, Fall: 1, Threshold: 1, WindowSeconds: 1, PassiveRecoverySeconds: 5}},
		{Upstreams: []string{"10.0.0.1:80"}, HealthCheck: &healthcheck.Config{ActiveEnabled: true, Type: healthcheck.CheckUDP, IntervalSeconds: 10, TimeoutSeconds: 3, Rise: 1, Fall: 1, Threshold: 1, WindowSeconds: 1, PassiveRecoverySeconds: 5}},
	}
	for i, p := range bad {
		if err := p.Validate(true, false); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	ok := PoolSettings{Upstreams: []string{" 10.0.0.1:53 ", "dns.example.com:53"}, HealthCheck: &healthcheck.Config{ActiveEnabled: true, Type: healthcheck.CheckUDP, Send: `\x00`, IntervalSeconds: 10, TimeoutSeconds: 3, Rise: 1, Fall: 1, Threshold: 1, WindowSeconds: 1, PassiveRecoverySeconds: 5}}
	if err := ok.Validate(false, true); err != nil {
		t.Fatal(err)
	}
	if ok.Upstreams[0] != "10.0.0.1:53" || ok.LoadBalancing != LBRoundRobin {
		t.Fatalf("normalised: %+v", ok)
	}
	// Checks turned off entirely: stored as no health check
	off := PoolSettings{Upstreams: []string{"10.0.0.1:53"}, HealthCheck: healthcheck.DefaultConfig()}
	off.HealthCheck.Type = healthcheck.CheckICMP
	if err := off.Validate(true, true); err != nil || off.HealthCheck != nil {
		t.Fatalf("disabled checks: %v %+v", err, off.HealthCheck)
	}
}

func TestL4UDPPool(t *testing.T) {
	m := newTestManager(t)
	echo := func(name string) (string, func()) {
		pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
		go func() {
			buf := make([]byte, 1500)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				pc.WriteTo(append([]byte(name+":"), buf[:n]...), from)
			}
		}()
		return pc.LocalAddr().String(), func() { pc.Close() }
	}
	a, stopA := echo("A")
	defer stopA()
	b, stopB := echo("B")
	defer stopB()
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	listen := pc.LocalAddr().String()
	pc.Close()
	id := m.NewConfig(&ProxyRelayOptions{Name: "udp", ListeningAddr: listen, ProxyAddr: a, UseUDP: true, Timeout: 5})
	if err := m.ApplyPoolSettings(id, &PoolSettings{Upstreams: []string{a, b}, LoadBalancing: LBFailover}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.GetConfigByUUID(id)
	c.Start()
	defer c.Stop()
	time.Sleep(200 * time.Millisecond)
	send := func() string {
		conn, _ := net.Dial("udp", listen)
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		conn.Write([]byte("ping"))
		buf := make([]byte, 100)
		n, err := conn.Read(buf)
		if err != nil {
			return "no reply"
		}
		return string(buf[:n])
	}
	if r := send(); r != "A:ping" {
		t.Fatalf("failover primary: %q", r)
	}
	m.SetUpstreamOnline(a, false)
	if r := send(); r != "B:ping" {
		t.Fatalf("new session after the primary went down: %q", r)
	}
	m.SetUpstreamOnline(b, false)
	if r := send(); r != "no reply" {
		t.Fatalf("all down: %q", r)
	}
}
