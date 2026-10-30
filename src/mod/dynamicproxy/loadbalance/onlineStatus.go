package loadbalance

import (
	"strconv"
	"strings"
)

// Return if the target host is online
func (m *RouteManager) IsTargetOnline(upstreamIP string) bool {
	value, ok := m.OnlineStatus.Load(upstreamIP)
	if !ok {
		// Assume online if not found, also update the map
		m.OnlineStatus.Store(upstreamIP, true)
		return true
	}

	isOnline, ok := value.(bool)
	return ok && isOnline
}

// NotifyHostOnlineState records the online state of a host.
func (m *RouteManager) NotifyHostOnlineState(upstreamIP string, isOnline bool) {
	//if the upstream IP contains http or https, strip it
	upstreamIP = strings.TrimPrefix(upstreamIP, "http://")
	upstreamIP = strings.TrimPrefix(upstreamIP, "https://")

	if m.IsTargetOnline(upstreamIP) == isOnline {
		return
	}

	m.OnlineStatus.Store(upstreamIP, isOnline)
	m.println("Updating destination "+upstreamIP+" online state to "+strconv.FormatBool(isOnline), nil)
}

// FilterOfflineOrigins return only online origins from a list of origins
// If originalUpstreamCount is 1 (only one upstream), or disableAutoFallback is true, return all origins unchanged
func (m *RouteManager) FilterOfflineOrigins(origins []*Upstream, originalUpstreamCount int, disableAutoFallback bool) []*Upstream {
	if originalUpstreamCount == 1 {
		return origins
	}

	if disableAutoFallback {
		return origins
	}

	var onlineOrigins []*Upstream
	for _, origin := range origins {
		if m.IsTargetOnline(origin.OriginIpOrDomain) {
			onlineOrigins = append(onlineOrigins, origin)
		}
	}

	return onlineOrigins
}
