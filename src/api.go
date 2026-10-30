package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"

	"imuslab.com/zoraxy/mod/acme/acmedns"
	"imuslab.com/zoraxy/mod/acme/acmewizard"
	"imuslab.com/zoraxy/mod/auth"
	"imuslab.com/zoraxy/mod/dynamicproxy/domainsniff"
	"imuslab.com/zoraxy/mod/utils"
)

// API handlers of the web management interface.

func RegisterHTTPProxyAPIs(authRouter *auth.RouterDef) {
	/* Reverse Proxy Settings & Status */
	authRouter.HandleFunc("/api/proxy/enable", ReverseProxyHandleOnOff)
	authRouter.HandleFunc("/api/proxy/add", ReverseProxyHandleAddEndpoint)
	authRouter.HandleFunc("/api/proxy/status", ReverseProxyStatus)
	authRouter.HandleFunc("/api/proxy/toggle", ReverseProxyToggleRuleSet)
	authRouter.HandleFunc("/api/proxy/list", ReverseProxyList)
	authRouter.HandleFunc("/api/proxy/listTags", ReverseProxyListTags)
	authRouter.HandleFunc("/api/proxy/detail", ReverseProxyListDetail)
	authRouter.HandleFunc("/api/proxy/edit", ReverseProxyHandleEditEndpoint)
	authRouter.HandleFunc("/api/proxy/setTags", ReverseProxyHandleSetTags)
	authRouter.HandleFunc("/api/proxy/setAlias", ReverseProxyHandleAlias)
	authRouter.HandleFunc("/api/proxy/setTlsConfig", ReverseProxyHandleSetTlsConfig)
	authRouter.HandleFunc("/api/proxy/setHostname", ReverseProxyHandleSetHostname)
	authRouter.HandleFunc("/api/proxy/del", DeleteProxyEndpoint)
	authRouter.HandleFunc("/api/proxy/updateCredentials", UpdateProxyBasicAuthCredentials)
	authRouter.HandleFunc("/api/proxy/listeningPorts/get", HandleGetListeningPorts)
	authRouter.HandleFunc("/api/proxy/listeningPorts/set", HandleSetListeningPorts)
	authRouter.HandleFunc("/api/proxy/listeningPorts/list", HandleListSecondaryListeners)
	authRouter.HandleFunc("/api/proxy/tlscheck", domainsniff.HandleCheckSiteSupportTLS)
	authRouter.HandleFunc("/api/proxy/setIncoming", HandleIncomingPortSet)
	authRouter.HandleFunc("/api/proxy/useHttpsRedirect", HandleUpdateHttpsRedirect)
	authRouter.HandleFunc("/api/proxy/listenPort80", HandleUpdatePort80Listener)
	authRouter.HandleFunc("/api/proxy/requestIsProxied", HandleManagementProxyCheck)
	authRouter.HandleFunc("/api/proxy/adminrule", HandleAdminRule)
	authRouter.HandleFunc("/api/ha/dnsguess", HandleDNSGuess)
	authRouter.HandleFunc("/api/proxy/developmentMode", HandleDevelopmentModeChange)
	authRouter.HandleFunc("/api/proxy/proxyProtocol", HandleProxyProtocolChange)
	authRouter.HandleFunc("/api/proxy/quic", HandleQuicToggle)
	authRouter.HandleFunc("/api/proxy/timeouts", HandleGlobalProxyTimeoutSettings)
	/* Reverse proxy upstream (load balance) */
	authRouter.HandleFunc("/api/proxy/upstream/list", ReverseProxyUpstreamList)
	authRouter.HandleFunc("/api/proxy/upstream/add", ReverseProxyUpstreamAdd)
	authRouter.HandleFunc("/api/proxy/upstream/update", ReverseProxyUpstreamUpdate)
	authRouter.HandleFunc("/api/proxy/upstream/remove", ReverseProxyUpstreamDelete)
	authRouter.HandleFunc("/api/proxy/healthcheck/get", HandleHealthCheckGet)
	authRouter.HandleFunc("/api/proxy/healthcheck/set", HandleHealthCheckSet)
	authRouter.HandleFunc("/api/proxy/healthcheck/status", HandleHealthCheckOverview)
	authRouter.HandleFunc("/api/proxy/healthcheck/summary", HandleUpstreamHealthSummary)
	authRouter.HandleFunc("/api/proxy/maintenance/get", HandleMaintenanceGet)
	authRouter.HandleFunc("/api/proxy/maintenance/set", HandleMaintenanceSet)
	authRouter.HandleFunc("/api/proxy/maintenance/preview", HandleMaintenancePreview)
	authRouter.HandleFunc("/api/info/brand", HandleBrandInfo)
	authRouter.HandleFunc("/api/ha/status", HandleHAStatus)
	authRouter.HandleFunc("/api/ha/config", HandleHAConfig)
	authRouter.HandleFunc("/api/ha/apply", HandleHAApply)
	authRouter.HandleFunc("/api/ha/installer", HandleHAInstallerFile)
	authRouter.HandleFunc("/api/ha/sync/now", HandleHASyncNow)
	authRouter.HandleFunc("/api/ha/cluster", HandleHACluster)
	authRouter.HandleFunc("/api/ha/switchover", HandleHASwitchover)
	authRouter.HandleFunc("/api/update/status", HandleUpdateStatus)
	authRouter.HandleFunc("/api/update/check", HandleUpdateCheck)
	authRouter.HandleFunc("/api/update/install", HandleUpdateInstall)
	authRouter.HandleFunc("/api/update/rollback", HandleUpdateRollback)
	authRouter.HandleFunc("/api/update/peer", HandleUpdatePeer)
	authRouter.HandleFunc("/api/update/rolling", HandleRollingUpdate)
	authRouter.HandleFunc("/api/proxy/assetcache/get", HandleAssetCacheGet)
	authRouter.HandleFunc("/api/proxy/assetcache/set", HandleAssetCacheSet)
	authRouter.HandleFunc("/api/proxy/assetcache/purge", HandleAssetCachePurge)
	authRouter.HandleFunc("/api/proxy/assetcache/limit", HandleAssetCacheLimit)
	authRouter.HandleFunc("/api/proxy/assetcache/summary", HandleAssetCacheSummary)
	authRouter.HandleFunc("/api/proxy/assetcache/stats", HandleAssetCacheStats)
	authRouter.HandleFunc("/api/proxy/listenip/options", HandleProxyListenIPOptions)
	authRouter.HandleFunc("/api/proxy/listenip/set", HandleProxyListenIPSet)
	/* Reverse proxy virtual directory */
	authRouter.HandleFunc("/api/proxy/vdir/list", ReverseProxyListVdir)
	authRouter.HandleFunc("/api/proxy/vdir/add", ReverseProxyAddVdir)
	authRouter.HandleFunc("/api/proxy/vdir/del", ReverseProxyDeleteVdir)
	authRouter.HandleFunc("/api/proxy/vdir/edit", ReverseProxyEditVdir)
	authRouter.HandleFunc("/api/proxy/vdir/bulkForwardAuth", ReverseProxyBulkApplyVdirByForwardAuth)
	/* Reverse proxy user-defined header */
	authRouter.HandleFunc("/api/proxy/header/list", HandleCustomHeaderList)
	authRouter.HandleFunc("/api/proxy/header/add", HandleCustomHeaderAdd)
	authRouter.HandleFunc("/api/proxy/header/remove", HandleCustomHeaderRemove)
	authRouter.HandleFunc("/api/proxy/header/handleHSTS", HandleHSTSState)
	authRouter.HandleFunc("/api/proxy/header/handleHopByHop", HandleHopByHop)
	authRouter.HandleFunc("/api/proxy/header/handleUserAgent", HandleUserAgent)
	authRouter.HandleFunc("/api/proxy/header/handleHostOverwrite", HandleHostOverwrite)
	authRouter.HandleFunc("/api/proxy/header/handlePermissionPolicy", HandlePermissionPolicy)
	authRouter.HandleFunc("/api/proxy/header/handleWsHeaderBehavior", HandleWsHeaderBehavior)
	/* Reverse proxy auth related */
	authRouter.HandleFunc("/api/proxy/auth/exceptions/list", ListProxyBasicAuthExceptionPaths)
	authRouter.HandleFunc("/api/proxy/auth/exceptions/add", AddProxyBasicAuthExceptionPaths)
	authRouter.HandleFunc("/api/proxy/auth/exceptions/delete", RemoveProxyBasicAuthExceptionPaths)
	/* ZorxAuth SSO per-endpoint exception rules */
	authRouter.HandleFunc("/api/proxy/auth/zorxauth/exceptions/list", ListProxyZorxAuthExceptionRules)
	authRouter.HandleFunc("/api/proxy/auth/zorxauth/exceptions/add", AddProxyZorxAuthExceptionRule)
	authRouter.HandleFunc("/api/proxy/auth/zorxauth/exceptions/delete", RemoveProxyZorxAuthExceptionRule)
}

