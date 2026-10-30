package main

// API of per-rule maintenance mode, and the status page's upstream health summary.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	maxMaintenanceMessage = 2000
	maxRetryAfterMinutes  = 7 * 24 * 60
)

// HandleMaintenanceGet returns a rule's maintenance settings.
// GET /api/proxy/maintenance/get?ep=<rule>
func HandleMaintenanceGet(w http.ResponseWriter, r *http.Request) {
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
	cfg := ep.Maintenance
	if cfg == nil {
		cfg = &dynamicproxy.MaintenanceConfig{}
	}
	js, _ := json.Marshal(map[string]any{
		"config":         cfg,
		"defaultMessage": dynamicproxy.DefaultMaintenanceMessage,
	})
	utils.SendJSONResponse(w, string(js))
}

// HandleMaintenanceSet turns maintenance mode on or off for a rule.
// POST /api/proxy/maintenance/set  ep=<rule> enabled=true|false message=<text> retryAfterMinutes=<n>
func HandleMaintenanceSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	epName, err := utils.PostPara(r, "ep")
	if err != nil {
		utils.SendErrorResponse(w, "ep not defined")
		return
	}
	enabled, err := utils.PostBool(r, "enabled")
	if err != nil {
		utils.SendErrorResponse(w, "enabled not defined")
		return
	}
	message, _ := utils.PostPara(r, "message")
	message = strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if len(message) > maxMaintenanceMessage {
		utils.SendErrorResponse(w, "message is too long (at most "+strconv.Itoa(maxMaintenanceMessage)+" characters)")
		return
	}
	retry := 0
	if raw, err := utils.PostPara(r, "retryAfterMinutes"); err == nil && strings.TrimSpace(raw) != "" {
		retry, err = strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || retry < 0 || retry > maxRetryAfterMinutes {
			utils.SendErrorResponse(w, "expected duration must be between 0 and "+strconv.Itoa(maxRetryAfterMinutes)+" minutes")
			return
		}
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
	since := time.Now().Unix()
	if target.Maintenance.On() && enabled && target.Maintenance.Since > 0 {
		since = target.Maintenance.Since
	}
	cfg := &dynamicproxy.MaintenanceConfig{Enabled: enabled, Message: message, RetryAfterMinutes: retry}
	if enabled {
		cfg.Since = since
	}
	newEndpoint.Maintenance = cfg
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
	if enabled != target.Maintenance.On() {
		state := "off"
		if enabled {
			state = "on"
		}
		SystemWideLogger.PrintAndLog("maintenance", "Maintenance mode "+state+" for rule "+newEndpoint.RootOrMatchingDomain, nil)
	}
	utils.SendOK(w)
}

// HandleMaintenancePreview renders a rule's maintenance page with the given
// or saved settings.
// GET /api/proxy/maintenance/preview?ep=<rule>[&message=..&retryAfterMinutes=..]
func HandleMaintenancePreview(w http.ResponseWriter, r *http.Request) {
	epName, err := utils.GetPara(r, "ep")
	if err != nil {
		http.Error(w, "ep not defined", http.StatusBadRequest)
		return
	}
	ep, err := dynamicProxyRouter.LoadProxy(epName)
	if err != nil {
		http.Error(w, "proxy rule not found", http.StatusNotFound)
		return
	}
	cfg := &dynamicproxy.MaintenanceConfig{Enabled: true}
	if ep.Maintenance != nil {
		*cfg = *ep.Maintenance
	}
	if msg, err := utils.GetPara(r, "message"); err == nil {
		cfg.Message = msg
	}
	if raw, err := utils.GetPara(r, "retryAfterMinutes"); err == nil {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= maxRetryAfterMinutes {
			cfg.RetryAfterMinutes = n
		}
	}
	if !cfg.Enabled || cfg.Since == 0 {
		cfg.Since = time.Now().Unix()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(dynamicProxyRouter.RenderMaintenancePage(ep.RootOrMatchingDomain, cfg))
}

type maintenanceEntry struct {
	Rule              string `json:"rule"`
	Since             int64  `json:"since"`
	Message           string `json:"message"`
	RetryAfterMinutes int    `json:"retryAfterMinutes"`
}

// HandleUpstreamHealthSummary returns every health-checked upstream and the
// rules in maintenance mode.
// GET /api/proxy/healthcheck/summary
func HandleUpstreamHealthSummary(w http.ResponseWriter, r *http.Request) {
	statuses := []healthcheck.Status{}
	if healthCheckManager != nil {
		statuses = healthCheckManager.Statuses("")
	}
	for i := range statuses {
		if strings.HasPrefix(statuses[i].RuleID, "l4:") {
			statuses[i].RuleID = l4RuleDisplay(statuses[i].RuleID)
			statuses[i].Label = ""
		}
	}
	maintenance := []maintenanceEntry{}
	checkedRules := countL4CheckedRules()
	if dynamicProxyRouter != nil {
		for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
			if ep.Disabled {
				continue
			}
			if ep.HealthCheck.Enabled() {
				checkedRules++
			}
			if ep.Maintenance.On() {
				maintenance = append(maintenance, maintenanceEntry{
					Rule:              ep.RootOrMatchingDomain,
					Since:             ep.Maintenance.Since,
					Message:           ep.Maintenance.Message,
					RetryAfterMinutes: ep.Maintenance.RetryAfterMinutes,
				})
			}
		}
	}
	sort.Slice(maintenance, func(i, j int) bool { return maintenance[i].Rule < maintenance[j].Rule })
	js, _ := json.Marshal(map[string]any{
		"statuses":     statuses,
		"maintenance":  maintenance,
		"checkedRules": checkedRules,
	})
	utils.SendJSONResponse(w, string(js))
}
