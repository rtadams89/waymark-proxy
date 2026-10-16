package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/csrf"
)

// newCSRFTestServer mirrors the management UI's CSRF setup in main.go
func newCSRFTestServer(t *testing.T, wrap bool) *httptest.Server {
	t.Helper()
	protect := csrf.Protect(
		[]byte("01234567890123456789012345678901"),
		csrf.CookieName("zoraxy_csrf_test"),
		csrf.Secure(false),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteLaxMode),
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, csrf.Token(r))
	})
	mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "saved")
	})
	var h http.Handler = protect(mux)
	if wrap {
		h = markPlaintextAdminRequests(h)
	}
	return httptest.NewServer(h)
}

// post performs a browser-like POST: fetch a token, then POST with it.
func csrfPost(t *testing.T, srv *httptest.Server, origin string, host string, xfp string, withToken bool) int {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	treq, _ := http.NewRequest(http.MethodGet, srv.URL+"/token", nil)
	if host != "" {
		treq.Host = host
	}
	tres, err := client.Do(treq)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := io.ReadAll(tres.Body)
	tres.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/save", strings.NewReader(url.Values{"a": {"b"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if withToken {
		req.Header.Set("X-CSRF-Token", string(token))
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if host != "" {
		req.Host = host
	}
	if xfp != "" {
		req.Header.Set("X-Forwarded-Proto", xfp)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestCSRFPlaintextAdminAccess(t *testing.T) {
	srv := newCSRFTestServer(t, true)
	defer srv.Close()
	direct := srv.URL // same-origin page served over plain http

	cases := []struct {
		name      string
		origin    string
		host      string
		xfp       string
		withToken bool
		want      int
	}{
		{"direct http (incl. SSH tunnel)", direct, "", "", true, http.StatusOK},
		{"behind Zoraxy's own https proxy", "https://zoraxy.example.com", "zoraxy.example.com", "https", true, http.StatusOK},
		{"missing token is still rejected", direct, "", "", false, http.StatusForbidden},
		{"cross-origin over http is rejected", "http://evil.example", "", "", true, http.StatusForbidden},
		{"https proxy with a foreign origin is rejected", "https://evil.example", "zoraxy.example.com", "https", true, http.StatusForbidden},
		{"https proxy origin downgraded to http is rejected", "http://zoraxy.example.com", "zoraxy.example.com", "https", true, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := csrfPost(t, srv, c.origin, c.host, c.xfp, c.withToken); got != c.want {
				t.Errorf("status = %d, want %d", got, c.want)
			}
		})
	}
}

// Documents the regression this wrapper fixes: without it, gorilla/csrf
// v1.7.3 rejects legitimate same-origin POSTs over plain HTTP.
func TestCSRFWithoutWrapperRejectsPlainHTTP(t *testing.T) {
	srv := newCSRFTestServer(t, false)
	defer srv.Close()
	if got := csrfPost(t, srv, srv.URL, "", "", true); got != http.StatusForbidden {
		t.Skipf("gorilla/csrf no longer rejects plain HTTP POSTs (status %d); the wrapper may be unnecessary", got)
	}
}

func TestForwardedAsHTTPS(t *testing.T) {
	for v, want := range map[string]bool{"https": true, "HTTPS": true, "https, http": true, "http": false, "": false, "http, https": false} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if v != "" {
			r.Header.Set("X-Forwarded-Proto", v)
		}
		if got := forwardedAsHTTPS(r); got != want {
			t.Errorf("X-Forwarded-Proto %q: got %v, want %v", v, got, want)
		}
	}
}
