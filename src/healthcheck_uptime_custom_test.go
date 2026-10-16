package main

import (
	"testing"
	"time"

	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/uptime"
)

func newTestFeed() (*uptimeFeed, *[]uptime.Record, *time.Time) {
	clock := time.Unix(1_700_000_000, 0)
	var got []uptime.Record
	f := &uptimeFeed{
		targets:  map[uptimeFeedKey]uptimeFeedTarget{},
		windows:  map[string]*uptimeWindow{},
		now:      func() time.Time { return clock },
		interval: func() time.Duration { return 300 * time.Second },
		sink:     func(r uptime.Record) { got = append(got, r) },
	}
	f.setTargets(map[uptimeFeedKey]uptimeFeedTarget{
		{"app.test", "10.0.0.2:80"}: {id: "app.test (upstream:1)", name: "app.test (upstream:1)", url: "http://10.0.0.2:80", protocol: "http"},
	})
	return f, &got, &clock
}

func hcTarget(cfgType healthcheck.CheckType) healthcheck.Target {
	cfg := healthcheck.DefaultConfig()
	cfg.ActiveEnabled, cfg.Type, cfg.Path = true, cfgType, "/health"
	return healthcheck.Target{RuleID: "app.test", Upstream: healthcheck.Upstream{Addr: "10.0.0.2:80"}, Config: *cfg}
}

func TestUptimeFeedRollsUpToOneRecordPerInterval(t *testing.T) {
	f, got, clock := newTestFeed()
	tg := hcTarget(healthcheck.CheckHTTPGet)
	ok := healthcheck.ProbeResult{OK: true, StatusCode: 200, Latency: 12 * time.Millisecond}

	f.onCheck(tg, ok, false) // first result recorded immediately
	if len(*got) != 1 || !(*got)[0].Online || (*got)[0].URL != "http://10.0.0.2:80/health" || (*got)[0].Latency != 12 {
		t.Fatalf("first record wrong: %+v", *got)
	}
	for i := 0; i < 29; i++ { // 10s checks for 290s: no new record
		*clock = clock.Add(10 * time.Second)
		f.onCheck(tg, ok, false)
	}
	if len(*got) != 1 {
		t.Fatalf("expected 1 record within the first interval, got %d", len(*got))
	}
	*clock = clock.Add(10 * time.Second) // 300s
	f.onCheck(tg, ok, false)
	if len(*got) != 2 || !(*got)[1].Online || (*got)[1].StatusCode != 200 {
		t.Fatalf("expected a second, online record at 300s: %+v", *got)
	}
}

func TestUptimeFeedMarksWindowDownForAnyOutage(t *testing.T) {
	f, got, clock := newTestFeed()
	tg := hcTarget(healthcheck.CheckHTTPGet)
	ok := healthcheck.ProbeResult{OK: true, StatusCode: 200}
	bad := healthcheck.ProbeResult{OK: false, StatusCode: 503}

	f.onCheck(tg, ok, false)
	*clock = clock.Add(60 * time.Second)
	f.onCheck(tg, bad, true) // disabled for a while...
	*clock = clock.Add(60 * time.Second)
	f.onCheck(tg, ok, false) // ...and back before the window closes
	*clock = clock.Add(180 * time.Second)
	f.onCheck(tg, ok, false)
	last := (*got)[len(*got)-1]
	if len(*got) != 2 || last.Online || last.StatusCode != 503 || last.Latency != 0 {
		t.Fatalf("window with an outage should be offline with the failing code: %+v", *got)
	}

	// Next window is clean again
	*clock = clock.Add(300 * time.Second)
	f.onCheck(tg, ok, false)
	if l := (*got)[len(*got)-1]; !l.Online || l.StatusCode != 200 {
		t.Fatalf("following window should be online: %+v", l)
	}
}

func TestUptimeFeedPassiveOutageReportsNoStatusCode(t *testing.T) {
	f, got, clock := newTestFeed()
	tg := hcTarget(healthcheck.CheckTCP)
	ok := healthcheck.ProbeResult{OK: true}
	f.onCheck(tg, ok, false)
	*clock = clock.Add(300 * time.Second)
	f.onCheck(tg, ok, true) // checks pass, but passive monitoring had it out
	last := (*got)[len(*got)-1]
	if last.Online || last.StatusCode != 0 || last.URL != "http://10.0.0.2:80" {
		t.Fatalf("passive outage should be offline with code 0 and no path: %+v", last)
	}
}

func TestUptimeFeedIgnoresUnmappedAndDropsRemoved(t *testing.T) {
	f, got, _ := newTestFeed()
	other := hcTarget(healthcheck.CheckHTTPGet)
	other.Upstream.Addr = "10.0.0.9:80"
	f.onCheck(other, healthcheck.ProbeResult{OK: true}, false)
	if len(*got) != 0 {
		t.Fatalf("unmapped upstream should not produce records: %+v", *got)
	}
	f.onCheck(hcTarget(healthcheck.CheckHTTPGet), healthcheck.ProbeResult{OK: true}, false)
	f.setTargets(map[uptimeFeedKey]uptimeFeedTarget{})
	if len(f.windows) != 0 {
		t.Error("windows of removed targets should be dropped")
	}
}
