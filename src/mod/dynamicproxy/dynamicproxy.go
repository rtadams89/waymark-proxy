package dynamicproxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	proxyproto "github.com/c0va23/go-proxyprotocol"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"

	"imuslab.com/zoraxy/mod/dynamicproxy/captcha"
	"imuslab.com/zoraxy/mod/dynamicproxy/dpcore"
	"imuslab.com/zoraxy/mod/netutils"
	"imuslab.com/zoraxy/mod/pathmatch"
)

// configureHTTP2 applies the HTTP/2 options to the TLS listener, or turns
// HTTP/2 off.
func (router *Router) configureHTTP2(srv *http.Server) error {
	if router.Option.DisableHttp2 {
		// A non-nil TLSNextProto disables HTTP/2
		srv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
		router.Option.Logger.PrintAndLog("http-proxy", "HTTP/2 disabled on TLS listener, serving HTTP/1.1 only", nil)
		return nil
	}

	h2s := &http2.Server{
		MaxConcurrentStreams:         router.Option.H2MaxConcurrentStreams,
		MaxUploadBufferPerConnection: router.Option.H2MaxUploadBufferPerConnection,
		MaxUploadBufferPerStream:     router.Option.H2MaxUploadBufferPerStream,
	}

	if h2s.MaxConcurrentStreams == 0 && h2s.MaxUploadBufferPerConnection == 0 && h2s.MaxUploadBufferPerStream == 0 {
		// No options set: Go's default HTTP/2
		return nil
	}

	return http2.ConfigureServer(srv, h2s)
}

// getAltSvcValue returns the Alt-Svc header value advertising HTTP/3, or ""
// when the QUIC listener is not active.
func (router *Router) getAltSvcValue() string {
	if !router.Option.EnableH3 || !router.Option.UseTls || router.Option.UseProxyProtocol || router.h3Server == nil {
		return ""
	}
	return `h3=":` + strconv.Itoa(router.Option.Port) + `"; ma=86400`
}

// startH3Listener starts the HTTP/3 (QUIC) listener on the TLS listener's
// port, binding the UDP socket before serving in the background. It is
// skipped with PROXY protocol on.
func (router *Router) startH3Listener(tlsConfig *tls.Config) (*http3.Server, error) {
	router.h3Conn = nil

	if !router.Option.EnableH3 {
		return nil, nil
	}

	if router.Option.UseProxyProtocol {
		router.Option.Logger.PrintAndLog("http-proxy", "HTTP/3 (QUIC) disabled: PROXY protocol is enabled and quic-go does not support PROXY protocol on QUIC listeners", nil)
		return nil, nil
	}

	// HTTP/3 ALPN on a copy of the TLS config
	h3TLSConfig := tlsConfig.Clone()
	h3TLSConfig.NextProtos = []string{"h3"}

	port := strconv.Itoa(router.Option.Port)

	srv := &http3.Server{
		Addr:      ":" + port,
		Handler:   router.mux,
		TLSConfig: h3TLSConfig,
		// 0-RTT disabled
		QUICConfig: &quic.Config{
			Allow0RTT: false,
		},
	}

	if router.Option.H3MaxConcurrentStreams > 0 {
		srv.QUICConfig.MaxIncomingStreams = int64(router.Option.H3MaxConcurrentStreams)
	}

	// Binds the UDP socket now; StopProxyService closes it
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: router.Option.Port})
	if err != nil {
		return nil, err
	}
	router.h3Conn = udpConn

	router.Option.Logger.PrintAndLog("http-proxy", "HTTP/3 (QUIC) listener starting on UDP :"+port, nil)

	go func() {
		defer udpConn.Close()
		if err := srv.Serve(udpConn); err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
			router.Option.Logger.PrintAndLog("http-proxy", "HTTP/3 listener stopped", err)
		}
	}()

	return srv, nil
}

