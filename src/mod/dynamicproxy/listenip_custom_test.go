package dynamicproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestListenIPRouting(t *testing.T) {
	router := &Router{ProxyEndpoints: &sync.Map{}}
	add := func(ep *ProxyEndpoint) { router.ProxyEndpoints.Store(ep.RootOrMatchingDomain, ep) }
	add(&ProxyEndpoint{RootOrMatchingDomain: "app.example.com", ListenIP: "10.0.0.5"})
	add(&ProxyEndpoint{RootOrMatchingDomain: "*.example.com", ListenIP: "10.0.0.6"})
	add(&ProxyEndpoint{RootOrMatchingDomain: "any.example.com"})

	req := func(host, local string) *ProxyEndpoint {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		if local != "" {
			r = r.WithContext(context.WithValue(r.Context(), httpLocalAddrKey(), &net.TCPAddr{IP: net.ParseIP(local), Port: 443}))
		}
		return router.GetProxyEndpointForRequest(r)
	}
	name := func(ep *ProxyEndpoint) string {
		if ep == nil {
			return "<none>"
		}
		return ep.RootOrMatchingDomain
	}
	cases := []struct{ host, local, want string }{
		{"app.example.com", "10.0.0.5", "app.example.com"},
		{"app.example.com", "::ffff:10.0.0.5", "app.example.com"}, // IPv4 seen on a dual-stack socket
		{"app.example.com", "10.0.0.6", "*.example.com"},          // bound elsewhere: the wildcard on this address answers
		{"app.example.com", "10.0.0.7", "<none>"},
		{"x.example.com", "10.0.0.6", "*.example.com"},
		{"x.example.com", "10.0.0.5", "<none>"},
		{"any.example.com", "10.0.0.7", "any.example.com"},
		{"app.example.com", "", "app.example.com"},        // address not known: answers
		{"app.example.com", "0.0.0.0", "app.example.com"}, // wildcard listener address: not known
	}
	for _, c := range cases {
		if got := name(req(c.host, c.local)); got != c.want {
			t.Errorf("%s on %q: got %s, want %s", c.host, c.local, got, c.want)
		}
	}
	// Host-name-only lookups (certificates, loopback detection) ignore the binding
	if name(router.GetProxyEndpointFromHostname("app.example.com")) != "app.example.com" {
		t.Error("hostname lookup should ignore ListenIP")
	}
}

func httpLocalAddrKey() any { return http.LocalAddrContextKey }
