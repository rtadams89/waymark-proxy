package main

import (
	"net/http"
	"strings"

	"github.com/gorilla/csrf"
)

/*
	csrf_plaintext.go (custom fork)

	gorilla/csrf v1.7.3 fixed GO-2025-3607 by treating every request as
	HTTPS unless the application marks it as plaintext. For HTTPS requests
	it requires the browser's Origin (or Referer) to be https://<Host>.

	Zoraxy's management UI is served over plain HTTP (directly, through an
	SSH tunnel, or behind Zoraxy's own TLS proxy), so without this wrapper
	every POST made over http:// is rejected with 403.

	A request is marked plaintext only when it did not arrive over TLS and
	no proxy in front of it reported an https client connection. Requests
	coming through Zoraxy's own HTTPS proxy carry X-Forwarded-Proto: https
	(set by dpcore, overwriting anything the client sent) and keep the strict
	https Origin/Referer checks. The CSRF token itself is always required.
*/

func markPlaintextAdminRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil && !forwardedAsHTTPS(r) {
			r = csrf.PlaintextHTTPRequest(r)
		}
		next.ServeHTTP(w, r)
	})
}

func forwardedAsHTTPS(r *http.Request) bool {
	// X-Forwarded-Proto may be a comma separated list when proxies chain;
	// the first entry is the client-facing connection.
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