func RegisterTLSAPIs(authRouter *auth.RouterDef) {
	//Global certificate settings
	authRouter.HandleFunc("/api/cert/tls", handleToggleTLSProxy)
	authRouter.HandleFunc("/api/cert/tlsMinVersion", handleSetTlsMinVersion)
	authRouter.HandleFunc("/api/cert/resolve", handleCertTryResolve)
	authRouter.HandleFunc("/api/cert/setPreferredCertificate", handleSetDomainPreferredCertificate)

	//Certificate store functions
	authRouter.HandleFunc("/api/cert/setDefault", tlsCertManager.SetCertAsDefault)
	authRouter.HandleFunc("/api/cert/getCommonName", tlsCertManager.HandleGetCertCommonName)
	authRouter.HandleFunc("/api/cert/upload", tlsCertManager.HandleCertUpload)
	authRouter.HandleFunc("/api/cert/download", tlsCertManager.HandleCertDownload)
	authRouter.HandleFunc("/api/cert/list", tlsCertManager.HandleListCertificate)
	authRouter.HandleFunc("/api/cert/listdomains", tlsCertManager.HandleListDomains)
	authRouter.HandleFunc("/api/cert/checkDefault", tlsCertManager.HandleDefaultCertCheck)
	authRouter.HandleFunc("/api/cert/delete", tlsCertManager.HandleCertRemove)
	authRouter.HandleFunc("/api/cert/selfsign", tlsCertManager.HandleSelfSignCertGenerate)
}

