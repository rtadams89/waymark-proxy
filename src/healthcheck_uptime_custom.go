package main

/*
	Uptime history from active health checks: each upstream's results are
	rolled up into one record per 5-minute interval, offline if the upstream
	was out of load balancing at any point in it. Also the default health
	check for new rules.
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

// healthCheckUptimeTargets returns one uptime target per upstream of every
// enabled rule with active health checks, and registers them with the feed.
func healthCheckUptimeTargets(dp *dynamicproxy.Router) []*uptime.Target {
	targets := []*uptime.Target{}
	fed := map[uptimeFeedKey]uptimeFeedTarget{}
	for ruleID, ep := range dp.GetProxyEndpointsAsMap() {
		if ep.Disabled || ep.HealthCheck == nil || !ep.HealthCheck.ActiveEnabled {
			continue
		}
		if _, err := ep.HealthCheck.Validate(); err != nil {
			continue
		}
		multiple := len(ep.ActiveOrigins) > 1
		for i, origin := range ep.ActiveOrigins {
			protocol := "http"
			if origin.RequireTLS {
				protocol = "https"
			}
			id := ruleID
			if multiple {
				id = fmt.Sprintf("%s (destination %d)", ruleID, i)
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
	targets = append(targets, l4UptimeTargets(fed)...)
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
		// Records the first result immediately
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
		// Offline records carry the failing code (0 for no HTTP response)
		rec.StatusCode = w.failCode
		rec.Latency = 0
	}
	return rec
}

// customForkTable is the sys.db table for the sticky-session key and the
// asset cache's daily counters.
const customForkTable = "custom_fork"

// defaultActiveHealthCheck returns the health check for new rules: HTTP GET
// of path every 10s, disabled after 3 failures, re-enabled after 2 passes.
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
	cfg.Path = "/" // invalid path: use the root
	v, _ := cfg.Validate()
	return v
}

// newRuleHealthCheck returns the health check of a new proxy rule.
func newRuleHealthCheck(enabled bool, path string) *healthcheck.Config {
	if !enabled {
		return nil
	}
	return defaultActiveHealthCheck(path)
}
