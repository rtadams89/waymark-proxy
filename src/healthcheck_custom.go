package main

/*
	healthcheck_custom.go (custom fork)

	Wires mod/healthcheck into Zoraxy:
	- builds health check targets from proxy rules that enable it
	- feeds proxied request outcomes to passive monitoring
	- stops the legacy uptime monitor from toggling upstreams that these
	  health checks manage; rules with active checks also feed the Uptime
	  Monitor page from them (see healthcheck_uptime_custom.go)
	- serves the API used by snippet/healthChecks.html
*/

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/dynamicproxy/dpcore"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/utils"
)

var healthCheckManager *healthcheck.Manager

// initHealthChecks must run after loadBalancer is created and before the
// uptime monitor starts.
func initHealthChecks() {
	if healthCheckManager != nil {
		healthCheckManager.Stop()
	}
	healthCheckManager = healthcheck.NewManager(
		notifyUpstreamOnline, // HTTP load balancer and stream proxies (streampool_custom.go)
		func(msg string) { SystemWideLogger.PrintAndLog("healthcheck", msg, nil) },
	)
	healthCheckManager.OnCheck = healthUptimeFeed.onCheck
}

// refreshHealthCheckTargets rebuilds the managed set from the current proxy
// rules. Called from UpdateUptimeMonitorTargets, which runs after every rule
// change, and once at startup.
func refreshHealthCheckTargets() {
	if healthCheckManager == nil || dynamicProxyRouter == nil {
		return
	}
	targets := []healthcheck.Target{}
	for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
		if ep.Disabled || !ep.HealthCheck.Enabled() {
			continue
		}
		cfg, err := ep.HealthCheck.Validate()
		if err != nil {
			SystemWideLogger.PrintAndLog("healthcheck", "Ignoring invalid health check settings on rule "+ep.RootOrMatchingDomain, err)
			continue
		}
		ruleID := strings.ToLower(ep.RootOrMatchingDomain)
		seen := map[string]bool{}
		for _, origin := range ep.ActiveOrigins {
			seen[origin.OriginIpOrDomain] = true
			targets = append(targets, healthcheck.Target{
				RuleID: ruleID,
				Upstream: healthcheck.Upstream{
					Addr:          origin.OriginIpOrDomain,
					RequireTLS:    origin.RequireTLS,
					SkipTLSVerify: origin.SkipCertValidations,
					TLSServerName: healthCheckTLSName(ep.RootOrMatchingDomain, origin),
				},
				Config: *cfg,
			})
		}
		// Virtual directory backends, checked with the same settings
		if cfg.IncludeVirtualDirs {
			for _, vdir := range ep.VirtualDirectories {
				if vdir.Disabled || seen[vdir.Domain] {
					continue
				}
				seen[vdir.Domain] = true
				targets = append(targets, healthcheck.Target{
					RuleID: ruleID,
					Upstream: healthcheck.Upstream{
						Addr:          vdir.Domain,
						RequireTLS:    vdir.RequireTLS,
						SkipTLSVerify: vdir.SkipCertValidations,
					},
					Config: *cfg,
					Label:  "vdir " + vdir.MatchingPath,
				})
			}
		}
	}
	targets = append(targets, streamHealthTargets()...) // streampool_custom.go
	healthCheckManager.SetTargets(targets)
}

// observeUpstreamResult is RouterOption.UpstreamResultObserver.
func observeUpstreamResult(ruleID string, upstreamAddr string, statusCode int, err error) {
	if healthCheckManager != nil {
		healthCheckManager.ObserveResult(ruleID, upstreamAddr, statusCode, err)
	}
}

// uptimeNotifyRespectingHealthChecks is the uptime monitor's
// OnlineStateNotify. Upstreams managed by health checks are left to them,
// so the two never fight. (The name length matches the upstream value it
// replaces in reverseproxy.go, which keeps gofmt alignment unchanged.)
func uptimeNotifyRespectingHealthChecks(upstreamURL string, online bool) {
	if healthCheckManager != nil && healthCheckManager.Manages(upstreamURL) {
		return
	}
	loadBalancer.NotifyHostOnlineState(upstreamURL, online)
}