func RegisterAuthenticationHandlerAPIs(authRouter *auth.RouterDef) {
	authRouter.HandleFunc("/api/sso/forward-auth", forwardAuthRouter.HandleAPIOptions)
	authRouter.HandleFunc("/api/sso/OAuth2", oauth2Router.HandleSetOAuth2Settings)
	authRouter.HandleFunc("/api/sso/zorxauth/provider", zorxAuthRouter.HandleAuthProviderSettings)
	authRouter.HandleFunc("/api/sso/zorxauth/gateway", zorxAuthRouter.HandleGatewaySettings)

	// Login page branding customization
	authRouter.HandleFunc("/api/sso/zorxauth/customization", zorxAuthRouter.HandleCustomizationSettings)
	authRouter.HandleFunc("/api/sso/zorxauth/customization/upload", zorxAuthRouter.HandleCustomizationUpload)
	authRouter.HandleFunc("/api/sso/zorxauth/customization/reset", zorxAuthRouter.HandleCustomizationReset)
	authRouter.HandleFunc("/api/sso/zorxauth/customization/preview", zorxAuthRouter.HandleCustomizationPreview)
}

func RegisterZorxAuthUserManagementAPIs(authRouter *auth.RouterDef) {
	authRouter.HandleFunc("/api/sso/zorxauth/users/list", zorxAuthRouter.HandleUsersList)
	authRouter.HandleFunc("/api/sso/zorxauth/users/create", zorxAuthRouter.HandleUserCreate)
	authRouter.HandleFunc("/api/sso/zorxauth/users/update", zorxAuthRouter.HandleUserUpdate)
	authRouter.HandleFunc("/api/sso/zorxauth/users/delete", zorxAuthRouter.HandleUserDelete)
	authRouter.HandleFunc("/api/sso/zorxauth/users/logoutAll", zorxAuthRouter.HandleLogoutAllUsers)
	authRouter.HandleFunc("/api/sso/zorxauth/users/disable2fa", zorxAuthRouter.HandleDisableUserTOTP)

	// Group Policy management
	authRouter.HandleFunc("/api/sso/zorxauth/grouppolicy/list", zorxAuthRouter.HandleGroupPolicyList)
	authRouter.HandleFunc("/api/sso/zorxauth/grouppolicy/create", zorxAuthRouter.HandleGroupPolicyCreate)
	authRouter.HandleFunc("/api/sso/zorxauth/grouppolicy/update", zorxAuthRouter.HandleGroupPolicyUpdate)
	authRouter.HandleFunc("/api/sso/zorxauth/grouppolicy/delete", zorxAuthRouter.HandleGroupPolicyDelete)
}

