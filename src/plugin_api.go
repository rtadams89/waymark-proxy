package main

import (
	"net/http"

	"imuslab.com/zoraxy/mod/auth"
	"imuslab.com/zoraxy/mod/netstat"
)

// REST API endpoints for plugin use

// Register the APIs for HTTP proxy management functions
func RegisterHTTPProxyRestAPI(authMiddleware *auth.PluginAuthMiddleware) {
	/* Reverse Proxy Settings & Status */
	authMiddleware.HandleFunc("/api/proxy/status", ReverseProxyStatus)
	authMiddleware.HandleFunc("/api/proxy/list", ReverseProxyList)
	authMiddleware.HandleFunc("/api/proxy/listTags", ReverseProxyListTags)
	authMiddleware.HandleFunc("/api/proxy/detail", ReverseProxyListDetail)
	/* Reverse proxy upstream (load balance) */
	authMiddleware.HandleFunc("/api/proxy/upstream/list", ReverseProxyUpstreamList)
	authMiddleware.HandleFunc("/api/proxy/upstream/add", ReverseProxyUpstreamAdd)
	authMiddleware.HandleFunc("/api/proxy/upstream/setPriority", ReverseProxyUpstreamSetPriority)
	authMiddleware.HandleFunc("/api/proxy/upstream/update", ReverseProxyUpstreamUpdate)
	authMiddleware.HandleFunc("/api/proxy/upstream/remove", ReverseProxyUpstreamDelete)
	/* Reverse proxy virtual directory */
	authMiddleware.HandleFunc("/api/proxy/vdir/list", ReverseProxyListVdir)
	/* Reverse proxy user-defined header */
	authMiddleware.HandleFunc("/api/proxy/header/list", HandleCustomHeaderList)
	/* Reverse proxy auth related */
	authMiddleware.HandleFunc("/api/proxy/auth/exceptions/list", ListProxyBasicAuthExceptionPaths)
	/* ZorxAuth SSO per-endpoint exception rules */
	authMiddleware.HandleFunc("/api/proxy/auth/zorxauth/exceptions/list", ListProxyZorxAuthExceptionRules)
}

// Register the APIs for redirection rules management functions
func RegisterRedirectionRestAPI(authRouter *auth.PluginAuthMiddleware) {
	authRouter.HandleFunc("/api/redirect/list", handleListRedirectionRules)
}

// Register the APIs for access rules management functions
func RegisterAccessRuleRestAPI(authRouter *auth.PluginAuthMiddleware) {
	/* ACLs (read only) */
	authRouter.HandleFunc("/api/acl/list", HandleACLList)
	authRouter.HandleFunc("/api/acl/get", HandleACLGet)
	authRouter.HandleFunc("/api/acl/objects", HandleACLObjects)
	/* Quick Ban List */
	authRouter.HandleFunc("/api/quickban/list", handleListQuickBan)
}

// Register the APIs for path blocking rules management functions
func RegisterPathRuleRestAPI(authRouter *auth.PluginAuthMiddleware) {
	authRouter.HandleFunc("/api/pathrule/list", pathRuleHandler.HandleListBlockingPath)
}

// Register the APIs statistic anlysis and uptime monitoring functions
func RegisterStatisticalRestAPI(authRouter *auth.PluginAuthMiddleware) {
	/* Traffic Summary */
	authRouter.HandleFunc("/api/stats/summary", statisticCollector.HandleTodayStatLoad)
	authRouter.HandleFunc("/api/stats/countries", HandleCountryDistrSummary)
	authRouter.HandleFunc("/api/stats/netstat", netstatBuffers.HandleGetNetworkInterfaceStats)
	authRouter.HandleFunc("/api/stats/netstatgraph", netstatBuffers.HandleGetBufferedNetworkInterfaceStats)
	authRouter.HandleFunc("/api/stats/listnic", netstat.HandleListNetworkInterfaces)
	/* Zoraxy Analytic */
	authRouter.HandleFunc("/api/analytic/list", AnalyticLoader.HandleSummaryList)
	authRouter.HandleFunc("/api/analytic/load", AnalyticLoader.HandleLoadTargetDaySummary)
	authRouter.HandleFunc("/api/analytic/loadRange", AnalyticLoader.HandleLoadTargetRangeSummary)
	authRouter.HandleFunc("/api/analytic/exportRange", AnalyticLoader.HandleRangeExport)
	authRouter.HandleFunc("/api/analytic/resetRange", AnalyticLoader.HandleRangeReset)
	authRouter.HandleFunc("/api/analytic/resetAll", AnalyticLoader.HandleResetAllStats)
	/* UpTime Monitor */
	authRouter.HandleFunc("/api/utm/list", HandleUptimeMonitorListing)
}

// Register the APIs for Layer 4 (TCP / UDP) Proxy management functions
func RegisterL4ProxyRestAPI(authRouter *auth.PluginAuthMiddleware) {
	authRouter.HandleFunc("/api/l4proxy/config/list", l4ProxyManager.HandleListConfigs)
	authRouter.HandleFunc("/api/l4proxy/config/status", l4ProxyManager.HandleGetProxyStatus)
	// Paths from before the rename to "Layer 4"
	authRouter.HandleFunc("/api/streamprox/config/list", l4ProxyManager.HandleListConfigs)
	authRouter.HandleFunc("/api/streamprox/config/status", l4ProxyManager.HandleGetProxyStatus)
}

// Register the APIs for mDNS service management functions
func RegisterMDNSRestAPI(authRouter *auth.PluginAuthMiddleware) {
	authRouter.HandleFunc("/api/mdns/list", HandleMdnsListing)
}

// Register the APIs for Static Web Server management functions
func RegisterStaticWebServerRestAPI(authRouter *auth.PluginAuthMiddleware) {
	/* Static Web Server Controls */
	authRouter.HandleFunc("/api/webserv/status", staticWebServer.HandleGetStatus)

}

func RegisterPluginRestAPI(authRouter *auth.PluginAuthMiddleware) {
	authRouter.HandleFunc("/api/plugins/list", pluginManager.HandleListPlugins)
	authRouter.HandleFunc("/api/plugins/info", pluginManager.HandlePluginInfo)

	authRouter.HandleFunc("/api/plugins/groups/list", pluginManager.HandleListPluginGroups)

	authRouter.HandleFunc("/api/plugins/store/list", pluginManager.HandleListDownloadablePlugins)

	// Full path: /plugin/event/emit
	authRouter.HandleFunc("/event/emit", pluginManager.HandleEmitCustomEvent)
}

/* Register all the APIs */
func initRestAPI(targetMux *http.ServeMux) {
	authMiddleware := auth.NewPluginAuthMiddleware(
		auth.PluginMiddlewareOptions{
			TargetMux:     targetMux,
			ApiKeyManager: pluginApiKeyManager,
			DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "401 - Unauthorized", http.StatusUnauthorized)
			},
		},
	)

	//Register the APIs
	RegisterHTTPProxyRestAPI(authMiddleware)
	RegisterRedirectionRestAPI(authMiddleware)
	RegisterAccessRuleRestAPI(authMiddleware)
	RegisterPathRuleRestAPI(authMiddleware)
	RegisterStatisticalRestAPI(authMiddleware)
	RegisterL4ProxyRestAPI(authMiddleware)
	RegisterMDNSRestAPI(authMiddleware)
	RegisterStaticWebServerRestAPI(authMiddleware)
	RegisterPluginRestAPI(authMiddleware)
}
