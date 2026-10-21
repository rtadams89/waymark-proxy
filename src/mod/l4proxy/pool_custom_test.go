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

func dests(addrs []string, weights ...int) []Destination {
	out := []Destination{}
	for i, a := range addrs {
		w := 1
		if i < len(weights) {
			w = weights[i]
		}
		out = append(out, Destination{Address: a, Weight: w, Enabled: true})
	}
	return out
}

func startRule(t *testing.T, m *Manager, d []Destination, mode string) (*ProxyRelayInstance, string) {
	t.Helper()
	listen := freePort(t)
	id := m.NewConfig(&ProxyRelayOptions{Name: "test", ListeningAddr: listen, ProxyAddr: d[0].Address, UseTCP: true, Timeout: 5})
	if err := m.ApplyPoolSettings(id, &PoolSettings{Destinations: d, LoadBalancing: mode}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.GetConfigByUUID(id)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	time.Sleep(200 * time.Millisecond)
	return c, listen
}

func TestL4WeightedAndBackup(t *testing.T) {
	m := newTestManager(t)
	a, stopA := nameServer(t, "A")
	defer stopA()
	b, stopB := nameServer(t, "B")
	defer stopB()
	_, listen := startRule(t, m, dests([]string{a, b}), LBWeighted)
	got := map[string]int{}
	for i := 0; i < 40; i++ {
		got[ask(t, listen)]++
	}
	if got["A"] == 0 || got["B"] == 0 || got["A"]+got["B"] != 40 {
		t.Fatalf("equal weights: %v", got)
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

	// Weight 0 is a backup: used only while A is down
	m2 := newTestManager(t)
	_, listen2 := startRule(t, m2, dests([]string{a, b}, 1, 0), LBWeighted)
	for i := 0; i < 5; i++ {
		if r := ask(t, listen2); r != "A" {
			t.Fatalf("backup used while the primary is up: %q", r)
		}
	}
	m2.SetUpstreamOnline(a, false)
	if r := ask(t, listen2); r != "B" {
		t.Fatalf("backup not used with the primary down: %q", r)
	}
}

func TestL4WeightsAndDisabled(t *testing.T) {
	c := &ProxyRelayInstance{Destinations: []Destination{
		{Address: "10.0.0.1:1", Weight: 3, Enabled: true},
		{Address: "10.0.0.2:1", Weight: 1, Enabled: true},
		{Address: "10.0.0.3:1", Weight: 5, Enabled: false},
	}, LoadBalancing: LBWeighted}
	got := map[string]int{}
	for i := 0; i < 4000; i++ {
		got[c.candidates("")[0]]++
	}
	if got["10.0.0.3:1"] != 0 {
		t.Fatalf("disabled destination picked: %v", got)
	}
	if r := float64(got["10.0.0.1:1"]) / float64(got["10.0.0.2:1"]); r < 2.4 || r > 3.8 {
		t.Fatalf("weights 3:1 gave %v", got)
	}
	if l := c.UpstreamList(); len(l) != 2 {
		t.Fatalf("enabled list: %v", l)
	}
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
	c, listen := startRule(t, m, dests([]string{dead, b}, 1, 0), LBWeighted)
	for i := 0; i < 3; i++ {
		if r := ask(t, listen); r != "B" {
			t.Fatalf("retry past a dead destination, got %q", r)
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

func TestL4StickyAndLeastConn(t *testing.T) {
	c := &ProxyRelayInstance{Destinations: dests([]string{"10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1"}), LoadBalancing: LBWeighted, StickySessions: true}
	first := c.candidates("192.0.2.7")[0]
	for i := 0; i < 20; i++ {
		if c.candidates("192.0.2.7")[0] != first {
			t.Fatal("sticky session is not kept")
		}
	}
	spread := map[string]bool{}
	for i := 0; i < 60; i++ {
		spread[c.candidates(fmt.Sprintf("192.0.2.%d", i))[0]] = true
	}
	if len(spread) < 2 {
		t.Fatalf("sticky clients use %d of 3 destinations", len(spread))
	}
	c2 := &ProxyRelayInstance{Destinations: dests([]string{"10.0.0.1:1", "10.0.0.2:1"}), LoadBalancing: LBLeastConn}
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
	// Weighted least connections: weight 2 takes two connections per one
	c3 := &ProxyRelayInstance{Destinations: dests([]string{"10.0.0.1:1", "10.0.0.2:1"}, 2, 1), LoadBalancing: LBLeastConn}
	count := map[string]int{}
	for i := 0; i < 6; i++ {
		a := c3.candidates("")[0]
		count[a]++
		c3.track(a, nopCloser{})
	}
	if count["10.0.0.1:1"] != 4 || count["10.0.0.2:1"] != 2 {
		t.Fatalf("weighted least connections: %v", count)
	}
}

func TestL4AddressListToDestinations(t *testing.T) {
	plain := &ProxyRelayInstance{Upstreams: []string{"10.0.0.1:1", "10.0.0.2:1"}}
	if !plain.normalizePool() {
		t.Fatal("not converted")
	}
	if plain.LoadBalancing != LBWeighted || plain.Destinations[0].Weight != 1 || plain.Destinations[1].Weight != 1 {
		t.Fatalf("address list: %+v", plain)
	}
	lc := &ProxyRelayInstance{ProxyTargetAddr: "10.0.0.1:1", LoadBalancing: LBLeastConn}
	lc.normalizePool()
	if lc.LoadBalancing != LBLeastConn || len(lc.Destinations) != 1 || !lc.Destinations[0].Enabled {
		t.Fatalf("least connections: %+v", lc)
	}
	if lc.normalizePool() {
		t.Fatal("converted twice")
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func TestL4CloseOnUpstreamDown(t *testing.T) {
	m := newTestManager(t)
	a, stopA := nameServer(t, "A")
	defer stopA()
	c, listen := startRule(t, m, dests([]string{a}), LBWeighted)
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
	one := func(addr string) []Destination { return []Destination{{Address: addr, Weight: 1, Enabled: true}} }
	bad := []PoolSettings{
		{Destinations: nil},
		{Destinations: one("10.0.0.1")},
		{Destinations: one("10.0.0.1:0")},
		{Destinations: one("bad host:80")},
		{Destinations: append(one("10.0.0.1:80"), one("10.0.0.1:80")...)},
		{Destinations: []Destination{{Address: "10.0.0.1:80", Weight: 101, Enabled: true}}},
		{Destinations: []Destination{{Address: "10.0.0.1:80", Weight: -1, Enabled: true}}},
		{Destinations: one("10.0.0.1:80"), LoadBalancing: "roundrobin"},
		{Destinations: one("10.0.0.1:80"), HealthCheck: &healthcheck.Config{ActiveEnabled: true, Type: healthcheck.CheckHTTPGet, IntervalSeconds: 10, TimeoutSeconds: 3, Rise: 1, Fall: 1, Threshold: 1, WindowSeconds: 1, PassiveRecoverySeconds: 5}},
		{Destinations: one("10.0.0.1:80"), HealthCheck: &healthcheck.Config{ActiveEnabled: true, Type: healthcheck.CheckUDP, IntervalSeconds: 10, TimeoutSeconds: 3, Rise: 1, Fall: 1, Threshold: 1, WindowSeconds: 1, PassiveRecoverySeconds: 5}},
	}
	for i, p := range bad {
		if err := p.Validate(true, false); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	ok := PoolSettings{Destinations: []Destination{{Address: " 10.0.0.1:53 ", Weight: 2, Enabled: true}, {Address: "dns.example.com:53", Weight: 0}}, HealthCheck: &healthcheck.Config{ActiveEnabled: true, Type: healthcheck.CheckUDP, Send: `\x00`, IntervalSeconds: 10, TimeoutSeconds: 3, Rise: 1, Fall: 1, Threshold: 1, WindowSeconds: 1, PassiveRecoverySeconds: 5}}
	if err := ok.Validate(false, true); err != nil {
		t.Fatal(err)
	}
	if ok.Destinations[0].Address != "10.0.0.1:53" || ok.LoadBalancing != LBWeighted {
		t.Fatalf("normalised: %+v", ok)
	}
	// Older clients send a plain address list
	legacy := PoolSettings{Upstreams: []string{"10.0.0.1:53", "10.0.0.2:53"}}
	if err := legacy.Validate(true, true); err != nil || len(legacy.Destinations) != 2 || legacy.Destinations[1].Weight != 1 {
		t.Fatalf("address list: %v %+v", err, legacy)
	}
	// Checks turned off entirely: stored as no health check
	off := PoolSettings{Destinations: one("10.0.0.1:53"), HealthCheck: healthcheck.DefaultConfig()}
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
	if err := m.ApplyPoolSettings(id, &PoolSettings{Destinations: dests([]string{a, b}, 1, 0)}); err != nil {
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
		t.Fatalf("primary: %q", r)
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