func RegisterRedirectionAPIs(authRouter *auth.RouterDef) {
	authRouter.HandleFunc("/api/redirect/list", handleListRedirectionRules)
	authRouter.HandleFunc("/api/redirect/add", handleAddRedirectionRule)
	authRouter.HandleFunc("/api/redirect/delete", handleDeleteRedirectionRule)
	authRouter.HandleFunc("/api/redirect/edit", handleEditRedirectionRule)
	authRouter.HandleFunc("/api/redirect/toggle", handleToggleRedirectionRuleEnable)
	authRouter.HandleFunc("/api/redirect/regex", handleToggleRedirectRegexpSupport)
	authRouter.HandleFunc("/api/redirect/case_sensitive", handleToggleRedirectCaseSensitivity)
}

func RegisterAccessRuleAPIs(authRouter *auth.RouterDef) {
	/* ACLs and objects */
	authRouter.HandleFunc("/api/acl/list", HandleACLList)
	authRouter.HandleFunc("/api/acl/get", HandleACLGet)
	authRouter.HandleFunc("/api/acl/create", HandleACLCreate)
	authRouter.HandleFunc("/api/acl/duplicate", HandleACLDuplicate)
	authRouter.HandleFunc("/api/acl/save", HandleACLSave)
	authRouter.HandleFunc("/api/acl/delete", HandleACLDelete)
	authRouter.HandleFunc("/api/acl/test", HandleACLTest)
	authRouter.HandleFunc("/api/acl/attach", HandleACLAttach)
	authRouter.HandleFunc("/api/acl/objects", HandleACLObjects)
	authRouter.HandleFunc("/api/acl/object/save", HandleACLObjectSave)
	authRouter.HandleFunc("/api/acl/object/delete", HandleACLObjectDelete)

	authRouter.HandleFunc("/api/quickban/list", handleListQuickBan)
	/* Trusted Proxies */
	authRouter.HandleFunc("/api/trustedproxy/list", handleListTrustedProxies)
	authRouter.HandleFunc("/api/trustedproxy/add", handleAddTrustedProxy)
	authRouter.HandleFunc("/api/trustedproxy/remove", handleRemoveTrustedProxy)
	authRouter.HandleFunc("/api/trustedproxy/update", handleUpdateTrustedProxy)
	authRouter.HandleFunc("/api/trustedproxy/bulkUpdate", handleBulkUpdateTrustedProxies)
	authRouter.HandleFunc("/api/trustedproxy/reset", handleResetDefaultTrustedProxies)
}

func RegisterPathRuleAPIs(authRouter *auth.RouterDef) {
	authRouter.HandleFunc("/api/pathrule/add", pathRuleHandler.HandleAddBlockingPath)
	authRouter.HandleFunc("/api/pathrule/list", pathRuleHandler.HandleListBlockingPath)
	authRouter.HandleFunc("/api/pathrule/remove", pathRuleHandler.HandleRemoveBlockingPath)
}

