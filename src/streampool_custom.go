package main

/*
	streampool_custom.go (custom fork)

	Health checks for stream proxy upstream pools (mod/streamproxy
	pool_custom.go). Stream rules share the health check manager with HTTP
	proxy rules, under the rule ID "stream:<uuid>":
	- every running stream rule with health checks contributes one target per
	  upstream (refreshHealthCheckTargets)
	- the manager's verdicts go to both the HTTP load balancer and the stream
	  proxy manager (an upstream address is taken out wherever it is used)
	- failed upstream connections feed passive monitoring
	- rules with active checks appear on Health & Uptime
*/

import (
	"fmt"
	"strings"

	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/streamproxy"
	"imuslab.com/zoraxy/mod/uptime"
)

// notifyUpstreamOnline is the health check manager's notifier.
func notifyUpstreamOnline(addr string, online bool) {
	if loadBalancer != nil {
		loadBalancer.NotifyHostOnlineState(addr, online)
	}
	if streamProxyManager != nil {
		streamProxyManager.SetUpstreamOnline(addr, online)
	}
}

// observeStreamConnect is streamproxy.Options.ObserveConnect.
func observeStreamConnect(ruleID string, addr string, err error) {
	if healthCheckManager != nil {
		healthCheckManager.ObserveResult(ruleID, addr, 0, err)
	}
}

// streamRuleName is how a stream rule is shown next to its upstreams.
func streamRuleName(c streamproxy.InstanceView) string {
	if strings.TrimSpace(c.Name) != "" {
		return c.Name
	}
	return c.ListeningAddress
}

// streamHealthTargets returns the health check targets of running stream
// rules.
func streamHealthTargets() []healthcheck.Target {
	targets := []healthcheck.Target{}
	if streamProxyManager == nil {
		return targets
	}
	for _, rule := range streamProxyManager.List() {
		c := rule.View()
		if !c.Running || !c.HealthCheck.Enabled() {
			continue
		}
		cfg, err := c.HealthCheck.Validate()
		if err != nil {
			SystemWideLogger.PrintAndLog("healthcheck", "Ignoring invalid health check settings on stream proxy "+streamRuleName(c), err)
			continue
		}
		for _, addr := range rule.UpstreamList() {
			targets = append(targets, healthcheck.Target{
				RuleID:   rule.HealthRuleID(),
				Upstream: healthcheck.Upstream{Addr: addr},
				Config:   *cfg,
				Label:    "stream proxy " + streamRuleName(c),
			})
		}
	}
	return targets
}

// streamRuleDisplay maps "stream:<uuid>" rule IDs to "Stream: <name>" for
// the UI, and leaves other IDs alone.
func streamRuleDisplay(ruleID string) string {
	if !strings.HasPrefix(ruleID, "stream:") || streamProxyManager == nil {
		return ruleID
	}
	for _, rule := range streamProxyManager.List() {
		if rule.HealthRuleID() == ruleID {
			return "Stream: " + streamRuleName(rule.View())
		}
	}
	return ruleID
}

// countStreamCheckedRules counts running stream rules with health checks.
func countStreamCheckedRules() int {
	n := 0
	if streamProxyManager != nil {
		for _, rule := range streamProxyManager.List() {
			if c := rule.View(); c.Running && c.HealthCheck.Enabled() {
				n++
			}
		}
	}
	return n
}

// streamUptimeTargets adds stream rules with active checks to Health &
// Uptime, registering them with the feed in fed.
func streamUptimeTargets(fed map[uptimeFeedKey]uptimeFeedTarget) []*uptime.Target {
	targets := []*uptime.Target{}
	if streamProxyManager == nil {
		return targets
	}
	for _, rule := range streamProxyManager.List() {
		c := rule.View()
		if !c.Running || c.HealthCheck == nil || !c.HealthCheck.ActiveEnabled {
			continue
		}
		if _, err := c.HealthCheck.Validate(); err != nil {
			continue
		}
		protocol := "tcp"
		switch {
		case c.UseTCP && c.UseUDP:
			protocol = "tcp+udp"
		case c.UseUDP:
			protocol = "udp"
		}
		ups := rule.UpstreamList()
		for i, addr := range ups {
			id := "Stream: " + streamRuleName(c)
			if len(ups) > 1 {
				id = fmt.Sprintf("%s (upstream:%d)", id, i)
			}
			t := &uptime.Target{
				ID:                  id,
				Name:                id,
				URL:                 protocol + "://" + addr,
				Protocol:            protocol,
				ProxyType:           uptime.ProxyType_Host,
				ExternallyMonitored: true,
			}
			targets = append(targets, t)
			fed[uptimeFeedKey{rule.HealthRuleID(), addr}] = uptimeFeedTarget{id: id, name: id, url: t.URL, protocol: protocol}
		}
	}
	return targets
}
