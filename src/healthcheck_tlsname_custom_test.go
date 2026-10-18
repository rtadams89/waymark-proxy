package main

import (
	"testing"

	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
)

func TestHealthCheckTLSName(t *testing.T) {
	cases := []struct {
		rule, addr, mode, want string
	}{
		{"app.example.com", "10.0.0.5:8443", "", ""},
		{"app.example.com", "10.0.0.5:8443", "proxyhost", "app.example.com"},
		{"*.example.com", "10.0.0.5:8443", "proxyhost", ""},
		{"app.example.com", "backend.lan:8443", "upstream", "backend.lan"},
		{"app.example.com", "backend.lan/sub", "upstream", "backend.lan"},
		{"app.example.com", "10.0.0.5", "upstream", "10.0.0.5"},
	}
	for _, c := range cases {
		got := healthCheckTLSName(c.rule, &loadbalance.Upstream{OriginIpOrDomain: c.addr, TLSVerifyName: c.mode})
		if got != c.want {
			t.Errorf("healthCheckTLSName(%q, %q, %q) = %q, want %q", c.rule, c.addr, c.mode, got, c.want)
		}
	}
}