func RegisterStatisticalAPIs(authRouter *auth.RouterDef) {
	/* Traffic Summary */
	authRouter.HandleFunc("/api/stats/summary", statisticCollector.HandleTodayStatLoad)
	authRouter.HandleFunc("/api/stats/overview", HandleDashboardOverview)
	authRouter.HandleFunc("/api/stats/trafficmap", HandleTrafficMapData)
	authRouter.HandleFunc("/api/stats/countries", HandleCountryDistrSummary)
	authRouter.HandleFunc("/api/custom/statusPanels", HandleStatusPanels)
	authRouter.HandleFunc("/api/stats/netstat", netstatBuffers.HandleGetNetworkInterfaceStats)
	authRouter.HandleFunc("/api/stats/netstatgraph", netstatBuffers.HandleGetBufferedNetworkInterfaceStats)
	/* Host System Information */
	authRouter.HandleFunc("/api/stats/system", HandleSystemResourceUsage)
	/* Analytics */
	authRouter.HandleFunc("/api/analytic/list", AnalyticLoader.HandleSummaryList)
	authRouter.HandleFunc("/api/analytic/load", AnalyticLoader.HandleLoadTargetDaySummary)
	authRouter.HandleFunc("/api/analytic/loadRange", AnalyticLoader.HandleLoadTargetRangeSummary)
	authRouter.HandleFunc("/api/analytic/exportRange", AnalyticLoader.HandleRangeExport)
	authRouter.HandleFunc("/api/analytic/resetRange", AnalyticLoader.HandleRangeReset)
	authRouter.HandleFunc("/api/analytic/resetAll", AnalyticLoader.HandleResetAllStats)
	/* UpTime Monitor */
	authRouter.HandleFunc("/api/utm/list", HandleUptimeMonitorListing)
}

func RegisterL4ProxyAPIs(authRouter *auth.RouterDef) {
	handlers := map[string]http.HandlerFunc{
		"add":       l4ProxyManager.HandleAddProxyConfig,
		"edit":      l4ProxyManager.HandleEditProxyConfigs,
		"list":      l4ProxyManager.HandleListConfigs,
		"start":     l4ProxyManager.HandleStartProxy,
		"stop":      l4ProxyManager.HandleStopProxy,
		"delete":    l4ProxyManager.HandleRemoveProxy,
		"status":    l4ProxyManager.HandleGetProxyStatus,
		"pool":      l4ProxyManager.HandleSetPool,
		"upstreams": l4ProxyManager.HandleUpstreamStatus,
	}
	for action, handler := range handlers {
		authRouter.HandleFunc("/api/l4proxy/config/"+action, handler)
		// Same handlers under the old /api/streamprox/ paths
		authRouter.HandleFunc("/api/streamprox/config/"+action, handler)
	}
	authRouter.HandleFunc("/api/l4proxy/healthcheck/get", HandleL4HealthCheckGet)
	authRouter.HandleFunc("/api/l4proxy/healthcheck/set", HandleL4HealthCheckSet)
}

func RegisterMDNSAPIs(authRouter *auth.RouterDef) {
	authRouter.HandleFunc("/api/mdns/list", HandleMdnsListing)
}

