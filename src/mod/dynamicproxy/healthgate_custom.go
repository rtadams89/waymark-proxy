package dynamicproxy

/*
	healthgate_custom.go (custom fork)

	On rules with health checks, the checks decide which upstreams receive
	traffic, whatever the number of upstreams:
	- if every upstream is down, visitors get the 521 error page at once
	  instead of waiting on a dead server (upstream Zoraxy always keeps
	  forwarding to a rule's only upstream)
	- the legacy "disable auto fallback" option is ignored, so a failed
	  upstream is always taken out of the pool
	Rules without health checks behave exactly as upstream Zoraxy.
*/

// allUpstreamsDown reports whether health checks have taken every upstream
// of this rule out of load balancing.
func (h *ProxyHandler) allUpstreamsDown(target *ProxyEndpoint) bool {
	if !target.HealthCheck.Enabled() || len(target.ActiveOrigins) == 0 || h.Parent.loadBalancer == nil {
		return false
	}
	for _, origin := range target.ActiveOrigins {
		if h.Parent.loadBalancer.IsTargetOnline(origin.OriginIpOrDomain) {
			return false
		}
	}
	return true
}

// autoFallbackOff is the disableAutoFallback value for load balancing.
func autoFallbackOff(target *ProxyEndpoint) bool {
	return target.DisableAutoFallback && !target.HealthCheck.Enabled()
}
