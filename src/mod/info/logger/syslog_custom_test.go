package logger

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLogOutputsValidate(t *testing.T) {
	ok := LogOutputs{Syslog: []SyslogDestination{{Host: "logs.example.com", Protocol: "tls"}, {Host: "10.0.0.5", Protocol: "UDP"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	d := ok.Syslog[0]
	if d.Port != 6514 || d.Framing != "octet" || d.Format != "rfc5424" || d.Facility != "local0" {
		t.Fatalf("tls defaults: %+v", d)
	}
	if u := ok.Syslog[1]; u.Port != 514 || u.Protocol != "udp" || u.Framing != "newline" {
		t.Fatalf("udp defaults: %+v", u)
	}
	bad := []SyslogDestination{
		{Host: "", Protocol: "udp"},
		{Host: "bad host", Protocol: "udp"},
		{Host: "a;b", Protocol: "udp"},
		{Host: "10.0.0.5", Protocol: "http"},
		{Host: "10.0.0.5", Protocol: "udp", Port: 70000},
		{Host: "10.0.0.5", Protocol: "udp", Format: "cef"},
		{Host: "10.0.0.5", Protocol: "udp", Facility: "local9"},
		{Host: "10.0.0.5", Protocol: "tcp", Framing: "crlf"},
		{Host: "10.0.0.5", Protocol: "tls", TLSCA: "not a pem"},
		{Host: "10.0.0.5", Protocol: "udp", Name: "<script>"},
	}
	for _, b := range bad {
		o := LogOutputs{Syslog: []SyslogDestination{b}}
		if err := o.Validate(); err == nil {
			t.Errorf("accepted %+v", b)
		}
	}
	many := LogOutputs{Syslog: make([]SyslogDestination, MaxSyslogDestinations+1)}
	if many.Validate() == nil {
		t.Error("too many servers accepted")
	}
}

func TestLogOutputsLoadSave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log_outputs.json")
	o, err := LoadLogOutputs(p)
	if err != nil || !o.SystemToFile || !o.TrafficToFile || len(o.Syslog) != 0 {
		t.Fatalf("missing file should give defaults: %+v %v", o, err)
	}
	o.TrafficToFile = false
	o.Syslog = []SyslogDestination{{Host: "10.0.0.5", Protocol: "udp", Enabled: true, Traffic: true}}
	o.Validate()
	if err := SaveLogOutputs(p, o); err != nil {
		t.Fatal(err)
	}
	back, err := LoadLogOutputs(p)
	if err != nil || back.TrafficToFile || len(back.Syslog) != 1 || back.Syslog[0].Port != 514 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	os.WriteFile(p, []byte(`{"syslog":[{"host":"x y","protocol":"udp"}]}`), 0644)
	if o, err := LoadLogOutputs(p); err == nil || !o.SystemToFile {
		t.Fatal("invalid file should give an error and the defaults")
	}
}

func sampleEvents() (*LogEvent, *LogEvent) {
	now := time.Date(2026, 10, 17, 13, 5, 9, 123456000, time.UTC)
	sys := &LogEvent{Stream: StreamSystem, Time: now, Severity: SeverityError, Module: "acme", Message: "renewal failed", Error: "timeout\nsecond line",
		Text: "[acme] [system:error] renewal failed: timeout\nsecond line"}
	tr := &LogEvent{Stream: StreamTraffic, Time: now, Severity: SeverityInfo, Class: "host-http", Origin: "app.example.com", Client: "10.0.0.9",
		UserAgent: "curl/8", Method: "GET", URI: "/x?y=1", Status: 200, Upstream: "10.0.0.20:8080"}
	tr.Text = "[router:host-http] [origin:app.example.com] [client: 10.0.0.9] [useragent: curl/8] GET /x?y=1 200"
	return sys, tr
}

func TestSyslogFormats(t *testing.T) {
	sys, tr := sampleEvents()
	d := SyslogDestination{Facility: "local0", Format: "rfc5424"}
	got := string(formatSyslog(&d, sys, "wm1"))
	want := "<131>1 2026-10-17T13:05:09.123456Z wm1 waymark " + syslogPID + " system - [acme] [system:error] renewal failed: timeout second line"
	if got != want {
		t.Fatalf("rfc5424:\n got %q\nwant %q", got, want)
	}
	d.Format, d.Facility = "rfc3164", "daemon"
	got = string(formatSyslog(&d, tr, "wm1"))
	want = "<30>" + tr.Time.Format(time.Stamp) + " wm1 waymark[" + syslogPID + "]: " + tr.Text
	if got != want {
		t.Fatalf("rfc3164:\n got %q\nwant %q", got, want)
	}
	d.Format = "json"
	var m map[string]any
	if err := json.Unmarshal(formatSyslog(&d, tr, "wm1"), &m); err != nil || m["status"].(float64) != 200 || m["uri"] != "/x?y=1" || m["host"] != "wm1" || m["stream"] != "traffic" {
		t.Fatalf("json: %v %v", m, err)
	}
	d.Format = "json5424"
	out := string(formatSyslog(&d, sys, "wm1"))
	i := strings.Index(out, " - {")
	if !strings.HasPrefix(out, "<27>1 ") || i < 0 || json.Unmarshal([]byte(out[i+3:]), &m) != nil || m["error"] != "timeout\nsecond line" {
		t.Fatalf("json5424: %q", out)
	}
	// Framing
	tcp := SyslogDestination{Protocol: "tcp", Framing: "octet"}
	if f := string(frame(&tcp, []byte("hello"))); f != "5 hello" {
		t.Fatalf("octet framing: %q", f)
	}
	tcp.Framing = "newline"
	if f := string(frame(&tcp, []byte("hello"))); f != "hello\n" {
		t.Fatalf("newline framing: %q", f)
	}
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSyslogDelivery(t *testing.T) {
	// UDP server
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udpGot := make(chan string, 10)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			udpGot <- string(buf[:n])
		}
	}()
	// TCP server, newline framed
	tcpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer tcpLn.Close()
	tcpGot := make(chan string, 10)
	go func() {
		for {
			c, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					tcpGot <- sc.Text()
				}
			}()
		}
	}()
	port := func(a net.Addr) int { _, p, _ := net.SplitHostPort(a.String()); n, _ := strconv.Atoi(p); return n }

	l, _ := NewFmtLogger()
	o := &LogOutputs{SystemToFile: true, TrafficToFile: true, Syslog: []SyslogDestination{
		{Name: "udp", Enabled: true, Host: "127.0.0.1", Port: port(udp.LocalAddr()), Protocol: "udp", System: true},
		{Name: "tcp", Enabled: true, Host: "127.0.0.1", Port: port(tcpLn.Addr()), Protocol: "tcp", Traffic: true, Format: "json"},
		{Name: "off", Enabled: false, Host: "127.0.0.1", Port: 9, Protocol: "tcp", System: true},
	}}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	l.ApplyOutputs(o)
	if n := len(l.SyslogStatuses()); n != 2 {
		t.Fatalf("disabled server started: %d senders", n)
	}
	l.Log("test", "hello system", nil, false)
	sys := <-udpGot
	if !strings.Contains(sys, " system - [test] [system:info] hello system") {
		t.Fatalf("udp got %q", sys)
	}
	req := httptest.NewRequest("GET", "http://app.example.com/path", nil)
	l.LogHTTPRequest(req, "host-http", 502, "app.example.com", "10.0.0.20", "10.0.0.9")
	select {
	case line := <-tcpGot:
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil || m["status"].(float64) != 502 || m["client"] != "10.0.0.9" {
			t.Fatalf("tcp got %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no traffic message over TCP")
	}
	// Streams go only where chosen: no traffic over UDP, no system over TCP
	select {
	case m := <-udpGot:
		t.Fatalf("traffic sent to the system-only server: %q", m)
	case m := <-tcpGot:
		t.Fatalf("system message sent to the traffic-only server: %q", m)
	case <-time.After(300 * time.Millisecond):
	}
	waitFor(t, "sent counters", func() bool {
		st := l.SyslogStatuses()
		return st[0].Sent == 1 && st[1].Sent == 1 && st[1].State == "connected"
	})

	// A TCP server that is down: the sender reports it and keeps the proxy unaffected
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadPort := port(dead.Addr())
	dead.Close()
	o2 := &LogOutputs{Syslog: []SyslogDestination{{Name: "down", Enabled: true, Host: "127.0.0.1", Port: deadPort, Protocol: "tcp", System: true}}}
	o2.Validate()
	l.ApplyOutputs(o2)
	for i := 0; i < syslogQueueSize+10; i++ {
		l.toSyslog(&LogEvent{Stream: StreamSystem, Time: time.Now(), Severity: SeverityInfo, Text: "x"})
	}
	waitFor(t, "error state", func() bool { st := l.SyslogStatuses()[0]; return st.State == "error" && st.Dropped >= 10 })
	if l.toFile(StreamSystem) || l.toFile(StreamTraffic) {
		t.Fatal("file outputs should follow the settings (both off in o2)")
	}
	l.ApplyOutputs(DefaultLogOutputs())
}

