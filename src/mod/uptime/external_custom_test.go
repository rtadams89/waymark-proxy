package uptime

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExternallyMonitoredTargetIsNotProbed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	var notified atomic.Int32
	m, err := NewUptimeMonitor(&Config{
		Targets: []*Target{
			{ID: "ext", Name: "ext", URL: srv.URL, Protocol: "http", ExternallyMonitored: true},
		},
		Interval:          300,
		MaxRecordsStore:   3,
		OnlineStateNotify: func(string, bool) { notified.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	m.ExecuteUptimeCheck()
	m.shadowPingTargets()

	if hits.Load() != 0 || notified.Load() != 0 {
		t.Fatalf("externally monitored target was probed (hits=%d, notifications=%d)", hits.Load(), notified.Load())
	}

	for i := 0; i < 5; i++ {
		m.RecordExternal(Record{ID: "ext", Timestamp: int64(i), Online: true})
	}
	m.CleanRecords() // must keep history for targets still in the list
	recs := m.OnlineStatusLog["ext"]
	if len(recs) != 3 || recs[0].Timestamp != 2 || recs[2].Timestamp != 4 {
		t.Fatalf("records not appended and trimmed to MaxRecordsStore: %+v", recs)
	}
	if m.IntervalSeconds() != 300 {
		t.Errorf("IntervalSeconds = %d", m.IntervalSeconds())
	}
}