/* API */

// HandleHealthCheckGet returns a rule's settings, live status and recent events.
// GET /api/proxy/healthcheck/get?ep=<rule>
func HandleHealthCheckGet(w http.ResponseWriter, r *http.Request) {
	epName, err := utils.GetPara(r, "ep")
	if err != nil {
		utils.SendErrorResponse(w, "ep not defined")
		return
	}
	ep, err := dynamicProxyRouter.LoadProxy(epName)
	if err != nil {
		utils.SendErrorResponse(w, "proxy rule not found")
		return
	}
	cfg := ep.HealthCheck
	if cfg == nil {
		cfg = healthcheck.DefaultConfig()
	}
	upstreams := []string{}
	for _, o := range ep.ActiveOrigins {
		upstreams = append(upstreams, o.OriginIpOrDomain)
	}
	vdirs := []map[string]string{}
	for _, v := range ep.VirtualDirectories {
		if !v.Disabled {
			vdirs = append(vdirs, map[string]string{"path": v.MatchingPath, "upstream": v.Domain})
		}
	}
	resp := map[string]any{
		"config":    cfg,
		"upstreams": upstreams,
		"vdirs":     vdirs,
		"disabled":  ep.Disabled,
		"statuses":  []healthcheck.Status{},
		"events":    []healthcheck.Event{},
	}
	if healthCheckManager != nil {
		resp["statuses"] = healthCheckManager.Statuses(ep.RootOrMatchingDomain)
		resp["events"] = healthCheckManager.Events(ep.RootOrMatchingDomain, 50)
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleHealthCheckSet saves a rule's health check settings.
// POST /api/proxy/healthcheck/set  ep=<rule>  config=<json>
func HandleHealthCheckSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	epName, err := utils.PostPara(r, "ep")
	if err != nil {
		utils.SendErrorResponse(w, "ep not defined")
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
	cfg, err := submitted.Validate()
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	target, err := dynamicProxyRouter.LoadProxy(epName)
	if err != nil {
		utils.SendErrorResponse(w, "proxy rule not found")
		return
	}
	newEndpoint := dynamicproxy.CopyEndpoint(target)
	if newEndpoint == nil {
		utils.SendErrorResponse(w, "failed to copy proxy rule")
		return
	}
	newEndpoint.HealthCheck = cfg
	ready, err := dynamicProxyRouter.PrepareProxyRoute(newEndpoint)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	target.Remove()
	dynamicProxyRouter.AddProxyRouteToRuntime(ready)
	if err := SaveReverseProxyConfig(newEndpoint); err != nil {
		utils.SendErrorResponse(w, "settings applied but could not be saved to disk")
		return
	}
	SystemWideLogger.PrintAndLog("healthcheck", "Health check settings updated for rule "+newEndpoint.RootOrMatchingDomain, nil)
	UpdateUptimeMonitorTargets() // also refreshes health check targets
	utils.SendOK(w)
}

// HandleHealthCheckOverview returns every managed upstream, for all rules.
// GET /api/proxy/healthcheck/status
func HandleHealthCheckOverview(w http.ResponseWriter, r *http.Request) {
	statuses := []healthcheck.Status{}
	if healthCheckManager != nil {
		statuses = healthCheckManager.Statuses("")
	}
	js, _ := json.Marshal(statuses)
	utils.SendJSONResponse(w, string(js))
}

// healthCheckTLSName is the certificate name health checks use for an
// upstream (custom fork), following its "check certificate name against"
// setting: the rule's host name for "proxyhost" (unless it is a wildcard),
// the address's host for "upstream", otherwise the default.
func healthCheckTLSName(ruleHost string, origin *loadbalance.Upstream) string {
	switch origin.TLSVerifyName {
	case dpcore.TLSNameProxyHost:
		if !strings.ContainsAny(ruleHost, "*?[") {
			return ruleHost
		}
	case dpcore.TLSNameUpstream:
		host := strings.TrimPrefix(strings.TrimPrefix(origin.OriginIpOrDomain, "https://"), "http://")
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		return host
	}
	return ""
}
