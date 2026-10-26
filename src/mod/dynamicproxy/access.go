package dynamicproxy

/*
	access.go: access control for HTTP proxy rules.

	Custom fork: each proxy rule has one ACL (AccessFilterUUID; empty means
	Allow all), evaluated top down for the client's address
	(mod/access/engine.go). Deny answers with the forbidden page (403),
	drop closes the connection without an answer (HTTP/1.1) or resets the
	stream (HTTP/2, HTTP/3). Refused requests are logged with the ACL and
	the rule that matched.

	The client's address is the connection's own, or with the rule's
	TrustProxyHeaders the forwarded one when the connection comes from a
	trusted proxy (Access > Trusted Proxies).
*/

import (
	"net/http"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/eventsystem"
	"imuslab.com/zoraxy/mod/netutils"
	"imuslab.com/zoraxy/mod/plugins/zoraxy_plugin/events"
)

// StatusDropped is the status logged for a request an ACL dropped (no
// response was sent; the number nginx uses for the same).
const StatusDropped = 444

// handleAccessRouting applies the rule's ACL; true means the request was
// refused and handled.
func (h *ProxyHandler) handleAccessRouting(w http.ResponseWriter, r *http.Request, sep *ProxyEndpoint) bool {
	return h.Parent.accessRefused(w, r, sep)
}

func (router *Router) accessRefused(w http.ResponseWriter, r *http.Request, sep *ProxyEndpoint) bool {
	if router.Option == nil || router.Option.AccessController == nil || sep == nil {
		return false
	}
	clientIP := router.GetClientIPForEndpoint(r, sep)
	d := router.Option.AccessController.Evaluate(sep.AccessFilterUUID, clientIP)
	if d.Action == access.ActionAllow {
		return false
	}

	eventsystem.Publisher.Emit(&events.BlacklistedIPBlockedEvent{
		IP:           clientIP,
		Comment:      "ACL " + d.RuleLabel() + " (" + d.Action + ")",
		RequestedURL: r.URL.String(),
		Hostname:     r.Host,
		UserAgent:    r.Header.Get("User-Agent"),
		Method:       r.Method,
	})

	if d.Action == access.ActionDrop {
		router.logRequest(r, false, StatusDropped, "acl-drop "+d.RuleLabel(), r.Host, "", sep)
		dropConnection(w)
		return true
	}
	router.logRequest(r, false, http.StatusForbidden, "acl-deny "+d.RuleLabel(), r.Host, "", sep)
	serveProxyRequestError(w, http.StatusForbidden, router, ErrorTemplateForbidden)
	return true
}

// dropConnection ends a request without a response.
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	// HTTP/2 and HTTP/3: abort the handler, which resets the stream
	panic(http.ErrAbortHandler)
}

// GetClientIPForEndpoint resolves the real client IP of a request for a
// proxy rule. Header supplied addresses (X-Real-IP, CF-Connecting-IP,
// Fastly-Client-IP, X-Forwarded-For) are only honored when the rule trusts
// them and the connection comes from a trusted proxy.
func (router *Router) GetClientIPForEndpoint(r *http.Request, pe *ProxyEndpoint) string {
	trust := pe != nil && pe.TrustProxyHeaders
	if router.Option != nil && router.Option.AccessController != nil {
		if ip := router.Option.AccessController.ClientIP(r, trust); ip != "" {
			return ip
		}
	}
	return netutils.GetRequesterIPUntrusted(r)
}
