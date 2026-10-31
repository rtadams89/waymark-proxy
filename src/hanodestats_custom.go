package main

// Key figures of this node for the status page's cluster panel. They are
// sent to the other nodes with the HA status (haSyncPeerInfo), so each node
// can show every node's figures.

import (
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/info/usageinfo"
)

// haNodeStats are one node's figures; counts are since local midnight.
type haNodeStats struct {
	ProxyRunning      bool    `json:"proxyRunning"`
	RequestsToday     int64   `json:"requestsToday"`
	ErrorsToday       int64   `json:"errorsToday"`
	BandwidthToday    int64   `json:"bandwidthToday"`    // bytes in and out
	ActiveConnections int     `json:"activeConnections"` // -1 if unknown
	CPU               float64 `json:"cpu"`               // percent
	RAM               float64 `json:"ram"`               // percent
	ResourcesReady    bool    `json:"resourcesReady"`    // false until CPU and RAM are sampled
	Collected         int64   `json:"collected"`         // Unix seconds
}

const haNodeStatsMaxAge = 5 * time.Second

var haNodeStatsCache struct {
	mu    sync.Mutex
	stats *haNodeStats
	at    time.Time
}

// haCurrentNodeStats returns this node's figures, gathered at most every
// few seconds (counting connections reads the whole TCP table).
func haCurrentNodeStats() *haNodeStats {
	haNodeStatsCache.mu.Lock()
	defer haNodeStatsCache.mu.Unlock()
	if haNodeStatsCache.stats != nil && time.Since(haNodeStatsCache.at) < haNodeStatsMaxAge {
		return haNodeStatsCache.stats
	}
	s := &haNodeStats{Collected: time.Now().Unix(), ActiveConnections: -1}
	s.ProxyRunning = dynamicProxyRouter != nil && dynamicProxyRouter.Running
	if statisticCollector != nil {
		total, errs, _ := statisticCollector.Summary().Counts()
		s.RequestsToday, s.ErrorsToday = total, errs
	}
	rx, tx := getBandwidthToday()
	s.BandwidthToday = rx + tx
	s.ActiveConnections = countActiveProxyConnections()
	cpu, _, _, ram, ready := usageinfo.GetCachedStats()
	s.CPU, s.RAM, s.ResourcesReady = cpu, ram, ready
	haNodeStatsCache.stats, haNodeStatsCache.at = s, time.Now()
	return s
}