func TestSyslogTLS(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "logs.test"}, DNSNames: []string{"logs.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				// Octet counting: "<len> <msg>"
				lenStr, err := r.ReadString(' ')
				if err != nil {
					return
				}
				n, _ := strconv.Atoi(strings.TrimSpace(lenStr))
				buf := make([]byte, n)
				if _, err := r.Read(buf); err == nil {
					got <- string(buf)
				}
			}()
		}
	}()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	d := SyslogDestination{Host: "127.0.0.1", Port: port, Protocol: "tls", TLSVerify: true, TLSCA: caPEM, TLSName: "logs.test"}
	if err := TestSyslog(d); err != nil {
		t.Fatalf("TLS with the right CA: %v", err)
	}
	if m := <-got; !strings.Contains(m, "Test message from Waymark") {
		t.Fatalf("got %q", m)
	}
	// Wrong name: refused
	d.TLSName = "other.test"
	if err := TestSyslog(d); err == nil {
		t.Fatal("certificate for another name accepted")
	}
	// No CA given: the self-signed certificate is not trusted
	d.TLSName, d.TLSCA = "logs.test", ""
	if err := TestSyslog(d); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	// Verification off: accepted
	d.TLSVerify = false
	if err := TestSyslog(d); err != nil {
		t.Fatalf("verification off: %v", err)
	}
}