func NewDynamicProxy(option RouterOption) (*Router, error) {
	proxyMap := sync.Map{}
	thisRouter := Router{
		Option:              &option,
		ProxyEndpoints:      &proxyMap,
		Running:             false,
		server:              nil,
		routingRules:        []*RoutingRule{},
		loadBalancer:        option.LoadBalancer,
		rateLimitCounter:    RequestCountPerIpTable{},
		captchaSessionStore: captcha.NewSessionStore(),
		secondaryServers:    make(map[string]*http.Server),
		secondaryStopChans:  make(map[string]chan bool),
	}

	thisRouter.mux = &ProxyHandler{
		Parent: &thisRouter,
	}

	return &thisRouter, nil
}

// UpdateTLSSetting sets whether TLS is used and restarts a running proxy.
func (router *Router) UpdateTLSSetting(tlsEnabled bool) {
	router.Option.UseTls = tlsEnabled
	router.restartIfRunning()
}

// SetTlsMinVersion sets the minimum TLS version and restarts a running proxy.
func (router *Router) SetTlsMinVersion(minTlsVersion uint16) {
	router.Option.MinTLSVersion = minTlsVersion
	router.restartIfRunning()
}

// UpdatePort80ListenerState turns the port 80 listener on or off and
// restarts a running proxy.
func (router *Router) UpdatePort80ListenerState(useRedirect bool) {
	router.Option.ListenOnPort80 = useRedirect
	router.restartIfRunning()
}

// GetPort80ListenerState reports whether the port 80 listener is running.
func (router *Router) GetPort80ListenerState() bool {
	if !router.Running {
		return false
	}
	return router.Option.ListenOnPort80 && router.tlsRedirectStop != nil
}

// UpdateHttpToHttpsRedirectSetting sets the HTTP to HTTPS redirect and
// restarts a running proxy.
func (router *Router) UpdateHttpToHttpsRedirectSetting(useRedirect bool) {
	router.Option.ForceHttpsRedirect = useRedirect
	router.restartIfRunning()
}

// restartIfRunning applies a listener setting: a running proxy restarts, a
// stopped one stays stopped (Restart alone would start it).
func (router *Router) restartIfRunning() {
	if router.Running {
		router.Restart()
	}
}

