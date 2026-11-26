// Package csrfcompat provides gorilla/csrf's Protect and Token API on top of
// net/http.CrossOriginProtection:
//
//	csrf "github.com/rtadams89/waymark-proxy/mod/csrfcompat"
//
// Unsafe methods are rejected when Sec-Fetch-Site reports a cross-origin
// request, or when the Origin host does not match the Host header. GET, HEAD
// and OPTIONS, and requests with neither header, are allowed.
package csrfcompat

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

// Option is gorilla/csrf.Protect's option type.
type Option func(*options)

type options struct{}

// SameSiteMode mirrors gorilla/csrf's type.
type SameSiteMode int

const (
	SameSiteDefaultMode SameSiteMode = iota + 1
	SameSiteLaxMode
	SameSiteStrictMode
	SameSiteNoneMode
)

func CookieName(string) Option     { return func(*options) {} }
func Secure(bool) Option           { return func(*options) {} }
func Path(string) Option           { return func(*options) {} }
func SameSite(SameSiteMode) Option { return func(*options) {} }

// Protect returns middleware that rejects cross-origin, state-changing
// browser requests.
func Protect(authKey []byte, opts ...Option) func(http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	return func(h http.Handler) http.Handler {
		return protection.Handler(h)
	}
}

// Token returns a random value.
func Token(r *http.Request) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "unused"
	}
	return base64.RawStdEncoding.EncodeToString(b)
}
