package dynamicproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"imuslab.com/zoraxy/mod/dynamicproxy/dpcore"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/dynamicproxy/rewrite"
	"imuslab.com/zoraxy/mod/statistic"
	"imuslab.com/zoraxy/mod/websocketproxy"
)

// Check if the request URI matches any of the proxy endpoint
func (router *Router) getTargetProxyEndpointFromRequestURI(requestURI string) *ProxyEndpoint {
	var targetProxyEndpoint *ProxyEndpoint = nil
	router.ProxyEndpoints.Range(func(key, value interface{}) bool {
		rootname := key.(string)
		if strings.HasPrefix(requestURI, rootname) {
			thisProxyEndpoint := value.(*ProxyEndpoint)
			targetProxyEndpoint = thisProxyEndpoint
		}
		return true
	})

	return targetProxyEndpoint
}

// Get the proxy endpoint from hostname, which might includes checking of wildcard certificates
func (router *Router) GetProxyEndpointFromHostname(hostname string) *ProxyEndpoint {
	return router.getProxyEndpoint(hostname, nil)
}

// getProxyEndpoint finds the rule for hostname. With local set, rules that
// only answer on another address are skipped.
func (router *Router) getProxyEndpoint(hostname string, local net.IP) *ProxyEndpoint {
	var targetSubdomainEndpoint *ProxyEndpoint = nil
	hostname = strings.ToLower(hostname)
	ep, ok := router.ProxyEndpoints.Load(hostname)
	if ok && ep.(*ProxyEndpoint).answersOn(local) {
		targetSubdomainEndpoint = ep.(*ProxyEndpoint)
		if !targetSubdomainEndpoint.Disabled {
			return targetSubdomainEndpoint
		}
	}

	//No hit. Try with wildcard and alias
	matchProxyEndpoints := []*ProxyEndpoint{}
	router.ProxyEndpoints.Range(func(k, v interface{}) bool {
		ep := v.(*ProxyEndpoint)
		if ep.Disabled || !ep.answersOn(local) {
			//Skip disabled endpoint, and rules that answer on another address
			return true
		}

		match, err := filepath.Match(ep.RootOrMatchingDomain, hostname)
		if err != nil {
			//Bad pattern. Skip this rule
			return true
		}

		if match {
			//Wildcard matches. Skip checking alias
			matchProxyEndpoints = append(matchProxyEndpoints, ep)
			return true
		}

		//Wildcard not match. Check for alias
		if len(ep.MatchingDomainAlias) > 0 {
			for _, aliasDomain := range ep.MatchingDomainAlias {
				match, err := filepath.Match(aliasDomain, hostname)
				if err != nil {
					//Bad pattern. Skip this alias
					continue
				}

				if match {
					matchProxyEndpoints = append(matchProxyEndpoints, ep)
					return true
				}
			}
		}
		return true
	})

	if len(matchProxyEndpoints) == 1 {
		return matchProxyEndpoints[0]
	} else if len(matchProxyEndpoints) > 1 {
		// More than one match, pick one that is:
		// 1. longer RootOrMatchingDomain (more specific)
		// 2. fewer wildcard characters (* and ?) (more specific)
		// 3. fallback to lexicographic order
		sort.SliceStable(matchProxyEndpoints, func(i, j int) bool {
			a := matchProxyEndpoints[i].RootOrMatchingDomain
			b := matchProxyEndpoints[j].RootOrMatchingDomain
			if len(a) != len(b) {
				return len(a) > len(b)
			}
			aw := strings.Count(a, "*") + strings.Count(a, "?")
			bw := strings.Count(b, "*") + strings.Count(b, "?")
			if aw != bw {
				return aw < bw
			}
			return a < b
		})
		return matchProxyEndpoints[0]
	}

	return targetSubdomainEndpoint
}

// Rewrite URL rewrite the prefix part of a virtual directory URL with /
// Only the path portion is normalized (collapsing duplicated slashes)
func (router *Router) rewriteURL(rooturl string, requestURL string) string {
	rewrittenURL := requestURL
	rewrittenURL = strings.TrimPrefix(rewrittenURL, strings.TrimSuffix(rooturl, "/"))

	path, query, hasQuery := strings.Cut(rewrittenURL, "?")
	if strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}

	if hasQuery {
		return path + "?" + query
	}
	return path
}

