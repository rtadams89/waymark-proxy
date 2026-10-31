package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminSecurityHeaders(t *testing.T) {
	h := adminSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'self'", "object-src 'none'", "form-action 'self'", "base-uri 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src *") {
		t.Errorf("CSP allows too much: %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("missing headers: %v", rec.Header())
	}

	// Plugin pages keep their own policy
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/plugin.ui/abc/", nil))
	if rec.Header().Get("Content-Security-Policy") != "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("plugin page headers: %v", rec.Header())
	}
}