func RegisterACMEAndAutoRenewerAPIs(authRouter *auth.RouterDef) {
	/* ACME Core */
	authRouter.HandleFunc("/api/acme/listExpiredDomains", acmeHandler.HandleGetExpiredDomains)
	authRouter.HandleFunc("/api/acme/obtainCert", AcmeCheckAndHandleRenewCertificate)
	/* Auto Renewer */
	authRouter.HandleFunc("/api/acme/autoRenew/enable", acmeAutoRenewer.HandleAutoRenewEnable)
	authRouter.HandleFunc("/api/acme/autoRenew/ca", HandleACMEPreferredCA)
	authRouter.HandleFunc("/api/acme/autoRenew/email", acmeAutoRenewer.HandleACMEEmail)
	authRouter.HandleFunc("/api/acme/autoRenew/setDomains", acmeAutoRenewer.HandleSetAutoRenewDomains)
	authRouter.HandleFunc("/api/acme/autoRenew/setEAB", acmeAutoRenewer.HanldeSetEAB)
	authRouter.HandleFunc("/api/acme/autoRenew/listDomains", acmeAutoRenewer.HandleLoadAutoRenewDomains)
	authRouter.HandleFunc("/api/acme/autoRenew/renewPolicy", acmeAutoRenewer.HandleRenewPolicy)
	authRouter.HandleFunc("/api/acme/autoRenew/renewNow", acmeAutoRenewer.HandleRenewNow)
	authRouter.HandleFunc("/api/acme/dns/providers", acmedns.HandleServeProvidersJson)
	/* DNS credential sets */
	authRouter.HandleFunc("/api/acme/dnsProfiles/list", acmeHandler.HandleListDNSProfiles)
	authRouter.HandleFunc("/api/acme/dnsProfiles/save", acmeHandler.HandleSaveDNSProfile)
	authRouter.HandleFunc("/api/acme/dnsProfiles/delete", acmeHandler.HandleDeleteDNSProfile)
	authRouter.HandleFunc("/api/acme/dnsProfiles/bindings", acmeHandler.HandleListDNSBindings)
	authRouter.HandleFunc("/api/acme/dnsProfiles/assign", acmeHandler.HandleAssignDNSProfile)
	/* ACME Wizard */
	authRouter.HandleFunc("/api/acme/wizard", acmewizard.HandleGuidedStepCheck)
}

func RegisterStaticWebServerAPIs(authRouter *auth.RouterDef) {
	/* Static Web Server Controls */
	authRouter.HandleFunc("/api/webserv/status", staticWebServer.HandleGetStatus)
	authRouter.HandleFunc("/api/webserv/start", staticWebServer.HandleStartServer)
	authRouter.HandleFunc("/api/webserv/stop", staticWebServer.HandleStopServer)
	authRouter.HandleFunc("/api/webserv/setPort", HandleStaticWebServerPortChange)
	authRouter.HandleFunc("/api/webserv/setDirList", staticWebServer.SetEnableDirectoryListing)
	authRouter.HandleFunc("/api/webserv/disableListenAllInterface", staticWebServer.SetDisableListenToAllInterface)

	/* WebDAV Server Controls */
	authRouter.HandleFunc("/api/webserv/webdav/start", staticWebServer.HandleStartWebDAV)
	authRouter.HandleFunc("/api/webserv/webdav/stop", staticWebServer.HandleStopWebDAV)
	authRouter.HandleFunc("/api/webserv/webdav/setPort", staticWebServer.HandleWebDAVPortChange)
	authRouter.HandleFunc("/api/webserv/webdav/setUseCustomCredentials", staticWebServer.HandleSetUseCustomCredentials)
	authRouter.HandleFunc("/api/webserv/webdav/setCustomCredentials", staticWebServer.HandleSetCustomCredentials)
}

