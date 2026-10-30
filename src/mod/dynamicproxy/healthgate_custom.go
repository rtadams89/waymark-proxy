package dynamicproxy

// Health check gating: with health checks on, a rule whose upstreams are all
// down answers with the 521 error page, and auto fallback is always on.

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

// autoFallbackOff reports whether auto fallback is off (only without health checks).
func autoFallbackOff(target *ProxyEndpoint) bool {
	return target.DisableAutoFallback && !target.HealthCheck.Enabled()
}

// vdirDown reports whether health checks took a virtual directory's backend
// out of service.
func (h *ProxyHandler) vdirDown(target *VirtualDirectoryEndpoint) bool {
	rule := target.parent
	if rule == nil || !rule.HealthCheck.Enabled() || !rule.HealthCheck.IncludeVirtualDirs || h.Parent.loadBalancer == nil {
		return false
	}
	return !h.Parent.loadBalancer.IsTargetOnline(target.Domain)
}