func (router *Router) StartProxyService() error {
	if router.server != nil {
		return errors.New("reverse proxy server already running")
	}

	if router.Root == nil {
		return errors.New("reverse proxy router root not set")
	}

	minVersion := tls.VersionTLS12
	if router.Option.MinTLSVersion != 0 {
		minVersion = int(router.Option.MinTLSVersion)
	}

	config := &tls.Config{
		GetCertificate: router.Option.TlsManager.GetCert,
		MinVersion:     uint16(minVersion),
	}

	err := router.startRateLimterCounterResetTicker()
	if err != nil {
		return err
	}
	if router.Option.UseTls {
		router.server = &http.Server{
			Addr:              ":" + strconv.Itoa(router.Option.Port),
			Handler:           router.mux,
			TLSConfig:         config,
			ReadHeaderTimeout: time.Duration(router.Option.ReadHeaderTimeout) * time.Second,
			WriteTimeout:      time.Duration(router.Option.WriteTimeout) * time.Second,
			IdleTimeout:       time.Duration(router.Option.IdleTimeout) * time.Second,
		}

		if err := router.configureHTTP2(router.server); err != nil {
			return err
		}

		router.Running = true
		if router.Option.Port != 80 && router.Option.ListenOnPort80 && !netutils.CheckIfPortOccupied(80) {
			// Port 80 listener
			httpServer := &http.Server{
				Addr: ":80",
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sep := router.GetProxyEndpointForRequest(r)
					if sep != nil && sep.BypassGlobalTLS {
						// Rules allowing plain HTTP
						router.handleNonTLSRequest(w, r, sep)
						return
					}

					if router.Option.ForceHttpsRedirect {
						protocol := "https://"
						if router.Option.Port == 443 {
							http.Redirect(w, r, protocol+r.Host+r.RequestURI, http.StatusTemporaryRedirect)
						} else {
							http.Redirect(w, r, protocol+r.Host+":"+strconv.Itoa(router.Option.Port)+r.RequestURI, http.StatusTemporaryRedirect)
						}
					} else {
						if sep != nil {
							// A rule exists but does not allow plain HTTP
							w.WriteHeader(http.StatusBadRequest)
							w.Write([]byte("400 - Bad Request"))
						} else {
							if router.Root.DefaultSiteOption == DefaultSite_NoResponse {
								hijacker, ok := w.(http.Hijacker)
								if !ok {
									w.Header().Set("Connection", "close")
									return
								}
								conn, _, err := hijacker.Hijack()
								if err != nil {
									w.Header().Set("Connection", "close")
									return
								}
								conn.Close()
							} else {
								router.mux.ServeHTTP(w, r)
							}

						}

					}

				}),
				ReadHeaderTimeout: time.Duration(router.Option.ReadHeaderTimeout) * time.Second,
				WriteTimeout:      time.Duration(router.Option.WriteTimeout) * time.Second,
				IdleTimeout:       time.Duration(router.Option.IdleTimeout) * time.Second,
			}

			router.Option.Logger.PrintAndLog("http-proxy", "Starting HTTP-to-HTTPS redirector (port 80)", nil)

			stopChan := make(chan bool)

			go func() {
				<-stopChan
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				httpServer.Shutdown(ctx)
				router.Option.Logger.PrintAndLog("http-proxy", "HTTP to HTTPS redirection listener stopped", nil)
			}()

			go func() {
				if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					// Turns port 80 off when its listener fails
					stopChan <- true
					router.Option.ListenOnPort80 = false
					router.tlsRedirectStop = nil
					router.Option.Logger.PrintAndLog("http-proxy", "Could not start HTTP-to-HTTPS redirector (port 80); disabling port 80 listener", err)
					return
				}
			}()
			router.tlsRedirectStop = stopChan
		}

		router.Option.Logger.PrintAndLog("http-proxy", "Reverse proxy service started in the background (TLS mode)", nil)
		go func(srv *http.Server) {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				router.Option.Logger.PrintAndLog("http-proxy", "Could not start proxy server (listen failed)", err)
				return
			}

			var finalListener net.Listener
			if router.Option.UseProxyProtocol {
				finalListener = proxyproto.NewDefaultListener(ln)
				router.Option.Logger.PrintAndLog("http-proxy", "PROXY protocol v1/v2 enabled for TLS listener", nil)
			} else {
				finalListener = ln
			}

			if err := srv.ServeTLS(finalListener, "", ""); err != nil && err != http.ErrServerClosed {
				router.Option.Logger.PrintAndLog("http-proxy", "Could not start proxy server", err)
			}

		}(router.server)

		h3Srv, err := router.startH3Listener(config)
		if err != nil {
			router.Option.Logger.PrintAndLog("http-proxy", "Could not start HTTP/3 listener", err)
		}
		router.h3Server = h3Srv
	} else {
		// Without TLS
		router.tlsListener = nil
		if router.Option.EnableH3 {
			router.Option.Logger.PrintAndLog("http-proxy", "HTTP/3 (QUIC) requires TLS; skipping QUIC listener in plain HTTP mode", nil)
		}
		router.server = &http.Server{
			Addr:              ":" + strconv.Itoa(router.Option.Port),
			Handler:           router.mux,
			ReadHeaderTimeout: time.Duration(router.Option.ReadHeaderTimeout) * time.Second,
			WriteTimeout:      time.Duration(router.Option.WriteTimeout) * time.Second,
			IdleTimeout:       time.Duration(router.Option.IdleTimeout) * time.Second,
		}
		router.Running = true
		router.Option.Logger.PrintAndLog("http-proxy", "Reverse proxy service started in the background (Plain HTTP mode)", nil)

		go func(srv *http.Server) {
			srv.ListenAndServe()
		}(router.server)
	}

	router.startSecondaryListeners()

	return nil
}