func RegisterPluginAPIs(authRouter *auth.RouterDef) {
	authRouter.HandleFunc("/api/plugins/list", pluginManager.HandleListPlugins)
	authRouter.HandleFunc("/api/plugins/enable", pluginManager.HandleEnablePlugin)
	authRouter.HandleFunc("/api/plugins/disable", pluginManager.HandleDisablePlugin)
	authRouter.HandleFunc("/api/plugins/rebuild", pluginManager.HandleRebuildPlugin)
	authRouter.HandleFunc("/api/plugins/icon", pluginManager.HandleLoadPluginIcon)
	authRouter.HandleFunc("/api/plugins/info", pluginManager.HandlePluginInfo)

	authRouter.HandleFunc("/api/plugins/groups/list", pluginManager.HandleListPluginGroups)
	authRouter.HandleFunc("/api/plugins/groups/add", pluginManager.HandleAddPluginToGroup)
	authRouter.HandleFunc("/api/plugins/groups/remove", pluginManager.HandleRemovePluginFromGroup)
	authRouter.HandleFunc("/api/plugins/groups/deleteTag", pluginManager.HandleRemovePluginGroup)

	authRouter.HandleFunc("/api/plugins/store/list", pluginManager.HandleListDownloadablePlugins)
	authRouter.HandleFunc("/api/plugins/store/resync", pluginManager.HandleResyncPluginList)
	authRouter.HandleFunc("/api/plugins/store/install", pluginManager.HandleInstallPlugin)
	authRouter.HandleFunc("/api/plugins/store/uninstall", pluginManager.HandleUninstallPlugin)
	authRouter.HandleFunc("/api/plugins/store/checkUpdates", pluginManager.HandleCheckPluginUpdates)
	authRouter.HandleFunc("/api/plugins/store/update", pluginManager.HandleUpdatePlugin)
	authRouter.HandleFunc("/api/plugins/store/sources", pluginManager.HandleListStoreSources)
	authRouter.HandleFunc("/api/plugins/store/sources/add", pluginManager.HandleAddStoreSource)
	authRouter.HandleFunc("/api/plugins/store/sources/remove", pluginManager.HandleRemoveStoreSource)

	// Developer options
	authRouter.HandleFunc("/api/plugins/developer/enableAutoReload", pluginManager.HandleEnableHotReload)
	authRouter.HandleFunc("/api/plugins/developer/setAutoReloadInterval", pluginManager.HandleSetHotReloadInterval)
}

func RegisterAuthAPIs(requireAuth bool, targetMux *http.ServeMux) {
	targetMux.HandleFunc("/api/auth/login", authAgent.HandleLogin)
	targetMux.HandleFunc("/api/auth/logout", authAgent.HandleLogout)
	targetMux.HandleFunc("/api/auth/checkLogin", func(w http.ResponseWriter, r *http.Request) {
		if requireAuth {
			authAgent.CheckLogin(w, r)
		} else {
			utils.SendJSONResponse(w, "true")
		}
	})
	targetMux.HandleFunc("/api/auth/authEnabled", func(w http.ResponseWriter, r *http.Request) {
		js, _ := json.Marshal(requireAuth)
		utils.SendJSONResponse(w, string(js))
	})
	targetMux.HandleFunc("/api/auth/username", func(w http.ResponseWriter, r *http.Request) {
		username, err := authAgent.GetUserName(w, r)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		js, _ := json.Marshal(username)
		utils.SendJSONResponse(w, string(js))
	})
	targetMux.HandleFunc("/api/auth/userCount", func(w http.ResponseWriter, r *http.Request) {
		js, _ := json.Marshal(authAgent.GetUserCounts())
		utils.SendJSONResponse(w, string(js))
	})
	targetMux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if authAgent.GetUserCounts() == 0 {
			authAgent.HandleRegisterWithoutEmail(w, r, func(username, reserved string) {})
		} else {
			utils.SendErrorResponse(w, "Root management account already exists")
		}
	})
	targetMux.HandleFunc("/api/auth/changePassword", func(w http.ResponseWriter, r *http.Request) {
		username, err := authAgent.GetUserName(w, r)
		if err != nil {
			http.Error(w, "401 - Unauthorized", http.StatusUnauthorized)
			return
		}

		oldPassword, err := utils.PostPara(r, "oldPassword")
		if err != nil {
			utils.SendErrorResponse(w, "empty current password")
			return
		}
		newPassword, err := utils.PostPara(r, "newPassword")
		if err != nil {
			utils.SendErrorResponse(w, "empty new password")
			return
		}
		confirmPassword, _ := utils.PostPara(r, "confirmPassword")

		if newPassword != confirmPassword {
			utils.SendErrorResponse(w, "confirm password not match")
			return
		}

		oldPasswordCorrect, _ := authAgent.ValidateUsernameAndPasswordWithReason(username, oldPassword)
		if !oldPasswordCorrect {
			utils.SendErrorResponse(w, "Invalid current password given")
			return
		}

		if err := authAgent.SetPassword(username, newPassword); err != nil {
			utils.SendErrorResponse(w, "could not change the password: "+err.Error())
			return
		}
		utils.SendOK(w)
	})
}