// upstreamHostSwap serves the request directly when the upstream is one of
// this proxy's own rules, and reports whether it did.
func (h *ProxyHandler) upstreamHostSwap(w http.ResponseWriter, r *http.Request, selectedUpstream *loadbalance.Upstream, currentTarget *ProxyEndpoint) bool {
	upstreamHostname := selectedUpstream.OriginIpOrDomain
	if strings.Contains(upstreamHostname, ":") {
		upstreamHostname = strings.Split(upstreamHostname, ":")[0]
	}
	loopbackProxyEndpoint := h.Parent.GetProxyEndpointFromHostname(upstreamHostname)
	if loopbackProxyEndpoint != nil && loopbackProxyEndpoint != currentTarget {
		// Loopback to one of this proxy's rules
		if loopbackProxyEndpoint.IsEnabled() {
			h.hostRequest(w, r, loopbackProxyEndpoint)
		} else {
			serveProxyRequestError(w, 521, h.Parent, ErrorTemplateRPError)
			h.Parent.logRequest(r, false, 521, "host-http", r.Host, upstreamHostname, currentTarget)
		}
		return true
	}
	return false
}

// Handle host request
func (h *ProxyHandler) hostRequest(w http.ResponseWriter, r *http.Request, target *ProxyEndpoint) {
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-Server", "waymark-"+h.Parent.Option.HostUUID)
	reqHostname := r.Host

	// Maintenance mode
	if h.Parent.serveMaintenance(w, r, target) {
		return
	}

	// All upstreams down
	if h.allUpstreamsDown(target) {
		serveProxyRequestError(w, 521, h.Parent, ErrorTemplateRPError)
		h.Parent.logRequest(r, false, 521, "host-http", reqHostname, "health-check-down", target)
		return
	}

	// Static file cache
	cacheSession := h.assetCacheBegin(w, r, target)
	if cacheSession != nil {
		if cacheSession.Served() {
			return
		}
		w, r = cacheSession, cacheSession.UpstreamRequest()
		defer cacheSession.Finish()
	}

	/* Load balancing */
	selectedUpstream, err := h.Parent.loadBalancer.GetRequestUpstreamTarget(w, r, target.ActiveOrigins, target.UseStickySession, autoFallbackOff(target), target.LoadBalancingMode)
	if err != nil {
		serveProxyRequestError(w, 521, h.Parent, ErrorTemplateRPError)
		h.Parent.Option.Logger.PrintAndLog("http-proxy", "Failed to assign a destination for this request", err)
		h.Parent.logRequest(r, false, 521, "subdomain-http", r.URL.Hostname(), r.Host, target)
		return
	}
	// Count the request in progress for least connections
	defer h.Parent.loadBalancer.Track(selectedUpstream.OriginIpOrDomain)()

	/* Upstream Host Swap (use to detect loopback to self) */
	if h.upstreamHostSwap(w, r, selectedUpstream, target) {
		return
	}

	/* WebSocket automatic proxy */
	if isWebSocketRequest(r) {
		h.hostWebSocketRequest(w, r, target, selectedUpstream)
		return
	}

	if r.URL != nil {
		r.Host = r.URL.Host
	} else {
		//Fallback when the upstream proxy screw something up in the header
		r.URL, _ = url.Parse(reqHostname)
	}

	//Populate the user-defined headers with the values from the request
	headerRewriteOptions := GetDefaultHeaderRewriteRules()
	if target.HeaderRewriteRules != nil {
		headerRewriteOptions = target.HeaderRewriteRules
	}
	rewrittenUserDefinedHeaders := rewrite.PopulateRequestHeaderVariables(r, headerRewriteOptions.UserDefinedHeaders)

	//Build downstream and upstream header rules
	upstreamHeaders, downstreamHeaders := rewrite.SplitUpDownStreamHeaders(&rewrite.HeaderRewriteOptions{
		UserDefinedHeaders:           rewrittenUserDefinedHeaders,
		HSTSMaxAge:                   headerRewriteOptions.HSTSMaxAge,
		HSTSIncludeSubdomains:        target.ContainsWildcardName(true),
		EnablePermissionPolicyHeader: headerRewriteOptions.EnablePermissionPolicyHeader,
		PermissionPolicy:             headerRewriteOptions.PermissionPolicy,
	})

	statusCode, err := selectedUpstream.ServeHTTP(w, r, &dpcore.ResponseRewriteRuleSet{
		ProxyDomain:                    selectedUpstream.OriginIpOrDomain,
		OriginalHost:                   reqHostname,
		UseTLS:                         selectedUpstream.RequireTLS,
		NoCache:                        h.Parent.Option.NoCache,
		PathPrefix:                     "",
		UpstreamHeaders:                upstreamHeaders,
		DownstreamHeaders:              downstreamHeaders,
		DisableChunkedTransferEncoding: target.DisableChunkedTransferEncoding,
		ForceHTTP11:                    target.ForceHTTP11,
		NoRemoveUserAgentHeader:        headerRewriteOptions.DisableUserAgentHeaderRemoval,
		HostHeaderOverwrite:            headerRewriteOptions.RequestHostOverwrite,
		NoRemoveHopByHop:               headerRewriteOptions.DisableHopByHopHeaderRemoval,
		AllowConnect:                   target.EnableConnectSupport,
		AllowUpgrade:                   target.EnableUpgradeForwarding,
		Version:                        target.parent.Option.HostVersion,
		DevelopmentMode:                target.parent.Option.DevelopmentMode,
		AltSvc:                         h.Parent.getAltSvcValue(),
	})

	// Passive health monitoring
	if observer := h.Parent.Option.UpstreamResultObserver; observer != nil {
		observer(target.RootOrMatchingDomain, selectedUpstream.OriginIpOrDomain, statusCode, err)
	}
	cacheSession.SetUpstreamResult(statusCode, err)

	var dnsError *net.DNSError
	upstreamHostname := selectedUpstream.OriginIpOrDomain
	if err != nil {
		if errors.As(err, &dnsError) {
			serveProxyRequestError(w, 404, h.Parent, ErrorTemplateHostError)
			h.Parent.logRequest(r, false, 404, "host-http", reqHostname, upstreamHostname, target)
		} else if errors.Is(err, context.Canceled) {
			//Request canceled by client, usually due to manual refresh before page load
			http.Error(w, "Request canceled", http.StatusRequestTimeout)
			h.Parent.logRequest(r, false, http.StatusRequestTimeout, "host-http", reqHostname, upstreamHostname, target)
		} else {
			serveProxyRequestError(w, 521, h.Parent, ErrorTemplateRPError)
			h.Parent.logRequest(r, false, 521, "host-http", reqHostname, upstreamHostname, target)
		}
		return
	}

	h.Parent.logRequest(r, true, statusCode, "host-http", reqHostname, upstreamHostname, target)
}

