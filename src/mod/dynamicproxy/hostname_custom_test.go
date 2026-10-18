package dynamicproxy

import "testing"

func TestRequestHostname(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":      "example.com",
		"example.com:8443": "example.com",
		"10.0.0.1:80":      "10.0.0.1",
		"[fd00::1]:443":    "fd00::1",
		"[fd00::1]":        "fd00::1",
		"":                 "",
	} {
		if got := requestHostname(in); got != want {
			t.Errorf("requestHostname(%q) = %q, want %q", in, got, want)
		}
	}
}
