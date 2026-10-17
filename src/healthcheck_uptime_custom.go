package main

/*
	healthcheck_uptime_custom.go (custom fork)

	The Uptime Monitor page shows proxy rules with active health checks, fed
	from those checks. The uptime monitor no longer probes anything itself:
	its target list contains only health-checked upstreams, all marked
	ExternallyMonitored, and it is kept only as the history store behind the
	page. Each upstream's check results are rolled up into one record per
	uptime interval (5 minutes), the spacing the page expects.

	A record is "offline" if the upstream was out of load balancing at any
	point during its window, whether active checks or passive monitoring took
	it out. Failed checks that did not reach the "Disable after" count do not
	mark a window offline.

	Rules without active health checks (including passive-only rules and
	virtual directories) do not appear on the page.

	Also here: the default health check for new rules, and the one-time
	migration of upstream's "Monitor Uptime" setting to health checks.
*/

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/uptime"
)

type uptimeFeedKey struct{ rule, addr string }

type uptimeFeedTarget struct {
	id, name, url, protocol string
}

type uptimeWindow struct {
	start    time.Time
	anyDown  bool
	failCode int // status code of the last failed check while down (0 = no HTTP response)
}

type uptimeFeed struct {
	mu       sync.Mutex
	targets  map[uptimeFeedKey]uptimeFeedTarget
	windows  map[string]*uptimeWindow
	now      func() time.Time
	interval func() time.Duration
	sink     func(uptime.Record)
}

func newUptimeFeed() *uptimeFeed {
	return &uptimeFeed{
		targets: map[uptimeFeedKey]uptimeFeedTarget{},
		windows: map[string]*uptimeWindow{},
		now:     time.Now,
		interval: func() time.Duration {
			if uptimeMonitor != nil {
				return time.Duration(uptimeMonitor.IntervalSeconds()) * time.Second
			}
			return 300 * time.Second
		},
		sink: func(rec uptime.Record) {
			if uptimeMonitor != nil {
				uptimeMonitor.RecordExternal(rec)
			}
		},
	}
}

var healthUptimeFeed = newUptimeFeed()

// healthCheckUptimeTargets builds the uptime monitor's target list: one
// entry per upstream of every enabled rule with valid active health checks,
// using upstream's ID format ("rule", or "rule (upstream:N)" when a rule has
// several upstreams). It also registers those targets with the feed.
func healthCheckUptimeTargets(dp *dynamicproxy.Router) []*uptime.Target {
	targets := []*uptime.Target{}
	fed := map[uptimeFeedKey]uptimeFeedTarget{}
	for ruleID, ep := range dp.GetProxyEndpointsAsMap() {
		if ep.Disabled || ep.HealthCheck == nil || !ep.HealthCheck.ActiveEnabled {
			continue
		}
		if _, err := ep.HealthCheck.Validate(); err != nil {
			continue // health checks ignore invalid settings too
		}
		multiple := len(ep.ActiveOrigins) > 1
		for i, origin := range ep.ActiveOrigins {
			protocol := "http"
			if origin.RequireTLS {
				protocol = "https"
			}
			id := ruleID
			if multiple {
				id = fmt.Sprintf("%s (upstream:%d)", ruleID, i)
			}
			t := &uptime.Target{
				ID:                  id,
				Name:                id,
				URL:                 protocol + "://" + origin.OriginIpOrDomain,
				Protocol:            protocol,
				ProxyType:           uptime.ProxyType_Host,
				SkipTlsValidation:   origin.SkipCertValidations,
				ExternallyMonitored: true,
			}
			targets = append(targets, t)
			fed[uptimeFeedKey{ruleID, origin.OriginIpOrDomain}] = uptimeFeedTarget{id: id, name: id, url: t.URL, protocol: protocol}
		}
	}
	targets = append(targets, streamUptimeTargets(fed)...) // streampool_custom.go
	healthUptimeFeed.setTargets(fed)
	return targets
}

func (f *uptimeFeed) setTargets(targets map[uptimeFeedKey]uptimeFeedTarget) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = targets
	keep := map[string]bool{}
	for _, t := range targets {
		keep[t.id] = true
	}
	for id := range f.windows {
		if !keep[id] {
			delete(f.windows, id)
		}
	}
}

// onCheck is healthcheck.Manager.OnCheck.
func (f *uptimeFeed) onCheck(t healthcheck.Target, res healthcheck.ProbeResult, offline bool) {
	f.mu.Lock()
	info, ok := f.targets[uptimeFeedKey{t.RuleID, t.Upstream.Addr}]
	if !ok {
		f.mu.Unlock()
		return
	}
	now := f.now()
	var emit *uptime.Record
	w, exists := f.windows[info.id]
	if !exists {
		// First result for this upstream: record it straight away so the
		// Uptime Monitor page is not empty for the first interval
		w = &uptimeWindow{start: now}
		f.windows[info.id] = w
		w.observe(res, offline)
		rec := f.record(info, t, res, w, now)
		emit = &rec
		*w = uptimeWindow{start: now}
	} else {
		w.observe(res, offline)
		if now.Sub(w.start) >= f.interval() {
			rec := f.record(info, t, res, w, now)
			emit = &rec
			*w = uptimeWindow{start: now}
		}
	}
	f.mu.Unlock()
	if emit != nil && f.sink != nil {
		f.sink(*emit)
	}
}

