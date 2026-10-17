package healthcheck

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const userAgent = "waymark-healthcheck/1"

// ProbeResult is the outcome of one active check.
type ProbeResult struct {
	OK         bool
	Detail     string // e.g. "status 200", "connection refused", "timed out after 3s"
	Latency    time.Duration
	StatusCode int // HTTP checks only; 0 if there was no HTTP response
}

// Upstream identifies what to probe.
type Upstream struct {
	Addr          string // OriginIpOrDomain, e.g. "10.0.0.5:8080"
	RequireTLS    bool
	SkipTLSVerify bool
}

// Prober runs one active check. Replaceable in tests.
type Prober func(ctx context.Context, up Upstream, cfg *Config) ProbeResult

// DefaultProber dispatches on the configured check type.
func DefaultProber(ctx context.Context, up Upstream, cfg *Config) ProbeResult {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	var res ProbeResult
	switch cfg.Type {
	case CheckHTTPGet, CheckHTTPHead:
		res = probeHTTP(ctx, up, cfg, timeout)
	case CheckTCP:
		res = probeTCP(ctx, up, timeout)
	case CheckICMP:
		res = probeICMP(ctx, up, timeout)
	default:
		res = ProbeResult{Detail: "unknown check type " + string(cfg.Type)}
	}
	if res.Latency == 0 {
		res.Latency = time.Since(start)
	}
	return res
}

func probeHTTP(ctx context.Context, up Upstream, cfg *Config, timeout time.Duration) ProbeResult {
	scheme := "http"
	if up.RequireTLS {
		scheme = "https"
	}
	target, err := url.Parse(scheme + "://" + up.Addr + cfg.Path)
	if err != nil || target.Host == "" {
		return ProbeResult{Detail: "invalid upstream address or path"}
	}
	method := http.MethodGet
	if cfg.Type == CheckHTTPHead {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return ProbeResult{Detail: "could not build request: " + err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	tlsConfig := &tls.Config{InsecureSkipVerify: up.SkipTLSVerify}
	if cfg.HostHeader != "" {
		req.Host = cfg.HostHeader
		if h, _, err := net.SplitHostPort(cfg.HostHeader); err == nil {
			tlsConfig.ServerName = h
		} else {
			tlsConfig.ServerName = cfg.HostHeader
		}
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// One-shot connections, like the upstream uptime monitor (see #1241)
			DisableKeepAlives:     true,
			TLSClientConfig:       tlsConfig,
			ResponseHeaderTimeout: timeout,
			Proxy:                 nil, // never route health checks through an environment proxy
		},
		// A redirect is an answer; judge it against ExpectedStatus instead of following it
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return ProbeResult{Detail: describeError(err, timeout), Latency: latency}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, bodyReadLimit))
	resp.Body.Close()

	ranges, _ := ParseStatusRanges(cfg.ExpectedStatus) // validated on save
	ok := statusMatches(resp.StatusCode, ranges)
	detail := fmt.Sprintf("status %d", resp.StatusCode)
	if !ok {
		detail += " (expected " + cfg.ExpectedStatus + ")"
	} else if cfg.BodyContains != "" && cfg.Type == CheckHTTPGet {
		switch {
		case readErr != nil:
			ok = false
			detail += ", but the response body could not be read: " + describeError(readErr, timeout)
		case !strings.Contains(string(body), cfg.BodyContains):
			ok = false
			detail += fmt.Sprintf(", but the response does not contain %q", cfg.BodyContains)
		default:
			detail += fmt.Sprintf(", contains %q", cfg.BodyContains)
		}
	}
	return ProbeResult{OK: ok, Detail: detail, Latency: latency, StatusCode: resp.StatusCode}
}

func probeTCP(ctx context.Context, up Upstream, timeout time.Duration) ProbeResult {
	addr := withDefaultPort(up.Addr, up.RequireTLS)
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		return ProbeResult{Detail: describeError(err, timeout), Latency: latency}
	}
	conn.Close()
	return ProbeResult{OK: true, Detail: "connected", Latency: latency}
}