// hostWebSocketRequest proxies a host level WebSocket upgrade request via websocketproxy
func (h *ProxyHandler) hostWebSocketRequest(w http.ResponseWriter, r *http.Request, target *ProxyEndpoint, selectedUpstream *loadbalance.Upstream) {
	if target.DisableWebSocket {
		http.Error(w, "WebSocket connections are disabled for this endpoint", http.StatusForbidden)
		return
	}
	//Handle WebSocket request. Forward the custom Upgrade header and rewrite origin
	r.Header.Set("Zr-Origin-Upgrade", "websocket")
	requestURL := r.URL.String()
	wsRedirectionEndpoint := selectedUpstream.OriginIpOrDomain
	if wsRedirectionEndpoint[len(wsRedirectionEndpoint)-1:] != "/" {
		wsRedirectionEndpoint = wsRedirectionEndpoint + "/"
	}
	if len(requestURL) > 0 && requestURL[:1] == "/" {
		requestURL = requestURL[1:]
	}
	u, _ := url.Parse("ws://" + wsRedirectionEndpoint + requestURL)
	if selectedUpstream.RequireTLS {
		u, _ = url.Parse("wss://" + wsRedirectionEndpoint + requestURL)
	}
	h.Parent.logRequest(r, true, 101, "host-websocket", r.Host, selectedUpstream.OriginIpOrDomain, target)

	if target.HeaderRewriteRules == nil {
		target.HeaderRewriteRules = GetDefaultHeaderRewriteRules()
	}

	wspHandler := websocketproxy.NewProxy(u, websocketproxy.Options{
		SkipTLSValidation:              selectedUpstream.SkipCertValidations,
		SkipOriginCheck:                selectedUpstream.SkipWebSocketOriginCheck,
		CopyAllHeaders:                 target.EnableWebsocketCustomHeaders,
		UserDefinedHeaders:             target.HeaderRewriteRules.UserDefinedHeaders,
		Logger:                         h.Parent.Option.Logger,
		Timeout:                        target.WebsocketTimeout,
		EnableTimeoutRefreshOnActivity: target.EnableTimeoutRefreshOnActivity,
	})
	wspHandler.ServeHTTP(w, r)
}