// handleNonTLSRequest handles plain HTTP requests for rules that allow them
// (port 80 and secondary listeners).
func (router *Router) handleNonTLSRequest(w http.ResponseWriter, r *http.Request, sep *ProxyEndpoint) {
	// ACL
	if router.accessRefused(w, r, sep) {
		return
	}

	// Rate Limit
	if sep.RequireRateLimit {
		if err := router.handleRateLimit(w, r, sep); err != nil {
			return
		}
	}

	// CAPTCHA Gating
	if handled, _ := router.handleCaptchaGating(w, r, sep); handled {
		return
	}

	proxyHandler := router.mux.(*ProxyHandler)
	if handleAuthProviderRouting(sep, w, r, proxyHandler) {
		return
	}

	proxyingPath := strings.TrimSpace(r.RequestURI)
	targetProxyEndpoint := sep.GetVirtualDirectoryHandlerFromRequestURI(proxyingPath)
	if targetProxyEndpoint != nil && !targetProxyEndpoint.Disabled {
		router.mux.(*ProxyHandler).vdirRequest(w, r, targetProxyEndpoint)
		return
	} else if !strings.HasSuffix(proxyingPath, "/") && sep.ProxyType != ProxyTypeRoot {
		potentialProxtEndpoint := sep.GetVirtualDirectoryHandlerFromRequestURI(proxyingPath + "/")
		if potentialProxtEndpoint != nil && !potentialProxtEndpoint.Disabled {
			// Adds the missing trailing slash
			http.Redirect(w, r, r.RequestURI+"/", http.StatusTemporaryRedirect)
			if !sep.DisableLogging {
				clientIP := router.GetClientIPForEndpoint(r, sep)
				router.Option.Logger.LogHTTPRequest(r, "redirect", 307, r.Host, "", clientIP)
			}
			return
		}
	}

	// Maintenance mode
	if router.serveMaintenance(w, r, sep) {
		return
	}

	// Every upstream down: error page
	if proxyHandler.allUpstreamsDown(sep) {
		serveProxyRequestError(w, 521, router, ErrorTemplateRPError)
		router.logRequest(r, false, 521, "host-http", r.Host, "health-check-down", sep)
		return
	}

	// Static file cache
	cacheSession := proxyHandler.assetCacheBegin(w, r, sep)
	if cacheSession != nil {
		if cacheSession.Served() {
			return
		}
		w, r = cacheSession, cacheSession.UpstreamRequest()
		defer cacheSession.Finish()
	}

	selectedUpstream, err := router.loadBalancer.GetRequestUpstreamTarget(w, r, sep.ActiveOrigins, sep.UseStickySession, autoFallbackOff(sep), sep.LoadBalancingMode)
	if err != nil {
		serveProxyRequestError(w, 404, router, ErrorTemplateHostError)
		router.Option.Logger.PrintAndLog("http-proxy", "failed to get destination for hostname", err)
		router.logRequest(r, false, 404, "vdir-http", r.Host, "", sep)
		return
	}
	// Counts the request for least connections balancing
	defer router.loadBalancer.Track(selectedUpstream.OriginIpOrDomain)()

	if isWebSocketRequest(r) {
		proxyHandler.hostWebSocketRequest(w, r, sep, selectedUpstream)
		return
	}

	// The Host is rewritten after authentication and routing
	originalHostHeader := r.Host
	if r.URL != nil {
		r.Host = r.URL.Host
	} else {
		r.URL, _ = url.Parse(originalHostHeader)
	}

	endpointProxyRewriteRules := GetDefaultHeaderRewriteRules()
	if sep.HeaderRewriteRules != nil {
		endpointProxyRewriteRules = sep.HeaderRewriteRules
	}

	statusCode, err := selectedUpstream.ServeHTTP(w, r, &dpcore.ResponseRewriteRuleSet{
		ProxyDomain:             selectedUpstream.OriginIpOrDomain,
		OriginalHost:            originalHostHeader,
		UseTLS:                  selectedUpstream.RequireTLS,
		HostHeaderOverwrite:     endpointProxyRewriteRules.RequestHostOverwrite,
		NoRemoveHopByHop:        endpointProxyRewriteRules.DisableHopByHopHeaderRemoval,
		NoRemoveUserAgentHeader: endpointProxyRewriteRules.DisableUserAgentHeaderRemoval,
		AllowConnect:            sep.EnableConnectSupport,
		AllowUpgrade:            sep.EnableUpgradeForwarding,
		PathPrefix:              "",
		Version:                 sep.parent.Option.HostVersion,
		DevelopmentMode:         sep.parent.Option.DevelopmentMode,
		AltSvc:                  router.getAltSvcValue(),
	})
	// Passive health monitoring
	if observer := router.Option.UpstreamResultObserver; observer != nil {
		observer(sep.RootOrMatchingDomain, selectedUpstream.OriginIpOrDomain, statusCode, err)
	}
	cacheSession.SetUpstreamResult(statusCode, err)
}

