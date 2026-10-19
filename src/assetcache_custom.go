package main

/*
	assetcache_custom.go (custom fork)

	Settings and API for the per-rule static file cache (mod/assetcache,
	hooked into the proxy in mod/dynamicproxy/assetcache_custom.go):
	- the shared memory limit, stored in sysdb
	- per-rule settings, stored in the rule's config file
	- stats and purge, used by snippet/assetCache.html
*/

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/assetcache"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	assetCacheLimitKey   = "asset_cache_max_mb"
	assetCacheDailyKey   = "asset_cache_daily"
	assetCacheDefaultMB  = 256
	assetCacheMinLimitMB = 16
	assetCacheMaxLimitMB = 65536
)

// initAssetCache applies the saved memory limit and restores today's
// counters. Called at startup.
func initAssetCache() {
	dynamicproxy.AssetCache.SetMaxBytes(int64(assetCacheLimitMB()) << 20)
	assetCacheDailyOnce.Do(func() {
		if sysdb != nil && sysdb.TableExists(migrationTable) && sysdb.KeyExists(migrationTable, assetCacheDailyKey) {
			saved := []assetcache.DayStats{}
			if err := sysdb.Read(migrationTable, assetCacheDailyKey, &saved); err == nil {
				dynamicproxy.AssetCache.RestoreDaily(saved)
			}
		}
		go func() {
			for range time.Tick(time.Minute) {
				saveAssetCacheDaily()
			}
		}()
	})
}

var assetCacheDailyOnce sync.Once

// saveAssetCacheDaily stores today's and yesterday's counters (for the
// status page) so a restart does not reset them.
func saveAssetCacheDaily() {
	if sysdb == nil {
		return
	}
	if !sysdb.TableExists(migrationTable) {
		sysdb.NewTable(migrationTable)
	}
	today, yesterday := dynamicproxy.AssetCache.Daily()
	sysdb.Write(migrationTable, assetCacheDailyKey, []assetcache.DayStats{today, yesterday})
}

func assetCacheLimitMB() int {
	mb := assetCacheDefaultMB
	if sysdb != nil && sysdb.TableExists(migrationTable) && sysdb.KeyExists(migrationTable, assetCacheLimitKey) {
		var saved int
		if err := sysdb.Read(migrationTable, assetCacheLimitKey, &saved); err == nil && saved >= assetCacheMinLimitMB && saved <= assetCacheMaxLimitMB {
			mb = saved
		}
	}
	return mb
}

// pruneAssetCache drops stored files of rules that were removed or no longer
// cache. Called after every rule change (UpdateUptimeMonitorTargets).
func pruneAssetCache() {
	if dynamicProxyRouter == nil {
		return
	}
	active := map[string]bool{}
	for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
		if ep.AssetCache.On() {
			active[strings.ToLower(ep.RootOrMatchingDomain)] = true
		}
	}
	for _, rule := range dynamicproxy.AssetCache.Rules() {
		if !active[rule] {
			dynamicproxy.AssetCache.Purge(rule)
		}
	}
}

/* API */

