package dynamicproxy

/*
	listenip_custom.go (custom fork)

	An HTTP rule can answer only on one address of this host, typically a
	floating IP of the HA cluster (ProxyEndpoint.ListenIP). The HTTP(S)
	listener still listens on every address; a request that arrived on
	another address is routed as if the rule did not exist (another
	matching rule, or the default site).

	This is routing, not access control: use an access rule to limit who
	can reach a site. When the address a request arrived on is not known
	(for example over HTTP/3, whose listener reports only its wildcard
	address), the rule answers. Additional listening addresses of a rule
	are used as they are written.
*/

import (
	"net"
	"net/http"
)

// requestLocalIP returns the address of this host the request arrived on,
// or nil when it is not known.
func requestLocalIP(r *http.Request) net.IP {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return nil
	}
	var ip net.IP
	switch v := a.(type) {
	case *net.TCPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	default:
		host, _, err := net.SplitHostPort(a.String())
		if err != nil {
			return nil
		}
		ip = net.ParseIP(host)
	}
	if ip == nil || ip.IsUnspecified() {
		return nil
	}
	return ip
}

// answersOn reports whether the rule answers a request that arrived on
// local (nil: not known).
func (ep *ProxyEndpoint) answersOn(local net.IP) bool {
	if ep == nil || ep.ListenIP == "" || local == nil {
		return true
	}
	ip := net.ParseIP(ep.ListenIP)
	return ip != nil && ip.Equal(local)
}

// GetProxyEndpointForRequest finds the rule for a request on the HTTP(S)
// listener, by its host name and the address it arrived on.
func (router *Router) GetProxyEndpointForRequest(r *http.Request) *ProxyEndpoint {
	return router.getProxyEndpoint(requestHostname(r.Host), requestLocalIP(r))
}