func (w *uptimeWindow) observe(res healthcheck.ProbeResult, offline bool) {
	if !offline {
		return
	}
	w.anyDown = true
	if !res.OK {
		w.failCode = res.StatusCode
	}
}

func (f *uptimeFeed) record(info uptimeFeedTarget, t healthcheck.Target, last healthcheck.ProbeResult, w *uptimeWindow, now time.Time) uptime.Record {
	url := info.url
	if t.Config.Type == healthcheck.CheckHTTPGet || t.Config.Type == healthcheck.CheckHTTPHead {
		url = strings.TrimRight(info.url, "/") + t.Config.Path
	}
	rec := uptime.Record{
		Timestamp:  now.Unix(),
		ID:         info.id,
		Name:       info.name,
		URL:        url,
		Protocol:   info.protocol,
		Online:     !w.anyDown,
		StatusCode: last.StatusCode,
		Latency:    last.Latency.Milliseconds(),
	}
	if w.anyDown {
		// The page treats an offline record with a 2xx code as online, so
		// report the failing code (or 0 when there was no HTTP response)
		rec.StatusCode = w.failCode
		rec.Latency = 0
	}
	return rec
}

/* Defaults for new rules, and migration from upstream's uptime monitor */

const (
	migrationTable              = "custom_fork"
	uptimeToHealthCheckMigrated = "uptime_to_healthcheck_migrated"
)

// defaultActiveHealthCheck is used for new rules and for migrated ones:
// HTTP GET every 10s, disabled after 3 failures, re-enabled after 2 passes.
// path is the rule's old uptime monitor health-check URI, if any.
func defaultActiveHealthCheck(path string) *healthcheck.Config {
	cfg := healthcheck.DefaultConfig()
	cfg.ActiveEnabled = true
	cfg.Type = healthcheck.CheckHTTPGet
	cfg.IntervalSeconds, cfg.TimeoutSeconds = 10, 3
	cfg.Fall, cfg.Rise = 3, 2
	cfg.Path = strings.TrimSpace(path)
	if v, err := cfg.Validate(); err == nil {
		return v
	}
	cfg.Path = "/" // unusable old path: fall back to the root
	v, _ := cfg.Validate()
	return v
}

// newRuleHealthCheck is applied when a proxy rule is created. The create
// form's "Health checks" checkbox and path field reuse upstream's enableUtm
// and utmURI parameters.
func newRuleHealthCheck(enabled bool, path string) *healthcheck.Config {
	if !enabled {
		return nil
	}
	return defaultActiveHealthCheck(path)
}

// migrateUptimeMonitorToHealthChecks gives every rule that had upstream's
// "Monitor Uptime" enabled (and no health check settings yet) an equivalent
// active health check, so failover and uptime history continue. It runs once
// per installation.
func migrateUptimeMonitorToHealthChecks() {
	if sysdb == nil || dynamicProxyRouter == nil {
		return
	}
	if !sysdb.TableExists(migrationTable) {
		sysdb.NewTable(migrationTable)
	}
	if sysdb.KeyExists(migrationTable, uptimeToHealthCheckMigrated) {
		return
	}
	endpoints := []*dynamicproxy.ProxyEndpoint{}
	for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
		endpoints = append(endpoints, ep)
	}
	converted := applyUptimeMigration(endpoints, SaveReverseProxyConfig)
	SystemWideLogger.PrintAndLog("healthcheck", fmt.Sprintf("Converted the uptime monitor setting of %d proxy rule(s) to health checks", converted), nil)
	sysdb.Write(migrationTable, uptimeToHealthCheckMigrated, time.Now().Unix())
}

func applyUptimeMigration(endpoints []*dynamicproxy.ProxyEndpoint, save func(*dynamicproxy.ProxyEndpoint) error) int {
	converted := 0
	for _, ep := range endpoints {
		if ep.HealthCheck != nil || ep.DisableUptimeMonitor {
			continue
		}
		ep.HealthCheck = defaultActiveHealthCheck(ep.UptimeMonitorURI)
		if err := save(ep); err != nil {
			SystemWideLogger.PrintAndLog("healthcheck", "Could not save converted health check for rule "+ep.RootOrMatchingDomain, err)
			continue
		}
		converted++
	}
	return converted
}