// Handle vdir type request
func (h *ProxyHandler) vdirRequest(w http.ResponseWriter, r *http.Request, target *VirtualDirectoryEndpoint) {
	// Maintenance mode
	if h.Parent.serveMaintenance(w, r, target.parent) {
		return
	}

	// Virtual directory backend down
	if h.vdirDown(target) {
		serveProxyRequestError(w, 521, h.Parent, ErrorTemplateRPError)
		h.Parent.logRequest(r, false, 521, "vdir-http", r.Host, "health-check-down", target.parent)
		return
	}

	// Static file cache, keyed on the unrewritten URL
	cacheSession := h.assetCacheBegin(w, r, target.parent)
	if cacheSession != nil {
		if cacheSession.Served() {
			return
		}
		w, r = cacheSession, cacheSession.UpstreamRequest()
		defer cacheSession.Finish()
	}

	rewriteURL := h.Parent.rewriteURL(target.MatchingPath, r.RequestURI)
	r.URL, _ = url.Parse(rewriteURL)
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-Server", "waymark-"+h.Parent.Option.HostUUID)

	if isWebSocketRequest(r) {
		if target.parent.DisableWebSocket {
			http.Error(w, "WebSocket connections are disabled for this endpoint", http.StatusForbidden)
			return
		}
		//Handle WebSocket request. Forward the custom Upgrade header and rewrite origin
		r.Header.Set("Zr-Origin-Upgrade", "websocket")
		wsRedirectionEndpoint := target.Domain
		if wsRedirectionEndpoint[len(wsRedirectionEndpoint)-1:] != "/" {
			wsRedirectionEndpoint = wsRedirectionEndpoint + "/"
		}
		u, _ := url.Parse("ws://" + wsRedirectionEndpoint + r.URL.String())
		if target.RequireTLS {
			u, _ = url.Parse("wss://" + wsRedirectionEndpoint + r.URL.String())
		}

		if target.parent.HeaderRewriteRules == nil {
			target.parent.HeaderRewriteRules = GetDefaultHeaderRewriteRules()
		}

		h.Parent.logRequest(r, true, 101, "vdir-websocket", r.Host, target.Domain, target.parent)
		wspHandler := websocketproxy.NewProxy(u, websocketproxy.Options{
			SkipTLSValidation:              target.SkipCertValidations,
			SkipOriginCheck:                true,
			CopyAllHeaders:                 target.parent.EnableWebsocketCustomHeaders,
			UserDefinedHeaders:             target.parent.HeaderRewriteRules.UserDefinedHeaders,
			Logger:                         h.Parent.Option.Logger,
			Timeout:                        target.parent.WebsocketTimeout,
			EnableTimeoutRefreshOnActivity: target.parent.EnableTimeoutRefreshOnActivity,
		})
		wspHandler.ServeHTTP(w, r)
		return
	}

	reqHostname := r.Host
	if r.URL != nil {
		r.Host = r.URL.Host
	} else {
		//Fallback when the upstream proxy screw something up in the header
		r.URL, _ = url.Parse(reqHostname)
	}

	//Populate the user-defined headers with the values from the request
	headerRewriteOptions := GetDefaultHeaderRewriteRules()
	if target.parent.HeaderRewriteRules != nil {
		headerRewriteOptions = target.parent.HeaderRewriteRules
	}

	rewrittenUserDefinedHeaders := rewrite.PopulateRequestHeaderVariables(r, headerRewriteOptions.UserDefinedHeaders)

	//Build downstream and upstream header rules, use the parent (subdomain) endpoint's headers
	upstreamHeaders, downstreamHeaders := rewrite.SplitUpDownStreamHeaders(&rewrite.HeaderRewriteOptions{
		UserDefinedHeaders:           rewrittenUserDefinedHeaders,
		HSTSMaxAge:                   headerRewriteOptions.HSTSMaxAge,
		HSTSIncludeSubdomains:        target.parent.ContainsWildcardName(true),
		EnablePermissionPolicyHeader: headerRewriteOptions.EnablePermissionPolicyHeader,
		PermissionPolicy:             headerRewriteOptions.PermissionPolicy,
	})

	statusCode, err := target.proxy.ServeHTTP(w, r, &dpcore.ResponseRewriteRuleSet{
		ProxyDomain:                    target.Domain,
		OriginalHost:                   reqHostname,
		UseTLS:                         target.RequireTLS,
		NoCache:                        target.parent.parent.Option.NoCache,
		PathPrefix:                     target.MatchingPath,
		UpstreamHeaders:                upstreamHeaders,
		DownstreamHeaders:              downstreamHeaders,
		DisableChunkedTransferEncoding: target.parent.DisableChunkedTransferEncoding,
		ForceHTTP11:                    target.parent.ForceHTTP11,
		NoRemoveUserAgentHeader:        headerRewriteOptions.DisableUserAgentHeaderRemoval,
		HostHeaderOverwrite:            headerRewriteOptions.RequestHostOverwrite,
		NoRemoveHopByHop:               headerRewriteOptions.DisableHopByHopHeaderRemoval,
		AllowUpgrade:                   target.parent.EnableUpgradeForwarding,
		Version:                        target.parent.parent.Option.HostVersion,
		DevelopmentMode:                target.parent.parent.Option.DevelopmentMode,
		AltSvc:                         h.Parent.getAltSvcValue(),
	})

	// Passive health monitoring and the static file cache
	if observer := h.Parent.Option.UpstreamResultObserver; observer != nil {
		observer(target.parent.RootOrMatchingDomain, target.Domain, statusCode, err)
	}
	cacheSession.SetUpstreamResult(statusCode, err)

	var dnsError *net.DNSError
	if err != nil {
		if errors.As(err, &dnsError) {
			serveProxyRequestError(w, 404, h.Parent, ErrorTemplateHostError)
			h.Parent.logRequest(r, false, 404, "vdir-http", reqHostname, target.Domain, target.parent)
		} else {
			serveProxyRequestError(w, 521, h.Parent, ErrorTemplateRPError)
			h.Parent.logRequest(r, false, 521, "vdir-http", reqHostname, target.Domain, target.parent)
		}
		return
	}
	h.Parent.logRequest(r, true, statusCode, "vdir-http", reqHostname, target.Domain, target.parent)

}

