package healthcheck

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStageInTLSHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	cfg := DefaultConfig()
	cfg.Type, cfg.TimeoutSeconds, cfg.Path = CheckHTTPGet, 1, "/"
	addr := ln.Addr().String()
	r := DefaultProber(context.Background(), Upstream{Addr: addr, RequireTLS: true, SkipTLSVerify: true}, cfg)
	want := "timed out after 1s in the TLS handshake with " + addr + " (connect "
	if r.OK || !strings.HasPrefix(r.Detail, want) {
		t.Fatalf("got %q, want prefix %q", r.Detail, want)
	}
}

func TestStageReadingHeaders(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				c.Read(buf)
				c.Write([]byte("HTTP/1.1 200 OK\r\n"))
				time.Sleep(3 * time.Second)
			}(c)
		}
	}()
	cfg := DefaultConfig()
	cfg.Type, cfg.TimeoutSeconds, cfg.Path = CheckHTTPGet, 1, "/"
	r := DefaultProber(context.Background(), Upstream{Addr: ln.Addr().String()}, cfg)
	if r.OK || !strings.Contains(r.Detail, "reading the response headers") || !strings.Contains(r.Detail, "first byte") {
		t.Fatalf("got %q", r.Detail)
	}
}

func TestStageDNSFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Type, cfg.TimeoutSeconds = CheckTCP, 2
	r := DefaultProber(context.Background(), Upstream{Addr: "does-not-exist.invalid:80"}, cfg)
	if r.OK || !strings.HasPrefix(r.Detail, "DNS lookup failed for does-not-exist.invalid") || strings.Contains(r.Detail, "in DNS") {
		t.Fatalf("got %q", r.Detail)
	}
}

func TestStageSuccessfulHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	cfg := DefaultConfig()
	cfg.Type, cfg.Path = CheckHTTPGet, "/"
	r := DefaultProber(context.Background(), Upstream{Addr: strings.TrimPrefix(srv.URL, "https://"), RequireTLS: true, SkipTLSVerify: true}, cfg)
	if !r.OK || r.Detail != "status 200" {
		t.Fatalf("got %+v", r)
	}
}

func TestStagesDescribe(t *testing.T) {
	s := &stages{}
	s.begin("looking up app.lan in DNS")
	s.done("DNS")
	s.begin("connecting to 10.0.0.5:443")
	got := s.describe(context.DeadlineExceeded, 5*time.Second)
	if !strings.HasPrefix(got, "timed out after 5s connecting to 10.0.0.5:443 (DNS ") {
		t.Fatalf("got %q", got)
	}
	s.done("connect")
	s.begin("in the TLS handshake with 10.0.0.5:443")
	got = s.describe(&tls.CertificateVerificationError{Err: context.Canceled}, 5*time.Second)
	if strings.Contains(got, "in the TLS handshake") || !strings.Contains(got, "connect ") {
		t.Fatalf("certificate error: %q", got)
	}
	for d, want := range map[time.Duration]string{500 * time.Microsecond: "<1ms", 42 * time.Millisecond: "42ms", 2345 * time.Millisecond: "2.35s"} {
		if got := shortDuration(d); got != want {
			t.Errorf("%s: got %s, want %s", d, got, want)
		}
	}
}