func (router *Router) handleCaptchaGating(w http.ResponseWriter, r *http.Request, sep *ProxyEndpoint) (handled bool, challenged bool) {
	if !sep.RequireCaptcha || !sep.CaptchaConfig.IsConfigured() {
		return false, false
	}

	// Client IP resolved with the rule's trusted proxy setting
	clientIP := router.GetClientIPForEndpoint(r, sep)

	if r.URL.Path == captcha.VerifyPath {
		captcha.HandleVerification(w, r, sep.CaptchaConfig, router.captchaSessionStore, clientIP)
		return true, false
	}

	if !captcha.ShouldEnforcePath(pathmatch.RequestTarget(r), sep.CaptchaConfig) {
		return false, false
	}

	if captcha.CheckException(r, sep.CaptchaConfig.ExceptionRules, clientIP) || captcha.CheckSession(r, router.captchaSessionStore) {
		return false, false
	}

	domain := r.Host
	if domain == "" {
		domain = sep.RootOrMatchingDomain
	}

	captcha.RenderChallenge(w, r, sep.CaptchaConfig, domain, router.Option.WebDirectory)
	return true, true
}

func (router *Router) startSecondaryListeners() {
	commonPorts := router.GetCommonListeningPorts()

	for addr, domains := range commonPorts {
		router.secondaryServerMutex.RLock()
		_, exists := router.secondaryServers[addr]
		router.secondaryServerMutex.RUnlock()

		if exists {
			continue
		}

		router.Option.Logger.PrintAndLog("http-proxy", "Starting secondary listener on "+addr+" for domains: "+strings.Join(domains, ", "), nil)

		secondaryServer := &http.Server{
			Addr: addr,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				domainOnly := requestHostname(r.Host)

				sep := router.GetProxyEndpointFromHostname(domainOnly)

				if sep != nil && router.isDomainListeningOnPort(sep, addr) {
					router.handleNonTLSRequest(w, r, sep)
					return
				}

				// No rule for this host and port: no response or 404
				if router.Root.DefaultSiteOption == DefaultSite_NoResponse {
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						w.Header().Set("Connection", "close")
						return
					}
					conn, _, err := hijacker.Hijack()
					if err != nil {
						w.Header().Set("Connection", "close")
						return
					}
					conn.Close()
				} else {
					http.NotFound(w, r)
				}
			}),
			ReadHeaderTimeout: time.Duration(router.Option.ReadHeaderTimeout) * time.Second,
			WriteTimeout:      time.Duration(router.Option.WriteTimeout) * time.Second,
			IdleTimeout:       time.Duration(router.Option.IdleTimeout) * time.Second,
		}

		stopChan := make(chan bool)

		router.secondaryServerMutex.Lock()
		router.secondaryServers[addr] = secondaryServer
		router.secondaryStopChans[addr] = stopChan
		router.secondaryServerMutex.Unlock()

		go func(addr string) {
			<-stopChan
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			secondaryServer.Shutdown(ctx)
		}(addr)

		go func(addr string) {
			if err := secondaryServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				router.Option.Logger.PrintAndLog("http-proxy", "Secondary listener on "+addr+" failed", err)
			}
		}(addr)
	}
}