func initAPIs(targetMux *http.ServeMux) {
	authRouter := auth.NewManagedHTTPRouter(auth.RouterOption{
		AuthAgent:   authAgent,
		RequireAuth: requireAuth,
		TargetMux:   targetMux,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "401 - Unauthorized", http.StatusUnauthorized)
		},
	})

	var staticWebRes http.Handler
	webFolderExists := utils.FileExists("web")
	if *development_build && !webFolderExists {
		fmt.Println("Warning: Development build is enabled but 'web' folder does not exist. Falling back to embedded static resources.")
		fmt.Println("If you are developing the web UI, please make sure to have the 'web' folder in the same directory as the executable for a better development experience with hot reload.")
	}
	if *development_build && webFolderExists {
		staticWebRes = http.FileServer(http.Dir("web/"))
	} else {
		subFS, err := fs.Sub(webres, "web")
		if err != nil {
			panic("Failed to strip 'web/' from embedded resources: " + err.Error())
		}
		staticWebRes = http.FileServer(http.FS(subFS))
	}

	advHandler := FSHandler(staticWebRes)
	targetMux.Handle("/", advHandler)

	RegisterAuthAPIs(requireAuth, targetMux)
	RegisterHTTPProxyAPIs(authRouter)
	RegisterTLSAPIs(authRouter)
	RegisterAuthenticationHandlerAPIs(authRouter)
	RegisterZorxAuthUserManagementAPIs(authRouter)
	RegisterRedirectionAPIs(authRouter)
	RegisterAccessRuleAPIs(authRouter)
	RegisterPathRuleAPIs(authRouter)
	RegisterStatisticalAPIs(authRouter)
	RegisterL4ProxyAPIs(authRouter)
	RegisterMDNSAPIs(authRouter)
	RegisterACMEAndAutoRenewerAPIs(authRouter)
	RegisterStaticWebServerAPIs(authRouter)
	RegisterPluginAPIs(authRouter)

	targetMux.HandleFunc("/api/info/x", HandleZoraxyInfo)
	authRouter.HandleFunc("/api/info/geoip", HandleGeoIpLookup)
	authRouter.HandleFunc("/api/geodb/updateStatus", HandleGeoDBUpdateStatus)
	authRouter.HandleFunc("/api/geodb/updateNow", HandleGeoDBUpdateNow)
	authRouter.HandleFunc("/api/geodb/autoUpdate", HandleGeoDBAutoUpdate)
	authRouter.HandleFunc("/api/geodb/enabled", HandleGeoIPEnabled)
	authRouter.HandleFunc("/api/conf/export", ExportConfigAsZip)
	authRouter.HandleFunc("/api/conf/import", ImportConfigFromZip)
	authRouter.HandleFunc("/api/log/list", LogViewer.HandleListLog)
	authRouter.HandleFunc("/api/log/read", LogViewer.HandleReadLog)
	authRouter.HandleFunc("/api/log/rotate/trigger", SystemWideLogger.HandleDebugTriggerLogRotation)
	authRouter.HandleFunc("/api/logger/config", handleLoggerConfig)
	authRouter.HandleFunc("/api/logger/outputs", HandleLogOutputs)
	authRouter.HandleFunc("/api/logger/outputs/test", HandleLogOutputsTest)
	authRouter.HandleFunc("/api/capture/status", HandleCaptureStatus)
	authRouter.HandleFunc("/api/capture/start", HandleCaptureStart)
	authRouter.HandleFunc("/api/capture/stop", HandleCaptureStop)
	authRouter.HandleFunc("/api/capture/download", HandleCaptureDownload)
	authRouter.HandleFunc("/api/capture/delete", HandleCaptureDelete)
}
