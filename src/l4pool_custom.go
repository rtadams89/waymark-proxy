package main

/*
	l4pool_custom.go (custom fork)

	Health checks for Layer 4 proxy upstream pools (mod/l4proxy
	pool_custom.go). Layer 4 rules share the health check manager with HTTP
	proxy rules, under the rule ID "l4:<uuid>":
	- every running Layer 4 rule with health checks contributes one target per
	  upstream (refreshHealthCheckTargets)
	- the manager's verdicts go to both the HTTP load balancer and the Layer 4
	  proxy manager (an upstream address is taken out wherever it is used)
	- failed upstream connections feed passive monitoring
	- rules with active checks appear on Health & Uptime
*/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/l4proxy"
	"imuslab.com/zoraxy/mod/uptime"
	"imuslab.com/zoraxy/mod/utils"
)

// notifyUpstreamOnline is the health check manager's notifier.
func notifyUpstreamOnline(addr string, online bool) {
	if loadBalancer != nil {
		loadBalancer.NotifyHostOnlineState(addr, online)
	}
	if l4ProxyManager != nil {
		l4ProxyManager.SetUpstreamOnline(addr, online)
	}
}

// observeL4Connect is l4proxy.Options.ObserveConnect.
func observeL4Connect(ruleID string, addr string, err error) {
	if healthCheckManager != nil {
		healthCheckManager.ObserveResult(ruleID, addr, 0, err)
	}
}

// l4RuleName is how a Layer 4 rule is shown next to its upstreams.
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

// l4RuleDisplay maps "l4:<uuid>" rule IDs to "Layer 4: <name>" for
// the UI, and leaves other IDs alone.
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

// l4UptimeTargets adds Layer 4 rules with active checks to Health &
// Uptime, registering them with the feed in fed.
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

// migrateL4ProxyConfigFolder moves the rule files from conf/streamproxy (the
// folder's name before stream proxies were renamed Layer 4 proxies) to
// conf/l4proxy, once. A restored older backup is moved the same way.
func migrateL4ProxyConfigFolder() {
	old := filepath.Join(CONF_FOLDER, "streamproxy")
	if _, err := os.Stat(old); err != nil {
		return
	}
	if _, err := os.Stat(CONF_L4_PROXY); err == nil {
		//Both exist (e.g. a backup restored over a newer setup): move rule
		//files that are not in the new folder yet, keep the rest
		entries, _ := os.ReadDir(old)
		for _, e := range entries {
			dst := filepath.Join(CONF_L4_PROXY, e.Name())
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				os.Rename(filepath.Join(old, e.Name()), dst)
			}
		}
		if rest, _ := os.ReadDir(old); len(rest) == 0 {
			os.Remove(old)
		}
		return
	}
	if err := os.Rename(old, CONF_L4_PROXY); err != nil {
		SystemWideLogger.PrintAndLog("l4-proxy", "Could not move "+old+" to "+CONF_L4_PROXY, err)
		return
	}
	SystemWideLogger.PrintAndLog("l4-proxy", "Moved Layer 4 proxy rules from "+old+" to "+CONF_L4_PROXY, nil)
}

/* API: the health check side panel (snippet/healthChecks.html) for a Layer 4 rule */

// l4DefaultHealthCheck is what the health check panel shows for a rule
// without health checks: a TCP connect check, or ping for UDP-only rules.
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
// live status and recent events, in the shape the HTTP rule version uses.
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

// HandleL4HealthCheckSet saves a Layer 4 rule's health check settings and
// keeps its destinations and load balancing.
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
		Upstreams:           rule.UpstreamList(),
		LoadBalancing:       c.LoadBalancing,
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