func (router *Router) isDomainListeningOnPort(endpoint *ProxyEndpoint, addr string) bool {
	if endpoint == nil || endpoint.ListeningPorts == nil {
		return false
	}

	for _, port := range endpoint.ListeningPorts {
		if strings.TrimSpace(port) == addr {
			return true
		}
	}

	return false
}

// UpdateSecondaryListeners starts and stops secondary listeners to match
// the rules' listening ports.
func (router *Router) UpdateSecondaryListeners() {
	if !router.Running {
		return
	}

	newCommonPorts := router.GetCommonListeningPorts()

	router.secondaryServerMutex.Lock()
	for addr := range router.secondaryServers {
		if _, exists := newCommonPorts[addr]; !exists {
			if stopChan, ok := router.secondaryStopChans[addr]; ok {
				stopChan <- true
			}
			delete(router.secondaryServers, addr)
			delete(router.secondaryStopChans, addr)
			router.Option.Logger.PrintAndLog("http-proxy", "Stopped secondary listener on "+addr, nil)
		}
	}
	router.secondaryServerMutex.Unlock()

	router.startSecondaryListeners()
}

// StopProxyService stops the proxy server and waits for all listeners to close
func (router *Router) StopProxyService() error {
	if router.server == nil && router.tlsListener == nil && router.tlsRedirectStop == nil && router.h3Server == nil && len(router.secondaryServers) == 0 {
		return errors.New("reverse proxy server already stopped")
	}

	var wg sync.WaitGroup

	// Stop main TLS/HTTP server
	if router.server != nil {
		wg.Add(1)
		go func(srv *http.Server) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := srv.Shutdown(ctx)
			if err != nil {
				router.Option.Logger.PrintAndLog("http-proxy", "Graceful shutdown timeout, forcing close", err)
				// Closes when graceful shutdown times out
				srv.Close()
			}
		}(router.server)
	}

	// Stop HTTP/3 (QUIC) listener
	if router.h3Server != nil {
		wg.Add(1)
		go func(srv *http3.Server, conn net.PacketConn) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := srv.Shutdown(ctx)
			if err != nil {
				router.Option.Logger.PrintAndLog("http-proxy", "HTTP/3 graceful shutdown timeout, forcing close", err)
				srv.Close()
			}
			// Closes the UDP connection to end Serve
			if conn != nil {
				conn.Close()
			}
		}(router.h3Server, router.h3Conn)
	}

	// Stop TLS redirect server
	if router.tlsRedirectStop != nil {
		wg.Add(1)
		go func(ch chan bool) {
			defer wg.Done()
			ch <- true
		}(router.tlsRedirectStop)
	}

	// Stop all secondary listening servers
	router.secondaryServerMutex.Lock()
	for addr, stopChan := range router.secondaryStopChans {
		wg.Add(1)
		go func(address string, ch chan bool) {
			defer wg.Done()
			ch <- true
			router.Option.Logger.PrintAndLog("http-proxy", "Secondary listener on "+address+" stopped", nil)
		}(addr, stopChan)
	}
	router.secondaryServerMutex.Unlock()

	// Stop rate limiter ticker if exists
	if router.rateLimterStop != nil {
		wg.Add(1)
		go func(ch chan bool) {
			defer wg.Done()
			ch <- true
		}(router.rateLimterStop)
	}

	wg.Wait()
	router.server = nil
	router.tlsListener = nil
	router.tlsRedirectStop = nil
	router.rateLimterStop = nil
	router.h3Server = nil
	router.h3Conn = nil

	router.secondaryServerMutex.Lock()
	router.secondaryServers = make(map[string]*http.Server)
	router.secondaryStopChans = make(map[string]chan bool)
	router.secondaryServerMutex.Unlock()

	router.Running = false

	router.Option.Logger.PrintAndLog("http-proxy", "Proxy service stopped successfully", nil)

	return nil
}

