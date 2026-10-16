package main

/*
	healthcheck_uptime_custom.go (custom fork)

	Rules with active health checks feed the Uptime Monitor from those checks
	instead of being probed a second time by the uptime monitor's own 5 minute
	check. Their uptime targets are marked ExternallyMonitored, and each
	upstream's check results are rolled up into one uptime record per uptime
	interval (5 minutes), the spacing the Uptime Monitor page expects.

	A record is "offline" if the upstream was out of load balancing at any
	point during its window, whether active checks or passive monitoring took
	it out. Failed checks that did not reach the "Disable after" count do not
	mark a window offline.

	Rules with passive monitoring only have nothing to probe with, so the
	uptime monitor keeps checking them as before.
*/

import (
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

// markHealthCheckedUptimeTargets is applied to the uptime monitor's target
// list every time it is rebuilt. Upstreams of rules with valid active health
// checks are marked ExternallyMonitored and registered with the feed.
func markHealthCheckedUptimeTargets(dp *dynamicproxy.Router, targets []*uptime.Target) []*uptime.Target {
	hosts := dp.GetProxyEndpointsAsMap()
	fed := map[uptimeFeedKey]uptimeFeedTarget{}
	for _, t := range targets {
		if t.ProxyType != uptime.ProxyType_Host {
			continue // virtual directories are not health checked
		}
		ruleID := t.ID
		if i := strings.Index(ruleID, " (upstream:"); i >= 0 {
			ruleID = ruleID[:i]
		}
		ep, ok := hosts[ruleID]
		if !ok || ep.HealthCheck == nil || !ep.HealthCheck.ActiveEnabled {
			continue
		}
		if _, err := ep.HealthCheck.Validate(); err != nil {
			continue // health checks ignore invalid settings, so keep the uptime probe
		}
		addr := strings.TrimPrefix(strings.TrimPrefix(t.URL, "https://"), "http://")
		t.ExternallyMonitored = true
		fed[uptimeFeedKey{ruleID, addr}] = uptimeFeedTarget{id: t.ID, name: t.Name, url: t.URL, protocol: t.Protocol}
	}
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
