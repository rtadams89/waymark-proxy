package dynamicproxy

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeAddrConn struct {
	net.Conn
	addr string
}

func (c fakeAddrConn) RemoteAddr() net.Addr { a, _ := net.ResolveTCPAddr("tcp", c.addr); return a }

func newTestTracker(p TLSProfile) (*tlsFailureTracker, *[]string, *time.Time) {
	logs := []string{}
	now := time.Unix(1_800_000_000, 0)
	t := newTLSFailureTracker(func() TLSProfile { return p }, func(s string) { logs = append(logs, s) })
	t.now = func() time.Time { return now }
	t.reset()
	return t, &logs, &now
}

func hello(addr, host string, versions, suites []uint16) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{Conn: fakeAddrConn{addr: addr}, ServerName: host, SupportedVersions: versions, CipherSuites: suites}
}

func TestTLSFailureVersion(t *testing.T) {
	tr, logs, _ := newTestTracker(TLSProfileIntermediate)
	tr.recordHello(hello("192.0.2.10:5000", "app.example.com", []uint16{tls.VersionTLS11, tls.VersionTLS10}, []uint16{0xc013}), "", "")
	w := errorLogWriter{tr}
	w.Write([]byte("http: TLS handshake error from 192.0.2.10:5000: tls: client offered only unsupported versions: [302 301]\n"))
	s := tr.Summary()
	if s.Counts[TLSFailureVersion] != 1 || len(s.Recent) != 1 {
		t.Fatalf("not counted: %+v", s)
	}
	f := s.Recent[0]
	if f.Client != "192.0.2.10" || f.Host != "app.example.com" || f.Profile != TLSProfileIntermediate {
		t.Fatalf("wrong details: %+v", f)
	}
	if !strings.Contains(f.Reason, "TLS 1.0 to 1.1") || !strings.Contains(f.Reason, "Intermediate TLS profile requires TLS 1.2") {
		t.Fatalf("unclear reason: %s", f.Reason)
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "app.example.com") || !strings.Contains((*logs)[0], "192.0.2.10") {
		t.Fatalf("not logged: %v", *logs)
	}
	if len(tr.pending) != 0 {
		t.Fatal("the hello was kept after the failure")
	}
}

func TestTLSFailureCipherNamesSuitesAndSkipsGREASE(t *testing.T) {
	tr, _, _ := newTestTracker(TLSProfileIntermediate)
	tr.recordHello(hello("[2001:db8::1]:443", "", []uint16{0x1a1a, tls.VersionTLS12}, []uint16{0x2a2a, tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, 0x00ff}), "", "")
	tr.handshakeFailed("[2001:db8::1]:443", "tls: no cipher suite supported by both client and server; client offered: [2a2a c013 ff]")
	f := tr.Summary().Recent[0]
	if f.Kind != TLSFailureCipher || f.Client != "2001:db8::1" {
		t.Fatalf("wrong failure: %+v", f)
	}
	if !strings.Contains(f.Reason, "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA") || strings.Contains(f.Reason, "0x") || strings.Contains(f.Reason, "SCSV") {
		t.Fatalf("suites not named cleanly: %s", f.Reason)
	}
}

func TestTLSFailureWithoutHelloIsIgnored(t *testing.T) {
	tr, logs, _ := newTestTracker(TLSProfileModern)
	w := errorLogWriter{tr}
	w.Write([]byte("http: TLS handshake error from 198.51.100.7:1234: EOF\n"))
	w.Write([]byte("http: TLS handshake error from 198.51.100.7:1235: client sent an HTTP request to an HTTPS server\n"))
	if s := tr.Summary(); len(s.Recent) != 0 || len(*logs) != 0 {
		t.Fatalf("a client that sent no hello was reported: %+v %v", s, *logs)
	}
}

func TestTLSFailureSuccessfulHandshakeIsForgotten(t *testing.T) {
	tr, _, _ := newTestTracker(TLSProfileModern)
	c := fakeAddrConn{addr: "192.0.2.1:9"}
	tr.recordHello(hello(c.addr, "a", []uint16{tls.VersionTLS13}, nil), "", "")
	tr.connState(c, http.StateActive)
	tr.handshakeFailed(c.addr, "EOF")
	if len(tr.Summary().Recent) != 0 {
		t.Fatal("a failure was reported for a connection whose handshake succeeded")
	}
}

