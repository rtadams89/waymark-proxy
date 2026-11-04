package dynamicproxy

// TLS profiles for the proxy's HTTPS listener, after Mozilla's server side
// TLS guidelines (https://wiki.mozilla.org/Security/Server_Side_TLS). A
// profile sets the lowest TLS version and, for TLS 1.2, which cipher suites
// are offered. TLS 1.3 suites cannot be chosen in Go and are all strong.
// Key exchange groups (including the post-quantum hybrid) stay at Go's
// defaults.

import "crypto/tls"

// TLSProfile names a profile; the names are what is stored and sent by the API.
type TLSProfile string

const (
	// TLSProfileModern accepts TLS 1.3 only.
	TLSProfileModern TLSProfile = "modern"
	// TLSProfileIntermediate accepts TLS 1.2 and 1.3; TLS 1.2 is limited to
	// forward-secret AEAD suites (ECDHE with AES-GCM or ChaCha20-Poly1305).
	TLSProfileIntermediate TLSProfile = "intermediate"
	// TLSProfileCompatible accepts TLS 1.2 and 1.3 with Go's default suites,
	// which add the ECDHE AES-CBC suites some older clients need.
	TLSProfileCompatible TLSProfile = "compatible"
	// TLSProfileLegacy also accepts TLS 1.0 and 1.1 (deprecated by RFC 8996),
	// with Go's default suites.
	TLSProfileLegacy TLSProfile = "legacy"
)

// IsValid reports whether p is a known profile.
func (p TLSProfile) IsValid() bool {
	switch p {
	case TLSProfileModern, TLSProfileIntermediate, TLSProfileCompatible, TLSProfileLegacy:
		return true
	}
	return false
}

// MinVersion is the lowest TLS version p accepts.
func (p TLSProfile) MinVersion() uint16 {
	switch p {
	case TLSProfileModern:
		return tls.VersionTLS13
	case TLSProfileLegacy:
		return tls.VersionTLS10
	}
	return tls.VersionTLS12
}

// intermediateSuites are Mozilla's intermediate TLS 1.2 suites that Go
// implements, in Mozilla's order (Go orders them itself).
var intermediateSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// CipherSuites are the TLS 1.0-1.2 suites p offers; nil means Go's defaults.
func (p TLSProfile) CipherSuites() []uint16 {
	if p == TLSProfileIntermediate {
		return append([]uint16(nil), intermediateSuites...)
	}
	return nil
}

// Apply sets config's lowest version and suites for p. An unknown profile
// is treated as intermediate.
func (p TLSProfile) Apply(config *tls.Config) {
	if !p.IsValid() {
		p = TLSProfileIntermediate
	}
	config.MinVersion = p.MinVersion()
	config.CipherSuites = p.CipherSuites()
}

// SetTLSProfile sets the TLS profile and restarts a running proxy.
func (router *Router) SetTLSProfile(p TLSProfile) {
	router.Option.TLSProfile = p
	router.restartIfRunning()
}
