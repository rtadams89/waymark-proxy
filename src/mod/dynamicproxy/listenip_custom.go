package dynamicproxy

/*
	A rule's ListenIP: the rule answers only requests that arrived on that
	address, or whose arrival address is not known; other requests are
	routed as if the rule did not exist.
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
// local (nil when not known).
func (ep *ProxyEndpoint) answersOn(local net.IP) bool {
	if ep == nil || ep.ListenIP == "" || local == nil {
		return true
	}
	ip := net.ParseIP(ep.ListenIP)
	return ip != nil && ip.Equal(local)
}

// GetProxyEndpointForRequest returns the rule for a request by host name and
// arrival address.
func (router *Router) GetProxyEndpointForRequest(r *http.Request) *ProxyEndpoint {
	return router.getProxyEndpoint(requestHostname(r.Host), requestLocalIP(r))
}
