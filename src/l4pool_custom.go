package main

/*
	Health checks of Layer 4 rule upstreams, under the rule ID "l4:<uuid>",
	shared with HTTP proxy rules: targets, verdicts for both load balancers,
	passive monitoring of failed connections, and uptime history.
*/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/l4proxy"
	"imuslab.com/zoraxy/mod/uptime"
	"imuslab.com/zoraxy/mod/utils"
)

// notifyUpstreamOnline applies a health check verdict to the HTTP load
// balancer and the Layer 4 proxies.
func notifyUpstreamOnline(addr string, online bool) {
	if loadBalancer != nil {
		loadBalancer.NotifyHostOnlineState(addr, online)
	}
	if l4ProxyManager != nil {
		l4ProxyManager.SetUpstreamOnline(addr, online)
	}
}

func observeL4Connect(ruleID string, addr string, err error) {
	if healthCheckManager != nil {
		healthCheckManager.ObserveResult(ruleID, addr, 0, err)
	}
}

// l4RuleName returns a Layer 4 rule's display name.
func l4RuleName(c l4proxy.InstanceView) string {
	if strings.TrimSpace(c.Name) != "" {
		return c.Name
	}
	return c.ListeningAddress
}

// l4HealthTargets returns the health check targets of running Layer 4
// rules.
func l4HealthTargets() []healthcheck.Target {
	targets := []healthcheck.Target{}
	if l4ProxyManager == nil {
		return targets
	}
	for _, rule := range l4ProxyManager.List() {
		c := rule.View()
		if !c.Running || !c.HealthCheck.Enabled() {
			continue
		}
		cfg, err := c.HealthCheck.Validate()
		if err != nil {
			SystemWideLogger.PrintAndLog("healthcheck", "Ignoring invalid health check settings on Layer 4 rule "+l4RuleName(c), err)
			continue
		}
		for _, addr := range rule.UpstreamList() {
			targets = append(targets, healthcheck.Target{
				RuleID:   rule.HealthRuleID(),
				Upstream: healthcheck.Upstream{Addr: addr},
				Config:   *cfg,
				RuleName: "Layer 4: " + l4RuleName(c),
			})
		}
	}
	return targets
}

// l4RuleDisplay maps "l4:<uuid>" rule IDs to "Layer 4: <name>" and leaves
// other IDs unchanged.
func l4RuleDisplay(ruleID string) string {
	if !strings.HasPrefix(ruleID, "l4:") || l4ProxyManager == nil {
		return ruleID
	}
	for _, rule := range l4ProxyManager.List() {
		if rule.HealthRuleID() == ruleID {
			return "Layer 4: " + l4RuleName(rule.View())
		}
	}
	return ruleID
}

// countL4CheckedRules counts running Layer 4 rules with health checks.
func countL4CheckedRules() int {
	n := 0
	if l4ProxyManager != nil {
		for _, rule := range l4ProxyManager.List() {
			if c := rule.View(); c.Running && c.HealthCheck.Enabled() {
				n++
			}
		}
	}
	return n
}

// l4UptimeTargets returns uptime targets for Layer 4 rules with active
// checks, registering them in fed.
func l4UptimeTargets(fed map[uptimeFeedKey]uptimeFeedTarget) []*uptime.Target {
	targets := []*uptime.Target{}
	if l4ProxyManager == nil {
		return targets
	}
	for _, rule := range l4ProxyManager.List() {
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
			id := "Layer 4: " + l4RuleName(c)
			if len(ups) > 1 {
				id = fmt.Sprintf("%s (destination %d)", id, i)
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

// l4DefaultHealthCheck returns the default check for a rule without health
// checks: TCP connect, or ping for UDP-only rules.
func l4DefaultHealthCheck(c l4proxy.InstanceView) *healthcheck.Config {
	cfg := healthcheck.DefaultConfig()
	cfg.Type = healthcheck.CheckTCP
	if !c.UseTCP {
		cfg.Type = healthcheck.CheckICMP
	}
	cfg.CountServerErrors = false
	return cfg
}

// HandleL4HealthCheckGet returns a Layer 4 rule's health check settings,
// live status and recent events.
// GET /api/l4proxy/healthcheck/get?uuid=<rule>
func HandleL4HealthCheckGet(w http.ResponseWriter, r *http.Request) {
	id, err := utils.GetPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "uuid not defined")
		return
	}
	rule, err := l4ProxyManager.GetConfigByUUID(id)
	if err != nil {
		utils.SendErrorResponse(w, "Layer 4 rule not found")
		return
	}
	c := rule.View()
	cfg := c.HealthCheck
	if cfg == nil {
		cfg = l4DefaultHealthCheck(c)
	}
	upstreams := rule.UpstreamList()
	if upstreams == nil {
		upstreams = []string{}
	}
	resp := map[string]any{
		"config":    cfg,
		"upstreams": upstreams,
		"vdirs":     []map[string]string{},
		"disabled":  !c.Running,
		"name":      l4RuleName(c),
		"useTCP":    c.UseTCP,
		"useUDP":    c.UseUDP,
		"statuses":  []healthcheck.Status{},
		"events":    []healthcheck.Event{},
	}
	if healthCheckManager != nil {
		resp["statuses"] = healthCheckManager.Statuses(rule.HealthRuleID())
		resp["events"] = healthCheckManager.Events(rule.HealthRuleID(), 50)
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleL4HealthCheckSet saves a Layer 4 rule's health check settings.
// POST /api/l4proxy/healthcheck/set  uuid=<rule>  config=<json>
func HandleL4HealthCheckSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := utils.PostPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "uuid not defined")
		return
	}
	raw, err := utils.PostPara(r, "config")
	if err != nil {
		utils.SendErrorResponse(w, "config not defined")
		return
	}
	submitted := healthcheck.DefaultConfig()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(submitted); err != nil {
		utils.SendErrorResponse(w, "invalid config: "+err.Error())
		return
	}
	rule, err := l4ProxyManager.GetConfigByUUID(id)
	if err != nil {
		utils.SendErrorResponse(w, "Layer 4 rule not found")
		return
	}
	c := rule.View()
	pool := &l4proxy.PoolSettings{
		Destinations:        rule.DestinationList(),
		LoadBalancing:       c.LoadBalancing,
		StickySessions:      c.StickySessions,
		HealthCheck:         submitted,
		CloseOnUpstreamDown: c.CloseOnUpstreamDown,
	}
	if err := l4ProxyManager.ApplyPoolSettings(id, pool); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	SystemWideLogger.PrintAndLog("healthcheck", "Health check settings updated for Layer 4 rule "+l4RuleName(c), nil)
	utils.SendOK(w)
}
