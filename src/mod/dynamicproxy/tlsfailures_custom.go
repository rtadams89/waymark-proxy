package dynamicproxy

// Failed TLS handshakes on the HTTPS listener. Each client's hello is kept
// by remote address until the connection is active or closed; a handshake
// error from the server's error log is matched to it, counted and logged.

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type TLSFailureKind string

const (
	TLSFailureVersion TLSFailureKind = "version"
	TLSFailureCipher  TLSFailureKind = "cipher"
	TLSFailureOther   TLSFailureKind = "other"
)

type TLSFailure struct {
	Time      int64          `json:"time"`
	Client    string         `json:"client"`
	Host      string         `json:"host"` // SNI
	Kind      TLSFailureKind `json:"kind"`
	Profile   TLSProfile     `json:"profile"`
	Exception string         `json:"exception,omitempty"`
	Reason    string         `json:"reason"`
}

type TLSFailureSummary struct {
	Since  int64                  `json:"since"`
	Counts map[TLSFailureKind]int `json:"counts"`
	Recent []TLSFailure           `json:"recent"` // newest first
}

const (
	tlsHelloMaxAge     = 2 * time.Minute
	tlsHelloMaxPending = 20000
	tlsFailureRecent   = 50
	tlsFailureLogEvery = 10 * time.Minute // per client, host and kind
	tlsFailureLogMax   = 5000
	tlsHandshakePrefix = "http: TLS handshake error from "
	maxSuitesInMessage = 8
)

type tlsHello struct {
	at        time.Time
	host      string
	versions  []uint16
	suites    []uint16
	profile   TLSProfile
	exception string
}

type tlsFailureTracker struct {
	mu        sync.Mutex
	now       func() time.Time
	profile   func() TLSProfile
	logf      func(string)
	pending   map[string]tlsHello // by remote address
	since     time.Time
	counts    map[TLSFailureKind]int
	recent    []TLSFailure
	lastLog   map[string]time.Time
	lastSweep time.Time
}

func newTLSFailureTracker(profile func() TLSProfile, logf func(string)) *tlsFailureTracker {
	t := &tlsFailureTracker{now: time.Now, profile: profile, logf: logf}
	t.reset()
	return t
}

func (t *tlsFailureTracker) reset() {
	t.pending = map[string]tlsHello{}
	t.since = t.now()
	t.counts = map[TLSFailureKind]int{}
	t.recent = []TLSFailure{}
	t.lastLog = map[string]time.Time{}
}

func (t *tlsFailureTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	pending := t.pending
	t.reset()
	t.pending = pending
}

func (t *tlsFailureTracker) recordHello(h *tls.ClientHelloInfo, profile TLSProfile, exception string) {
	if h == nil || h.Conn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if now.Sub(t.lastSweep) > tlsHelloMaxAge || len(t.pending) >= tlsHelloMaxPending {
		for k, v := range t.pending {
			if now.Sub(v.at) > tlsHelloMaxAge {
				delete(t.pending, k)
			}
		}
		t.lastSweep = now
	}
	if len(t.pending) >= tlsHelloMaxPending {
		return
	}
	t.pending[h.Conn.RemoteAddr().String()] = tlsHello{
		at:        now,
		host:      h.ServerName,
		versions:  append([]uint16(nil), h.SupportedVersions...),
		suites:    append([]uint16(nil), h.CipherSuites...),
		profile:   profile,
		exception: exception,
	}
}

// connState drops the hello once the connection is active or closed.
func (t *tlsFailureTracker) connState(c net.Conn, s http.ConnState) {
	if s == http.StateActive || s == http.StateClosed || s == http.StateHijacked {
		t.mu.Lock()
		delete(t.pending, c.RemoteAddr().String())
		t.mu.Unlock()
	}
}

func (t *tlsFailureTracker) handshakeFailed(addr, goReason string) {
	t.mu.Lock()
	hello, ok := t.pending[addr]
	delete(t.pending, addr)
	if !ok {
		t.mu.Unlock()
		return
	}
	profile := hello.profile
	if profile == "" {
		profile = t.profile()
	}
	f := TLSFailure{Time: t.now().Unix(), Client: addrIP(addr), Host: hello.host, Profile: profile, Exception: hello.exception}
	f.Kind, f.Reason = explainTLSFailure(goReason, hello, profile)
	t.counts[f.Kind]++
	t.recent = append([]TLSFailure{f}, t.recent...)
	if len(t.recent) > tlsFailureRecent {
		t.recent = t.recent[:tlsFailureRecent]
	}
	key := f.Client + "|" + f.Host + "|" + string(f.Kind)
	now := t.now()
	shouldLog := now.Sub(t.lastLog[key]) >= tlsFailureLogEvery
	if shouldLog {
		if len(t.lastLog) >= tlsFailureLogMax {
			for k, v := range t.lastLog {
				if now.Sub(v) >= tlsFailureLogEvery {
					delete(t.lastLog, k)
				}
			}
		}
		if len(t.lastLog) < tlsFailureLogMax {
			t.lastLog[key] = now
		}
	}
	t.mu.Unlock()

	if shouldLog && t.logf != nil {
		host := f.Host
		if host == "" {
			host = "(no host name)"
		}
		t.logf("TLS handshake failed: client " + f.Client + ", host " + host + ": " + f.Reason + " (further failures from this client for this host are not logged for 10 minutes)")
	}
}

