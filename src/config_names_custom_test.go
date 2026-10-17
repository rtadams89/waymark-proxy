package main

import "testing"

func TestValidProxyConfigName(t *testing.T) {
	for _, ok := range []string{"example.com", "*.example.com", "a.example.com:8443", "10.0.0.5"} {
		if !validProxyConfigName(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", `a\b`, "a..b", "x\x00y"} {
		if validProxyConfigName(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestValidHostRuleName(t *testing.T) {
	for _, ok := range []string{"example.com", "*.example.com", "a?.example.com", "a.example.com:8443", "[fd00::1]:443", "my_host"} {
		if !validHostRuleName(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", "a b", "<b>x</b>", `a"b`, "a'b", "a/b", "a..b", "x&y"} {
		if validHostRuleName(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
