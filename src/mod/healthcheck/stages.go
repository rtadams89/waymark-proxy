package healthcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

// stages records which step of a check is running and how long the
// finished steps took.
type stages struct {
	mu       sync.Mutex
	current  string // e.g. "connecting to 10.0.0.5:443"
	started  time.Time
	finished []string // e.g. "DNS 2ms"
	addr     string   // address of the last successful connection
}

func (s *stages) begin(current string) {
	s.mu.Lock()
	s.current, s.started = current, time.Now()
	s.mu.Unlock()
}

// done records the current step as finished under label.
func (s *stages) done(label string) {
	s.mu.Lock()
	if s.current != "" {
		s.finished = append(s.finished, label+" "+shortDuration(time.Since(s.started)))
	}
	s.current = ""
	s.mu.Unlock()
}

// describe returns reason with the running step and the finished steps,
// e.g. "timed out after 5s in the TLS handshake with 10.0.0.5:443
// (DNS 2ms, connect 1ms)".
func (s *stages) describe(err error, timeout time.Duration) string {
	reason := describeError(err, timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	var certErr *tls.CertificateVerificationError
	if s.current != "" && !strings.HasPrefix(reason, "DNS lookup failed") && !errors.As(err, &certErr) {
		reason += " " + s.current
	}
	if len(s.finished) > 0 {
		reason += " (" + strings.Join(s.finished, ", ") + ")"
	}
	return reason
}

// trace returns ctx with an HTTP client trace that updates s.
func (s *stages) trace(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) { s.begin("looking up " + info.Host + " in DNS") },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if info.Err == nil {
				s.done("DNS")
			}
		},
		ConnectStart: func(_, addr string) { s.begin("connecting to " + addr) },
		ConnectDone: func(_, addr string, err error) {
			if err == nil {
				s.mu.Lock()
				s.addr = addr
				s.mu.Unlock()
				s.done("connect")
			}
		},
		TLSHandshakeStart: func() {
			s.mu.Lock()
			addr := s.addr
			s.mu.Unlock()
			s.begin("in the TLS handshake with " + addr)
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				s.done("TLS")
			}
		},
		GotConn:              func(httptrace.GotConnInfo) { s.begin("sending the request") },
		WroteRequest:         func(httptrace.WroteRequestInfo) { s.begin("waiting for the response") },
		GotFirstResponseByte: func() { s.done("first byte"); s.begin("reading the response headers") },
	})
}

// dial resolves the host of addr, then connects to its addresses in turn.
func (s *stages) dial(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips := []string{host}
	if net.ParseIP(host) == nil {
		s.begin("looking up " + host + " in DNS")
		found, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return nil, err
		}
		s.done("DNS")
		ips = found
	}
	d := net.Dialer{Timeout: timeout}
	var lastErr error
	for _, ip := range ips {
		target := net.JoinHostPort(ip, port)
		s.begin("connecting to " + target)
		conn, err := d.DialContext(ctx, network, target)
		if err == nil {
			s.done("connect")
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(10 * time.Millisecond).String()
}