func TestTLSFailureLogIsRateLimited(t *testing.T) {
	tr, logs, now := newTestTracker(TLSProfileIntermediate)
	fail := func(port int) {
		addr := fmt.Sprintf("192.0.2.20:%d", port)
		tr.recordHello(hello(addr, "h", []uint16{tls.VersionTLS10}, nil), "", "")
		tr.handshakeFailed(addr, "tls: client offered only unsupported versions: [301]")
	}
	for i := 0; i < 5; i++ {
		fail(1000 + i)
	}
	if len(*logs) != 1 || tr.Summary().Counts[TLSFailureVersion] != 5 {
		t.Fatalf("expected 1 log line and 5 counted, got %d lines, %+v", len(*logs), tr.Summary().Counts)
	}
	*now = now.Add(11 * time.Minute)
	fail(2000)
	if len(*logs) != 2 {
		t.Fatalf("not logged again after 10 minutes: %d lines", len(*logs))
	}
}

func TestTLSFailureOtherLinesPassThroughAndClear(t *testing.T) {
	tr, _, _ := newTestTracker(TLSProfileIntermediate)
	tr.recordHello(hello("192.0.2.30:1", "h", []uint16{tls.VersionTLS13}, nil), "", "")
	tr.handshakeFailed("192.0.2.30:1", "remote error: tls: bad certificate")
	s := tr.Summary()
	if s.Counts[TLSFailureOther] != 1 || s.Recent[0].Reason != `the client does not accept the server's certificate (it sent "bad certificate")` {
		t.Fatalf("other failure not kept as reported: %+v", s)
	}
	if n, _ := (errorLogWriter{tr}).Write([]byte("http: Accept error: something\n")); n == 0 {
		t.Fatal("other error log lines were not consumed")
	}
	tr.Clear()
	if js, _ := json.Marshal(tr.Summary()); !strings.Contains(string(js), `"recent":[]`) {
		t.Fatalf("an empty list is not sent as []: %s", js)
	}
	if s := tr.Summary(); len(s.Recent) != 0 || s.Counts[TLSFailureOther] != 0 || s.Counts[TLSFailureVersion] != 0 {
		t.Fatalf("Clear kept failures: %+v", s)
	}
}

func TestTLSFailurePendingIsBounded(t *testing.T) {
	tr, _, now := newTestTracker(TLSProfileIntermediate)
	for i := 0; i < tlsHelloMaxPending+10; i++ {
		tr.recordHello(hello(fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255), "", nil, nil), "", "")
	}
	if len(tr.pending) > tlsHelloMaxPending {
		t.Fatalf("%d pending hellos kept", len(tr.pending))
	}
	*now = now.Add(3 * time.Minute)
	tr.recordHello(hello("192.0.2.40:1", "", nil, nil), "", "")
	if len(tr.pending) != 1 {
		t.Fatalf("old hellos were not dropped: %d", len(tr.pending))
	}
}

func TestVersionRange(t *testing.T) {
	cases := map[string][]uint16{
		"TLS 1.3":              {tls.VersionTLS13},
		"TLS 1.0 to 1.2":       {tls.VersionTLS12, tls.VersionTLS11, tls.VersionTLS10, 0x3a3a},
		"unknown TLS versions": {},
	}
	for want, v := range cases {
		if got := versionRange(v); got != want {
			t.Errorf("%v: got %q, want %q", v, got, want)
		}
	}
}

func TestExplainOtherTLSFailure(t *testing.T) {
	cases := map[string]string{
		"remote error: tls: unknown certificate authority": "does not accept the server's certificate",
		"remote error: tls: handshake failure":             `ended the handshake with the error "handshake failure"`,
		"EOF":                                              "closed the connection during the handshake",
		"read tcp 1.2.3.4:443->5.6.7.8:9: i/o timeout":     "did not finish the handshake in time",
		"local error: tls: bad record MAC":                 "the handshake failed: local error: tls: bad record MAC",
	}
	for in, want := range cases {
		if got := explainOtherTLSFailure(in); !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want it to contain %q", in, got, want)
		}
	}
}
