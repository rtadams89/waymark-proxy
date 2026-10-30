package domainsniff

import "testing"

func TestValidHostPort(t *testing.T) {
	cases := map[string]bool{
		"example.com":           true,
		"192.168.1.10:8443":     true,
		"[fe80::1]:443":         true,
		"localhost":             true,
		"":                      false,
		"evil.com@example.com":  false,
		"example.com/path":      false,
		"example.com?x=1":       false,
		"example.com#frag":      false,
		"http://example.com":    false,
		"example.com:443/admin": false,
		" example.com":          false,
	}
	for in, want := range cases {
		if got := validHostPort(in); got != want {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
}