func (t *tlsFailureTracker) Summary() TLSFailureSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	counts := map[TLSFailureKind]int{TLSFailureVersion: 0, TLSFailureCipher: 0, TLSFailureOther: 0}
	for k, v := range t.counts {
		counts[k] = v
	}
	return TLSFailureSummary{Since: t.since.Unix(), Counts: counts, Recent: append([]TLSFailure{}, t.recent...)}
}

// errorLogWriter sends handshake errors to the tracker and other lines to
// the standard log.
type errorLogWriter struct{ t *tlsFailureTracker }

func (w errorLogWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\r\n")
	if rest, ok := strings.CutPrefix(line, tlsHandshakePrefix); ok {
		// rest is "<addr>: <reason>"
		if addr, reason, ok := strings.Cut(rest, ": "); ok {
			w.t.handshakeFailed(addr, reason)
			return len(p), nil
		}
	}
	log.Print(line)
	return len(p), nil
}

func (t *tlsFailureTracker) hook(srv *http.Server) {
	srv.ConnState = t.connState
	srv.ErrorLog = log.New(errorLogWriter{t}, "", 0)
}

var tlsProfileNames = map[TLSProfile]string{
	TLSProfileModern:       "Modern",
	TLSProfileIntermediate: "Intermediate",
	TLSProfileCompatible:   "Compatible",
	TLSProfileLegacy:       "Legacy",
}

func profileName(p TLSProfile) string {
	if n, ok := tlsProfileNames[p]; ok {
		return n
	}
	return string(p)
}

func explainTLSFailure(goReason string, h tlsHello, p TLSProfile) (TLSFailureKind, string) {
	profile := "the " + profileName(p) + " TLS profile"
	if h.exception != "" {
		profile += " (from the exception \"" + h.exception + "\")"
	}
	switch {
	case strings.Contains(goReason, "unsupported versions") || strings.Contains(goReason, "protocol version not supported"):
		return TLSFailureVersion, "the client only supports " + versionRange(h.versions) + ", and " + profile + " requires " + tls.VersionName(p.MinVersion()) + " or later"
	case strings.Contains(goReason, "no cipher suite supported"):
		return TLSFailureCipher, "none of the client's cipher suites are allowed by " + profile + "; it offered " + suiteList(h.suites)
	}
	return TLSFailureOther, explainOtherTLSFailure(goReason)
}

func explainOtherTLSFailure(goReason string) string {
	if alert, ok := strings.CutPrefix(goReason, "remote error: tls: "); ok {
		switch alert {
		case "bad certificate", "unknown certificate authority", "certificate unknown", "certificate expired", "unsupported certificate", "certificate revoked":
			return "the client does not accept the server's certificate (it sent \"" + alert + "\")"
		}
		return "the client ended the handshake with the error \"" + alert + "\""
	}
	switch {
	case goReason == "EOF" || strings.Contains(goReason, "connection reset by peer") || strings.Contains(goReason, "broken pipe"):
		return "the client closed the connection during the handshake (" + goReason + ")"
	case strings.Contains(goReason, "i/o timeout"):
		return "the client did not finish the handshake in time"
	case strings.Contains(goReason, "no certificates configured"):
		return "there is no certificate for the host the client asked for (" + goReason + ")"
	}
	return "the handshake failed: " + goReason
}

// isGREASE reports RFC 8701 values.
func isGREASE(v uint16) bool { return v&0x0f0f == 0x0a0a && v>>8 == v&0xff }

func versionRange(versions []uint16) string {
	var lo, hi uint16
	for _, v := range versions {
		if isGREASE(v) || v < tls.VersionSSL30 || v > tls.VersionTLS13 {
			continue
		}
		if lo == 0 || v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	switch {
	case lo == 0:
		return "unknown TLS versions"
	case lo == hi:
		return tls.VersionName(lo)
	}
	return tls.VersionName(lo) + " to " + strings.TrimPrefix(tls.VersionName(hi), "TLS ")
}

func suiteList(suites []uint16) string {
	names := []string{}
	for _, s := range suites {
		if isGREASE(s) || s == 0x00ff { // TLS_EMPTY_RENEGOTIATION_INFO_SCSV
			continue
		}
		names = append(names, tls.CipherSuiteName(s))
	}
	if len(names) == 0 {
		return "no cipher suites"
	}
	if len(names) > maxSuitesInMessage {
		return strings.Join(names[:maxSuitesInMessage], ", ") + fmt.Sprintf(" and %d more", len(names)-maxSuitesInMessage)
	}
	return strings.Join(names, ", ")
}

func addrIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func (router *Router) TLSFailures() TLSFailureSummary {
	return router.tlsFailureTracker().Summary()
}

func (router *Router) ClearTLSFailures() {
	router.tlsFailureTracker().Clear()
}

func (router *Router) tlsFailureTracker() *tlsFailureTracker {
	router.tlsFailuresOnce.Do(func() {
		router.tlsFailures = newTLSFailureTracker(
			func() TLSProfile { return router.Option.TLSProfile },
			func(msg string) {
				if router.Option.Logger != nil {
					router.Option.Logger.PrintAndLog("tls", msg, nil)
				}
			})
	})
	return router.tlsFailures
}
