package oauth2

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCheckOAuth2State(t *testing.T) {
	nonce := newOAuth2StateNonce()
	if len(nonce) != 32 || nonce == newOAuth2StateNonce() {
		t.Fatalf("bad nonce %q", nonce)
	}
	ret := url.QueryEscape("https://app.example.com/dash")
	req := func(cookie string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/internal/oauth2?code=x", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "z-state", Value: cookie})
		}
		return r
	}
	if got, ok := checkOAuth2State(nonce+"."+ret, req(nonce), "z-state"); !ok || got != ret {
		t.Fatalf("matching state refused: %q %v", got, ok)
	}
	bad := []struct{ state, cookie string }{
		{nonce + "." + ret, ""},
		{nonce + "." + ret, newOAuth2StateNonce()},
		{ret, nonce},
		{"short." + ret, "short"},
	}
	for _, b := range bad {
		if _, ok := checkOAuth2State(b.state, req(b.cookie), "z-state"); ok {
			t.Errorf("state %q with cookie %q accepted", b.state, b.cookie)
		}
	}
	if got := oauth2ReturnLocation(ret, "app.example.com"); got != "/dash" {
		t.Errorf("return location: %q", got)
	}
}