// withDefaultPort adds :80 or :443 when the upstream address has no port.
func withDefaultPort(addr string, tlsOn bool) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	port := "80"
	if tlsOn {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), port)
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return strings.Trim(addr, "[]")
}

func probeICMP(ctx context.Context, up Upstream, timeout time.Duration) ProbeResult {
	host := hostOnly(up.Addr)
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return ProbeResult{Detail: "could not resolve " + host}
	}
	ip := ips[0].IP
	for _, a := range ips {
		if a.IP.To4() != nil { // prefer IPv4
			ip = a.IP
			break
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}
	rtt, err := pingOnce(ip, deadline)
	if err != nil {
		return ProbeResult{Detail: describeError(err, timeout)}
	}
	return ProbeResult{OK: true, Detail: "reply from " + ip.String(), Latency: rtt}
}

// pingOnce sends one ICMP echo request and waits for the matching reply.
// It first uses an unprivileged ICMP socket (allowed by Debian's default
// net.ipv4.ping_group_range), then falls back to a raw socket (root or
// CAP_NET_RAW).
func pingOnce(ip net.IP, deadline time.Time) (time.Duration, error) {
	v4 := ip.To4() != nil
	type mode struct {
		network, laddr string
		udp            bool
	}
	var modes []mode
	if v4 {
		modes = []mode{{"udp4", "0.0.0.0", true}, {"ip4:icmp", "0.0.0.0", false}}
	} else {
		modes = []mode{{"udp6", "::", true}, {"ip6:ipv6-icmp", "::", false}}
	}

	var conn *icmp.PacketConn
	var udp bool
	var lastErr error
	for _, m := range modes {
		c, err := icmp.ListenPacket(m.network, m.laddr)
		if err == nil {
			conn, udp = c, m.udp
			break
		}
		lastErr = err
	}
	if conn == nil {
		return 0, fmt.Errorf("ICMP not permitted (allow it with sysctl net.ipv4.ping_group_range, or grant CAP_NET_RAW): %w", lastErr)
	}
	defer conn.Close()

	var idb [4]byte
	rand.Read(idb[:])
	id := int(binary.BigEndian.Uint16(idb[:2]))
	seq := int(binary.BigEndian.Uint16(idb[2:]))
	payload := []byte("waymark-healthcheck")

	var echoType, replyType icmp.Type
	proto := 1
	if v4 {
		echoType, replyType = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	} else {
		echoType, replyType = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
		proto = 58
	}
	msg := icmp.Message{Type: echoType, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	var dst net.Addr = &net.IPAddr{IP: ip}
	if udp {
		dst = &net.UDPAddr{IP: ip}
	}

	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	start := time.Now()
	if _, err := conn.WriteTo(wb, dst); err != nil {
		return 0, err
	}
	rb := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(rb)
		if err != nil {
			return 0, err
		}
		if !sameIP(peer, ip) {
			continue
		}
		reply, err := icmp.ParseMessage(proto, rb[:n])
		if err != nil || reply.Type != replyType {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok || echo.Seq != seq {
			continue
		}
		// Unprivileged sockets rewrite the ID to the socket's port; only
		// raw sockets see our own ID
		if !udp && echo.ID != id {
			continue
		}
		return time.Since(start), nil
	}
}

func sameIP(a net.Addr, ip net.IP) bool {
	switch v := a.(type) {
	case *net.IPAddr:
		return v.IP.Equal(ip)
	case *net.UDPAddr:
		return v.IP.Equal(ip)
	}
	return false
}

// describeError turns network errors into short, log-friendly reasons.
func describeError(err error, timeout time.Duration) string {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("timed out after %s", timeout)
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "host unreachable"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS lookup failed for " + dnsErr.Name
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "TLS certificate not trusted (enable Skip TLS Validation on the upstream if it is self-signed)"
	}
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160] + "…"
	}
	return msg
}