func (router *Router) Restart() error {
	if router.restarting {
		return errors.New("proxy server is restarting in progress")
	}
	router.restarting = true
	defer func() {
		router.restarting = false
	}()
	if router.Running {

		router.Option.Logger.PrintAndLog("http-proxy", "Restarting proxy server...", nil)
		if err := router.StopProxyService(); err != nil {
			return err
		}
		// Waits for the ports to be released
		time.Sleep(200 * time.Millisecond)
	}

	if err := router.StartProxyService(); err != nil {
		router.Option.Logger.PrintAndLog("http-proxy", "Failed to restart proxy server", err)
		return err
	}

	router.Option.Logger.PrintAndLog("http-proxy", "Proxy server restarted successfully", nil)
	return nil
}

func (router *Router) IsProxiedSubdomain(r *http.Request) bool {
	hostname := r.Header.Get("X-Forwarded-Host")
	if hostname == "" {
		hostname = r.Host
	}
	hostname = strings.Split(hostname, ":")[0]
	subdEndpoint := router.GetProxyEndpointFromHostname(hostname)
	return subdEndpoint != nil
}

// LoadProxy returns the rule for a matching domain.
func (router *Router) LoadProxy(matchingDomain string) (*ProxyEndpoint, error) {
	var targetProxyEndpoint *ProxyEndpoint
	router.ProxyEndpoints.Range(func(key, value interface{}) bool {
		key, ok := key.(string)
		if !ok {
			return true
		}
		v, ok := value.(*ProxyEndpoint)
		if !ok {
			return true
		}

		if key == strings.ToLower(matchingDomain) {
			targetProxyEndpoint = v
		}
		return true
	})

	if targetProxyEndpoint == nil {
		return nil, errors.New("target routing rule not found")
	}

	return targetProxyEndpoint, nil
}

// CopyEndpoint returns a deep copy of a rule without its runtime fields.
func CopyEndpoint(endpoint *ProxyEndpoint) *ProxyEndpoint {
	js, _ := json.Marshal(endpoint)
	newProxyEndpoint := ProxyEndpoint{}
	err := json.Unmarshal(js, &newProxyEndpoint)
	if err != nil {
		return nil
	}
	newProxyEndpoint.InitializeExploitDetector()
	return &newProxyEndpoint
}

func (r *Router) GetProxyEndpointsAsMap() map[string]*ProxyEndpoint {
	m := make(map[string]*ProxyEndpoint)
	r.ProxyEndpoints.Range(func(key, value interface{}) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}
		v, ok := value.(*ProxyEndpoint)
		if !ok {
			return true
		}
		m[k] = v
		return true
	})
	return m
}

// GetCommonListeningPorts returns the listening addresses of enabled rules
// (e.g. ":8080") with the domains using each.
func (r *Router) GetCommonListeningPorts() map[string][]string {
	portMap := make(map[string][]string)

	r.ProxyEndpoints.Range(func(key, value interface{}) bool {
		domain, ok := key.(string)
		if !ok {
			return true
		}
		endpoint, ok := value.(*ProxyEndpoint)
		if !ok {
			return true
		}

		if endpoint.Disabled {
			return true
		}

		if endpoint.ListeningPorts != nil {
			for _, port := range endpoint.ListeningPorts {
				port = strings.TrimSpace(port)
				if port != "" {
					portMap[port] = append(portMap[port], domain)
				}
			}
		}

		return true
	})

	return portMap
}
