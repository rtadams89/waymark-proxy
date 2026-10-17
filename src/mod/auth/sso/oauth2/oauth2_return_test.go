package oauth2

import (
	"net/url"
	"testing"
)

func TestOAuth2ReturnLocation(t *testing.T) {
	host := "app.example.com"
	cases := map[string]string{
		url.QueryEscape("https://app.example.com/dash?x=1"):            "/dash?x=1",
		url.QueryEscape("http://APP.example.com/"):                     "/",
		url.QueryEscape("https://app.example.com//evil.example.net/x"): "/evil.example.net/x",
		url.QueryEscape("https://evil.example.net/"):                   "/",
		url.QueryEscape("https://app.example.com.evil/"):               "/",
		url.QueryEscape("https://user@app.example.com/"):               "/",
		url.QueryEscape("javascript:alert(1)"):                         "/",
		url.QueryEscape("//evil.example.net/"):                         "/",
		url.QueryEscape("/\\evil.example.net/"):                        "/evil.example.net/",
		url.QueryEscape("/dash"):                                       "/dash",
		"/internal/" + url.QueryEscape("https://app.example.com/a"):    "/a",
		"":           "/",
		"%zz":        "/",
		"relative/x": "/",
	}
	for state, want := range cases {
		if got := oauth2ReturnLocation(state, host); got != want {
			t.Errorf("state %q: got %q, want %q", state, got, want)
		}
	}
}
