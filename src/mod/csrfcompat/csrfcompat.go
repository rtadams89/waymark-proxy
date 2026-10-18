// Package csrfcompat replaces github.com/gorilla/csrf with the standard
// library's net/http.CrossOriginProtection (Go 1.25+), behind the small part
// of the gorilla API that Zoraxy uses, so upstream files only need their
// import changed:
//
//	csrf "imuslab.com/zoraxy/mod/csrfcompat"
//
// Why (custom fork): gorilla/csrf has an unpatched advisory affecting all
// versions through v1.7.3 (TrustedOrigins also trusts http:// origins), and
// v1.7.3 rejects same-origin POSTs over plain HTTP unless every request is
// tagged. The advisory recommends migrating to CrossOriginProtection.
//
// How protection works now: unsafe methods (POST, PUT, DELETE, ...) are
// rejected when the browser reports a cross-origin request via the
// Sec-Fetch-Site header (sent by all browsers since 2023), or, for older
// browsers, when the Origin header's host doesn't match the Host header.
// GET, HEAD and OPTIONS are always allowed, as with gorilla/csrf. Requests
// with neither header are not from a browser and are allowed, since CSRF is a
// browser attack. This works the same over HTTP, HTTPS, an SSH tunnel, or
// behind Zoraxy's own reverse proxy.
//
// Tokens are no longer checked. Token still returns a random value because
// the web UI templates it into pages and sends it back; it is harmless.
package csrfcompat

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

// Option is accepted for source compatibility with gorilla/csrf.Protect.
// Cookie options have no effect: no CSRF cookie is set any more.
type Option func(*options)

type options struct{}

// SameSiteMode mirrors gorilla/csrf's type so call sites compile unchanged.
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
// browser requests. authKey and opts are ignored.
func Protect(authKey []byte, opts ...Option) func(http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	return func(h http.Handler) http.Handler {
		return protection.Handler(h)
	}
}

// Token returns a random value for templates and scripts that still expect
// one. It is not checked by Protect.
func Token(r *http.Request) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "unused"
	}
	return base64.RawStdEncoding.EncodeToString(b)
}