// HandleAssetCacheGet returns a rule's cache settings and stats.
// GET /api/proxy/assetcache/get?ep=<rule>
func HandleAssetCacheGet(w http.ResponseWriter, r *http.Request) {
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
	cfg := ep.AssetCache
	if cfg == nil {
		cfg = assetcache.DefaultConfig()
	}
	resp := map[string]any{
		"config":             cfg,
		"defaultExtensions":  assetcache.DefaultExtensions,
		"staticContentTypes": assetcache.StaticContentTypes,
		"stats":              dynamicproxy.AssetCache.Stats(ep.RootOrMatchingDomain),
		"usage":              dynamicproxy.AssetCache.Usage(),
		"limitMB":            assetCacheLimitMB(),
		"zoraxyBasicAuth":    ep.AuthenticationProvider != nil && ep.AuthenticationProvider.AuthMethod == dynamicproxy.AuthMethodBasic,
		"globalNoCache":      dynamicProxyRouter.Option.NoCache,
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleAssetCacheSet saves a rule's cache settings and clears its stored files.
// POST /api/proxy/assetcache/set  ep=<rule>  config=<json>
func HandleAssetCacheSet(w http.ResponseWriter, r *http.Request) {
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
	submitted := assetcache.DefaultConfig()
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
	newEndpoint.AssetCache = cfg
	ready, err := dynamicProxyRouter.PrepareProxyRoute(newEndpoint)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	target.Remove()
	dynamicProxyRouter.AddProxyRouteToRuntime(ready)
	dynamicproxy.AssetCache.Purge(newEndpoint.RootOrMatchingDomain)
	if err := SaveReverseProxyConfig(newEndpoint); err != nil {
		utils.SendErrorResponse(w, "settings applied but could not be saved to disk")
		return
	}
	state := "off"
	if cfg.Enabled {
		state = "on"
	}
	SystemWideLogger.PrintAndLog("assetcache", "Static file caching "+state+" for rule "+newEndpoint.RootOrMatchingDomain, nil)
	utils.SendOK(w)
}

// HandleAssetCachePurge clears a rule's stored files and counters.
// POST /api/proxy/assetcache/purge  ep=<rule>
func HandleAssetCachePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	epName, err := utils.PostPara(r, "ep")
	if err != nil {
		utils.SendErrorResponse(w, "ep not defined")
		return
	}
	ep, err := dynamicProxyRouter.LoadProxy(epName)
	if err != nil {
		utils.SendErrorResponse(w, "proxy rule not found")
		return
	}
	dynamicproxy.AssetCache.Purge(ep.RootOrMatchingDomain)
	SystemWideLogger.PrintAndLog("assetcache", "Cache cleared for rule "+ep.RootOrMatchingDomain, nil)
	utils.SendOK(w)
}

// HandleAssetCacheLimit sets the memory limit shared by all rules.
// POST /api/proxy/assetcache/limit  limitMB=<n>
func HandleAssetCacheLimit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := utils.PostPara(r, "limitMB")
	if err != nil {
		utils.SendErrorResponse(w, "limitMB not defined")
		return
	}
	mb, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || mb < assetCacheMinLimitMB || mb > assetCacheMaxLimitMB {
		utils.SendErrorResponse(w, "memory limit must be a whole number between "+strconv.Itoa(assetCacheMinLimitMB)+" and "+strconv.Itoa(assetCacheMaxLimitMB)+" MB")
		return
	}
	if !sysdb.TableExists(migrationTable) {
		sysdb.NewTable(migrationTable)
	}
	if err := sysdb.Write(migrationTable, assetCacheLimitKey, mb); err != nil {
		utils.SendErrorResponse(w, "could not save the memory limit")
		return
	}
	dynamicproxy.AssetCache.SetMaxBytes(int64(mb) << 20)
	SystemWideLogger.PrintAndLog("assetcache", "Cache memory limit set to "+strconv.Itoa(mb)+" MB", nil)
	utils.SendOK(w)
}

// HandleAssetCacheSummary returns today's and yesterday's cache totals
// across all rules, for the status page.
// GET /api/proxy/assetcache/summary
func HandleAssetCacheSummary(w http.ResponseWriter, r *http.Request) {
	today, yesterday := dynamicproxy.AssetCache.Daily()
	enabled := 0
	if dynamicProxyRouter != nil {
		for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
			if ep.AssetCache.On() && !ep.Disabled {
				enabled++
			}
		}
	}
	js, _ := json.Marshal(map[string]any{
		"today":            today,
		"todayHitRate":     today.HitRate(),
		"yesterday":        yesterday,
		"yesterdayHitRate": yesterday.HitRate(),
		"cachedHosts":      enabled,
		"usage":            dynamicproxy.AssetCache.Usage(),
	})
	utils.SendJSONResponse(w, string(js))
}

// HandleAssetCacheStats returns every rule's cache counters, for the Proxy
// Rules list: {rule: {hits, revalidated, misses, uncacheable, bytesServed,
// entries, bytes, hitRate}} (hitRate -1 when nothing was requested yet).
// GET /api/proxy/assetcache/stats
func HandleAssetCacheStats(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	if dynamicProxyRouter != nil {
		for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
			st := dynamicproxy.AssetCache.Stats(ep.RootOrMatchingDomain)
			day := assetcache.DayStats{Hits: st.Hits, Revalidated: st.Revalidated, Misses: st.Misses, Uncacheable: st.Uncacheable}
			out[ep.RootOrMatchingDomain] = map[string]any{
				"hits": st.Hits, "revalidated": st.Revalidated, "misses": st.Misses, "uncacheable": st.Uncacheable,
				"bytesServed": st.BytesServed, "entries": st.Entries, "bytes": st.Bytes, "hitRate": day.HitRate(),
			}
		}
	}
	js, _ := json.Marshal(out)
	utils.SendJSONResponse(w, string(js))
}
