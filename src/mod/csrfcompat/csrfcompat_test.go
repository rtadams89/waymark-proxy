package csrfcompat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newProtected returns a handler wrapped like in main.go.
func newProtected() http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	return Protect([]byte("ignored"), CookieName("x"), Secure(false), Path("/"), SameSite(SameSiteLaxMode))(ok)
}

func TestCrossOriginProtection(t *testing.T) {
	h := newProtected()
	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		want    int
	}{
		// Modern browsers (Sec-Fetch-Site present)
		{"same-origin POST over plain http", "POST", "192.168.1.50:8000", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://192.168.1.50:8000"}, 200},
		{"same-origin POST via SSH tunnel", "POST", "localhost:8000", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://localhost:8000"}, 200},
		{"same-origin POST behind Zoraxy https proxy", "POST", "zoraxy.example.com", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://zoraxy.example.com"}, 200},
		{"cross-site POST rejected", "POST", "192.168.1.50:8000", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"same-site (other subdomain) POST rejected", "POST", "zoraxy.example.com", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://app.example.com"}, 403},
		{"cross-site DELETE rejected", "DELETE", "192.168.1.50:8000", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		// Older browsers (Origin only)
		{"old browser same host allowed", "POST", "192.168.1.50:8000", map[string]string{"Origin": "http://192.168.1.50:8000"}, 200},
		{"old browser foreign origin rejected", "POST", "192.168.1.50:8000", map[string]string{"Origin": "http://evil.example"}, 403},
		// Safe methods and non-browser clients
		{"cross-site GET allowed (safe method)", "GET", "192.168.1.50:8000", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"non-browser POST allowed", "POST", "192.168.1.50:8000", nil, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "http://"+c.host+"/api/x", strings.NewReader("a=b"))
			r.Host = c.host
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestTokenIsRandom(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	a, b := Token(r), Token(r)
	if a == "" || a == b {
		t.Errorf("Token should return distinct non-empty values, got %q and %q", a, b)
	}
}