// logRequest records the request in the log file and the statistics.
func (router *Router) logRequest(r *http.Request, succ bool, statusCode int, forwardType string, originalHostname string, upstreamHostname string, endpoint *ProxyEndpoint) {
	// Client IP from the trusted-proxy aware resolver
	clientIP := router.GetClientIPForEndpoint(r, endpoint)

	// endpoint is nil when the request was handled before a rule matched (e.g. a redirection rule)
	if endpoint == nil || !endpoint.DisableLogging {
		router.Option.Logger.LogHTTPRequest(r, forwardType, statusCode, originalHostname, upstreamHostname, clientIP)
	}

	if endpoint == nil || router.Option.StatisticCollector == nil {
		return
	}

	if !endpoint.DisableStatisticCollection {

		go func() {
			countryISOCode := ""
			if router.Option.GeodbStore != nil {
				if countryInfo, err := router.Option.GeodbStore.ResolveCountryCodeFromIP(clientIP); err == nil {
					countryISOCode = countryInfo.CountryIsoCode
				}
			}
			requestInfo := statistic.RequestInfo{
				IpAddr:                        clientIP,
				RequestOriginalCountryISOCode: countryISOCode,
				Succ:                          succ,
				StatusCode:                    statusCode,
				ForwardType:                   forwardType,
				RequestMethod:                 r.Method,
				Referer:                       r.Referer(),
				UserAgent:                     r.UserAgent(),
				RequestURL:                    r.Host + r.URL.EscapedPath(),
				Target:                        originalHostname,
				Upstream:                      upstreamHostname,
			}
			router.Option.StatisticCollector.RecordRequest(requestInfo)
		}()
	}
}

// serveProxyRequestError serves the error page for statusCode, using a custom
// template from the web directory if present.
func serveProxyRequestError(w http.ResponseWriter, statusCode int, router *Router, templateType ErrorTemplateType) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)

	embeddedContent, templateName := templateType.getTemplateContent()

	// Try to load custom template from web directory
	if router != nil && router.Option.WebDirectory != "" {
		customTemplate, err := os.ReadFile(filepath.Join(router.Option.WebDirectory, "templates", templateName))
		if err == nil {
			w.Write(customTemplate)
			return
		}
	}

	// Fall back to embedded template
	w.Write(embeddedContent)
}
