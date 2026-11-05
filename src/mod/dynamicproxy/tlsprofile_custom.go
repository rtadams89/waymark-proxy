package dynamicproxy

// TLS profiles (https://wiki.mozilla.org/Security/Server_Side_TLS): the
// lowest TLS version and the TLS 1.0-1.2 cipher suites.

import (
	"crypto/tls"
	"net/http"
	"slices"
	"sync"
)

type TLSProfile string

const (
	// TLS 1.3 only
	TLSProfileModern TLSProfile = "modern"
	// TLS 1.2 and 1.3; TLS 1.2 with ECDHE AES-GCM and ChaCha20-Poly1305 only
	TLSProfileIntermediate TLSProfile = "intermediate"
	// TLS 1.2 and 1.3 with Go's default suites
	TLSProfileCompatible TLSProfile = "compatible"
	// TLS 1.0 to 1.3 with Go's default suites
	TLSProfileLegacy TLSProfile = "legacy"
)

func (p TLSProfile) IsValid() bool {
	switch p {
	case TLSProfileModern, TLSProfileIntermediate, TLSProfileCompatible, TLSProfileLegacy:
		return true
	}
	return false
}

func (p TLSProfile) MinVersion() uint16 {
	switch p {
	case TLSProfileModern:
		return tls.VersionTLS13
	case TLSProfileLegacy:
		return tls.VersionTLS10
	}
	return tls.VersionTLS12
}

var intermediateSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// CipherSuites returns nil for Go's default suites.
func (p TLSProfile) CipherSuites() []uint16 {
	if p == TLSProfileIntermediate {
		return append([]uint16(nil), intermediateSuites...)
	}
	return nil
}

// Apply sets config's lowest version and suites; an unknown profile applies
// intermediate.
func (p TLSProfile) Apply(config *tls.Config) {
	if !p.IsValid() {
		p = TLSProfileIntermediate
	}
	config.MinVersion = p.MinVersion()
	config.CipherSuites = p.CipherSuites()
}

func (router *Router) SetTLSProfile(p TLSProfile) {
	router.Option.TLSProfile = p
	router.restartIfRunning()
}

// installTLSHooks sets each client's profile from Option.TLSProfileFor and
// records its hello for the failed handshake list.
func (router *Router) installTLSHooks(srv *http.Server, base *tls.Config) {
	tracker := router.tlsFailureTracker()
	tracker.hook(srv)
	main := router.Option.TLSProfile
	if !main.IsValid() {
		main = TLSProfileIntermediate
	}
	choose := router.Option.TLSProfileFor

	// Built at the first handshake that needs one, after the server has set
	// base's ALPN protocols
	var once sync.Once
	var perProfile map[TLSProfile]*tls.Config
	build := func() {
		perProfile = map[TLSProfile]*tls.Config{}
		for _, p := range []TLSProfile{TLSProfileModern, TLSProfileIntermediate, TLSProfileCompatible, TLSProfileLegacy} {
			if p == main {
				continue
			}
			c := base.Clone()
			c.GetConfigForClient = nil
			p.Apply(c)
			if !slices.Contains(c.NextProtos, "http/1.1") {
				c.NextProtos = append(c.NextProtos, "http/1.1")
			}
			perProfile[p] = c
		}
	}

	base.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		profile, exception := main, ""
		var cfg *tls.Config
		if choose != nil && h != nil && h.Conn != nil {
			if p, name, ok := choose(addrIP(h.Conn.RemoteAddr().String())); ok && p.IsValid() {
				profile, exception = p, name
				if p != main {
					once.Do(build)
					cfg = perProfile[p]
				}
			}
		}
		tracker.recordHello(h, profile, exception)
		return cfg, nil
	}
}
