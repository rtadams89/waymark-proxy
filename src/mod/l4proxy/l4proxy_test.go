package l4proxy

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Lifecycle tests on loopback.

func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return l.Addr().String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}

func echoThrough(t *testing.T, addr string) {
	t.Helper()
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		if c, err = net.DialTimeout("tcp", addr, time.Second); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo through %s: %q %v", addr, buf, err)
	}
}

func TestStartEditStop(t *testing.T) {
	m := newTestManager(t)
	upstream := echoServer(t)
	listen := freeAddr(t)
	id := m.NewConfig(&ProxyRelayOptions{Name: "t", ListeningAddr: listen, ProxyAddr: upstream, Timeout: 5, UseTCP: true})
	c, err := m.GetConfigByUUID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !c.IsRunning() || !c.View().Running {
		t.Fatal("rule should be running")
	}
	echoThrough(t, listen)

	// Editing a running rule restarts it promptly
	listen2 := freeAddr(t)
	start := time.Now()
	if err := m.EditConfig(&ProxyRuleUpdateConfig{InstanceUUID: id, NewListeningAddr: listen2, UseTCP: true, NewTimeout: -1}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("edit took %v", d)
	}
	echoThrough(t, listen2)
	if _, err := net.DialTimeout("tcp", listen, 200*time.Millisecond); err == nil {
		t.Fatal("old listen address still accepts connections")
	}

	c.Stop()
	if c.IsRunning() || c.View().Running {
		t.Fatal("rule should be stopped")
	}
	if _, err := net.DialTimeout("tcp", listen2, 200*time.Millisecond); err == nil {
		t.Fatal("listener still open after stop")
	}

	// The saved file has the API field names and the stopped state
	b, err := os.ReadFile(filepath.Join(m.Options.ConfigStore, id+".config"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["ListeningAddress"] != listen2 || saved["Running"] != false || saved["UUID"] != id {
		t.Fatalf("saved config: %s", b)
	}

	if err := m.RemoveConfig(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Options.ConfigStore, id+".config")); !os.IsNotExist(err) {
		t.Fatal("config file not removed")
	}
	if len(m.List()) != 0 {
		t.Fatal("config not removed from list")
	}
}

func TestListenerFailureStopsRule(t *testing.T) {
	m := newTestManager(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	id := m.NewConfig(&ProxyRelayOptions{Name: "busy", ListeningAddr: busy.Addr().String(), ProxyAddr: "127.0.0.1:1", Timeout: 5, UseTCP: true})
	c, _ := m.GetConfigByUUID(id)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for c.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.IsRunning() || c.View().Running {
		t.Fatal("rule still running after its listener failed")
	}
	// Stop must not block on a listener that never started
	done := make(chan struct{})
	go func() { c.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked")
	}
}

func TestConcurrentChangesAndReads(t *testing.T) {
	m := newTestManager(t)
	upstream := echoServer(t)
	listen := freeAddr(t)
	id := m.NewConfig(&ProxyRelayOptions{Name: "r", ListeningAddr: listen, ProxyAddr: upstream, Timeout: 5, UseTCP: true})
	c, _ := m.GetConfigByUUID(id)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, r := range m.List() {
					if _, err := json.Marshal(r); err != nil {
						t.Error(err)
					}
					_ = r.View().HealthCheck.Enabled()
					_ = r.UpstreamInfos()
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		ups := []string{upstream}
		if i%2 == 0 {
			ups = append(ups, "127.0.0.1:9")
		}
		if err := m.ApplyPoolSettings(id, &PoolSettings{Upstreams: ups, LoadBalancing: LBLeastConn}); err != nil {
			t.Fatal(err)
		}
		other := m.NewConfig(&ProxyRelayOptions{Name: "x", ListeningAddr: freeAddr(t), ProxyAddr: upstream, UseUDP: true})
		if err := m.RemoveConfig(other); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	echoThrough(t, listen)
	files, _ := filepath.Glob(filepath.Join(m.Options.ConfigStore, "*"))
	for _, f := range files {
		if strings.Contains(filepath.Base(f), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", f)
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected one config file, found %v", files)
	}
}

func TestValidateListener(t *testing.T) {
	m := newTestManager(t)
	m.NewConfig(&ProxyRelayOptions{Name: "a", ListeningAddr: "127.0.0.1:7300", ProxyAddr: "127.0.0.1:1", UseTCP: true})
	m.NewConfig(&ProxyRelayOptions{Name: "w", ListeningAddr: ":7400", ProxyAddr: "127.0.0.1:1", UseUDP: true})
	cases := []struct {
		listen   string
		tcp, udp bool
		ok       bool
	}{
		{"127.0.0.1:7301", true, false, true},
		{"7301", true, true, true},
		{"127.0.0.1:7300", false, true, true},  // same address, other protocol
		{"127.0.0.1:7300", true, false, false}, // taken
		{"0.0.0.0:7300", true, false, false},   // wildcard overlaps
		{"10.0.0.1:7400", false, true, false},  // other rule listens on all addresses
		{"127.0.0.1:7302", false, false, false},
		{"127.0.0.1:99999", true, false, false},
		{"bad host!:80", true, false, false},
	}
	for _, c := range cases {
		err := m.validateListener(c.listen, c.tcp, c.udp, "")
		if (err == nil) != c.ok {
			t.Errorf("validateListener(%q, tcp=%v, udp=%v) = %v", c.listen, c.tcp, c.udp, err)
		}
	}
}
