package main

// Browser security headers for the admin console. They are a second line
// of defence behind the UI's escaping: scripts, styles, frames and form
// posts may only come from the console itself.

import (
	"net/http"
	"strings"
)

// adminCSP is the admin console's Content-Security-Policy. The UI still
// uses inline scripts and onclick handlers, so 'unsafe-inline' is needed
// for scripts; other sites' scripts, plugins (object), framing by other
// sites and posting forms elsewhere are blocked. Images may come from
// https (app store icons are on other sites).
const adminCSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-src 'self'; " +
	"frame-ancestors 'self'; " +
	"form-action 'self'; " +
	"base-uri 'self'; " +
	"object-src 'none'"

// adminSecurityHeaders adds the headers to every admin console response.
// Plugin pages (/plugin.ui/) are the plugins' own and keep their headers,
// apart from nosniff.
func adminSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(r.URL.Path, "/plugin.ui/") {
			h.Set("Content-Security-Policy", adminCSP)
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Referrer-Policy", "same-origin")
		}
		next.ServeHTTP(w, r)
	})
}
