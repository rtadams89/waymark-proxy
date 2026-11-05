package main

// Security headers for the admin console.

import (
	"net/http"
	"strings"
)

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

// adminSecurityHeaders sets nosniff on every response and the other headers
// outside /plugin.ui/.
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
