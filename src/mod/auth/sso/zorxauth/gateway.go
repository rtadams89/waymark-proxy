package zorxauth

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp/totp"
	"imuslab.com/zoraxy/mod/netutils"
)

// validateTOTPCode validates a TOTP code against secret.
func validateTOTPCode(code, secret string) bool {
	return totp.Validate(code, secret)
}

//go:embed auth.html
var authPageHTML []byte

//go:embed noaccess.html
var noAccessHTML []byte

//go:embed logout.html
var logoutHTML []byte

//go:embed favicon.png
var faviconPNG []byte

//go:embed wallpaper.svg
var wallpaperSVG []byte

//go:embed logo_white.svg
var logoWhiteSVG []byte

// BRANDING_ASSET_PATH is the URL prefix of the login page's customizable
// resources.
const BRANDING_ASSET_PATH = "/assets/branding/"

// GatewayServer represents the authentication gateway HTTP server
type GatewayServer struct {
	server *http.Server
	router *AuthRouter
	mu     sync.RWMutex
	mux    *http.ServeMux
}

// NewGatewayServer creates a new gateway server instance
func NewGatewayServer(router *AuthRouter) *GatewayServer {
	mux := http.NewServeMux()
	gs := &GatewayServer{
		router: router,
		mux:    mux,
	}

	mux.HandleFunc("/", gs.handleAuthPage)
	mux.HandleFunc("/noaccess", gs.handleNoAccessPage)
	mux.HandleFunc("/favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		w.Write(faviconPNG)
	})
	// Branding assets: the upload if one exists, otherwise the embedded default
	mux.HandleFunc(BRANDING_ASSET_PATH, func(w http.ResponseWriter, r *http.Request) {
		assetKey := strings.TrimPrefix(r.URL.Path, BRANDING_ASSET_PATH)
		if _, ok := assetSpecs[assetKey]; !ok {
			http.NotFound(w, r)
			return
		}
		gs.router.ServeBrandingAsset(w, r, assetKey)
	})
	mux.HandleFunc("/login", gs.handleLogin)
	mux.HandleFunc("/logout", gs.handleLogout)
	mux.HandleFunc("/totp-verify", gs.handleTOTPVerify)
	mux.HandleFunc("/passkey/auth/begin", gs.handlePasskeyAuthBegin)
	mux.HandleFunc("/passkey/auth/complete", gs.handlePasskeyAuthComplete)
	mux.HandleFunc("/user", gs.handleUserPortal)
	mux.HandleFunc("/user/api/", gs.handleUserPortalAPI)

	return gs
}

// Start starts the gateway server on the configured port
func (gs *GatewayServer) Start() error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if gs.server != nil {
		return fmt.Errorf("gateway server is already running")
	}

	if !gs.router.Options.EnableAuthGateway {
		return fmt.Errorf("authentication gateway is disabled")
	}

	port := gs.router.Options.GatewayPort
	if port == 0 {
		port = 5489
	}

	gs.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      gs.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if gs.router.Logger != nil {
			gs.router.Logger.PrintAndLog("waymark-auth", fmt.Sprintf("Starting authentication gateway on port %d", port), nil)
		}

		if err := gs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if gs.router.Logger != nil {
				gs.router.Logger.PrintAndLog("waymark-auth", fmt.Sprintf("Gateway server error: %v", err), err)
			}
		}
	}()

	return nil
}

// Stop stops the gateway server
func (gs *GatewayServer) Stop() error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if gs.server == nil {
		return fmt.Errorf("gateway server is not running")
	}

	if gs.router.Logger != nil {
		gs.router.Logger.PrintAndLog("waymark-auth", "Stopping authentication gateway", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := gs.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown gateway server: %w", err)
	}

	gs.server = nil
	return nil
}

// Restart restarts the gateway server
func (gs *GatewayServer) Restart() error {
	if gs.router.Logger != nil {
		gs.router.Logger.PrintAndLog("waymark-auth", "Restarting authentication gateway", nil)
	}

	gs.mu.RLock()
	isRunning := gs.server != nil
	gs.mu.RUnlock()

	if isRunning {
		if err := gs.Stop(); err != nil {
			return fmt.Errorf("failed to stop gateway during restart: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	return gs.Start()
}

// IsRunning returns whether the gateway server is currently running
func (gs *GatewayServer) IsRunning() bool {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return gs.server != nil
}

/* Authentication gateway handlers */

// handleAuthPage serves the authentication/login page
func (gs *GatewayServer) handleAuthPage(w http.ResponseWriter, r *http.Request) {
	if gs.ServeGatewayDisabled(w, r) {
		return
	}

	// With a valid SSO portal session, redirects to the target domain's
	// session-set URL with a one-time validation code
	authenticated, username := gs.router.RequestIsAuthenticatedInSSO(w, r)
	if authenticated {
		redirectURL := r.URL.Query().Get("redirect")
		if redirectURL == "" {
			redirectURL = "/user" // user portal
			http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
			return
		}

		host := ""
		parsedRedirectURL, err := url.Parse(redirectURL)
		if err == nil {
			host = parsedRedirectURL.Hostname()
		} else {
			if gs.router.Logger != nil {
				gs.router.Logger.PrintAndLog("waymark-auth", fmt.Sprintf("Error parsing redirect URL: %v", err), err)
			}
		}

		if gs.router.ValidateUserAccessToHost(username, host) {
			// Extends the gateway session expiry
			cookie, cookieErr := r.Cookie(gs.router.Options.CookieName)
			if cookieErr == nil && cookie.Value != "" {
				if sessionData, exists := gs.router.gatewaySessionStore.Load(cookie.Value); exists {
					if gatewaySession, ok := sessionData.(*GatewaySession); ok {
						newExpiry := time.Now().Add(time.Duration(gs.router.Options.CookieDuration) * time.Second)
						gatewaySession.Expiry = newExpiry
						gs.router.gatewaySessionStore.Store(cookie.Value, gatewaySession)
					}
				}
			}

			sessionId := gs.router.generateValidationCodeForSession(username)
			parsedTarget, parseErr := url.Parse(redirectURL)
			if parseErr == nil && parsedTarget.Host != "" {
				host := parsedTarget.Hostname()
				port := parsedTarget.Port()
				targetProtocol := parsedTarget.Scheme
				if targetProtocol == "" {
					targetProtocol = "http"
				}
				hostWithPort := host
				if port != "" {
					hostWithPort = host + ":" + port
				}
				sessionSetURL := fmt.Sprintf("%s://%s/%s?session_id=%s&redirect=%s",
					targetProtocol, hostWithPort,
					strings.TrimPrefix(gs.router.Options.SSOSessionSetURL, "/"),
					sessionId, url.QueryEscape(redirectURL))
				http.Redirect(w, r, sessionSetURL, http.StatusSeeOther)
				return
			}
		} else {
			// No access to this host: no-access page
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write(noAccessHTML)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(gs.router.RenderAuthPage())
}

// handleLogin processes login requests
func (gs *GatewayServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if gs.ServeGatewayDisabled(w, r) {
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Method not allowed",
		})
		return
	}

	clientIP := netutils.GetRequesterIPUntrusted(r)

	// Per-minute attempt limit per IP
	if gs.router.Options.EnableRateLimit && gs.router.Options.RateLimitPerIp > 0 {
		if gs.router.getLoginAttemptCount(clientIP) >= int64(gs.router.Options.RateLimitPerIp) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Too many login attempts. Please try again later.",
			})
			return
		}
	}
	gs.router.incrementLoginAttempt(clientIP)

	username := r.FormValue("username")
	password := r.FormValue("password")

	if !gs.router.ValidateUsername(username, password) {
		// Exponential backoff on failed attempts
		if gs.router.Options.UseExpotentialBackoff {
			failures := gs.router.incrementLoginFailure(clientIP)
			// 100ms * 2^(failures-1), at most 30 s
			delay := 100 * time.Millisecond * (1 << uint(failures-1))
			const maxDelay = 30 * time.Second
			if delay > maxDelay || delay < 0 {
				delay = maxDelay
			}
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid username or password",
		})
		return
	}

	gs.router.loginFailureCounter.Delete(clientIP)

	rawRedirect := r.FormValue("redirect")
	redirectTarget := rawRedirect
	isDirectLogin := redirectTarget == "/"
	if redirectTarget == "/" {
		protocolScheme := "http"
		if r.URL.Scheme != "" {
			protocolScheme = r.URL.Scheme
		}
		redirectTarget = protocolScheme + "://" + r.Host + "/user" // user portal
	}

	parsedTarget, err := url.Parse(redirectTarget)

	if err != nil || parsedTarget.Host == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid redirect target",
		})
		return
	}

	host := parsedTarget.Hostname()
	port := parsedTarget.Port()

	targetProtocol := parsedTarget.Scheme
	if targetProtocol == "" {
		targetProtocol = "http"
	}

	u, err := gs.router.getUserByUsernameOrEmail(username)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Internal server error",
		})
		return
	}

	if !gs.router.ValidateUserAccessToHost(u.Username, host) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Access to the requested host is not allowed for this user",
		})
		return
	}

	rememberMe := false
	if r.FormValue("remember_me") == "on" || strings.EqualFold(r.FormValue("remember_me"), "true") {
		rememberMe = true
	}
	if r.FormValue("rememberMe") == "on" || strings.EqualFold(r.FormValue("rememberMe"), "true") {
		rememberMe = true
	}

	// With 2FA, issues a short-lived pending TOTP session
	if u.Enable2FA {
		totpToken := gs.router.generateSessionToken()
		gs.router.pendingTOTPSessions.Store(totpToken, &PendingTOTPSession{
			Username:       u.Username,
			Host:           host,
			Port:           port,
			Protocol:       targetProtocol,
			RedirectTarget: redirectTarget,
			IsDirectLogin:  isDirectLogin,
			RememberMe:     rememberMe,
			Expiry:         time.Now().Add(5 * time.Minute),
		})
		time.AfterFunc(5*time.Minute, func() {
			gs.router.pendingTOTPSessions.Delete(totpToken)
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":      false,
			"require_totp": true,
			"totp_token":   totpToken,
		})
		return
	}

	// Gateway session cookie
	cookieDuration := gs.router.Options.CookieDuration
	if rememberMe {
		cookieDuration = gs.router.Options.CookieDurationRememberMe
	}
	isSecure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")

	sessionToken := gs.router.generateSessionToken()
	expiryTime := time.Now().Add(time.Duration(cookieDuration) * time.Second)
	gatewaySession := &GatewaySession{
		Username: u.Username,
		Expiry:   expiryTime,
	}
	gs.router.gatewaySessionStore.Store(sessionToken, gatewaySession)

	http.SetCookie(w, &http.Cookie{
		Name:     gs.router.Options.CookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cookieDuration,
	})

	// Removes the session from the store when the cookie expires
	time.AfterFunc(time.Duration(cookieDuration)*time.Second, func() {
		gs.router.gatewaySessionStore.Delete(sessionToken)
	})

	// Direct login (no redirect target): sends the user to the user portal
	if isDirectLogin {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"redirectTarget": "/user",
		})
		return
	}

	sessionId := gs.router.generateValidationCodeForSession(u.Username)
	hostWithPort := host
	if port != "" {
		hostWithPort = host + ":" + port
	}
	sessionSetURL := fmt.Sprintf("%s://%s/%s", targetProtocol, hostWithPort, strings.TrimPrefix(gs.router.Options.SSOSessionSetURL, "/"))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"sessionId":      sessionId,
		"redirectTarget": redirectTarget,
		"sessionSetURL":  sessionSetURL,
		"rememberMe":     rememberMe,
	})
}

// handleNoAccessPage serves the access-denied page when an authenticated user
// does not have permission for the requested host.
func (gs *GatewayServer) handleNoAccessPage(w http.ResponseWriter, r *http.Request) {
	if gs.ServeGatewayDisabled(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	w.Write(noAccessHTML)
}

// handleLogout: GET serves the logout page; POST ends all of the user's
// sessions and clears the cookie.
func (gs *GatewayServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if gs.ServeGatewayDisabled(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		authenticated, username := gs.router.RequestIsAuthenticatedInSSO(w, r)
		if !authenticated {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("Unauthorized"))
			return
		}

		pageContent := strings.Replace(string(logoutHTML), "{{username}}", username, -1)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(pageContent))
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authenticated, username := gs.router.RequestIsAuthenticatedInSSO(w, r)
	if !authenticated || username == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "No active session",
		})
		return
	}

	cookie, err := r.Cookie(gs.router.Options.CookieName)
	if err == nil && cookie.Value != "" {
		gs.router.gatewaySessionStore.Delete(cookie.Value)
	}

	// Removes all browser sessions of this user
	gs.router.cookieIdStore.Range(func(key, value interface{}) bool {
		sessionID, ok := key.(string)
		if !ok {
			return true
		}

		browserSession, ok := value.(*BrowserSession)
		if !ok {
			return true
		}

		if browserSession.Username == username {
			gs.router.cookieIdStore.Delete(sessionID)
		}

		return true
	})

	isSecure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     gs.router.Options.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

func (gs *GatewayServer) ServeGatewayDisabled(w http.ResponseWriter, r *http.Request) bool {
	if !gs.router.Options.EnableAuthGateway {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Authentication gateway is disabled"))
		return true
	}
	return false
}

// handleTOTPVerify completes a 2FA login with the posted totp_token and code.
func (gs *GatewayServer) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	if gs.ServeGatewayDisabled(w, r) {
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Method not allowed",
		})
		return
	}

	totpToken := r.FormValue("totp_token")
	totpCode := r.FormValue("totp_code")

	if totpToken == "" || totpCode == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "totp_token and totp_code are required",
		})
		return
	}

	pendingObj, exists := gs.router.pendingTOTPSessions.Load(totpToken)
	if !exists {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid or expired TOTP session; please log in again",
		})
		return
	}

	pending, ok := pendingObj.(*PendingTOTPSession)
	if !ok || time.Now().After(pending.Expiry) {
		gs.router.pendingTOTPSessions.Delete(totpToken)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "TOTP session expired; please log in again",
		})
		return
	}

	// The pending session is used once
	gs.router.pendingTOTPSessions.Delete(totpToken)

	u, err := gs.router.getUserByUsername(pending.Username)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Internal server error",
		})
		return
	}

	if !validateTOTPCode(totpCode, u.TOTPSecret) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid authenticator code",
		})
		return
	}

	cookieDuration := gs.router.Options.CookieDuration
	if pending.RememberMe {
		cookieDuration = gs.router.Options.CookieDurationRememberMe
	}
	isSecure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")

	sessionToken := gs.router.generateSessionToken()
	expiryTime := time.Now().Add(time.Duration(cookieDuration) * time.Second)
	gs.router.gatewaySessionStore.Store(sessionToken, &GatewaySession{
		Username: u.Username,
		Expiry:   expiryTime,
	})
	time.AfterFunc(time.Duration(cookieDuration)*time.Second, func() {
		gs.router.gatewaySessionStore.Delete(sessionToken)
	})

	http.SetCookie(w, &http.Cookie{
		Name:     gs.router.Options.CookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cookieDuration,
	})

	// Direct login (no redirect target): sends the user to the user portal
	if pending.IsDirectLogin {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"redirectTarget": "/user",
		})
		return
	}

	sessionId := gs.router.generateValidationCodeForSession(u.Username)
	hostWithPort := pending.Host
	if pending.Port != "" {
		hostWithPort = pending.Host + ":" + pending.Port
	}
	sessionSetURL := fmt.Sprintf("%s://%s/%s", pending.Protocol, hostWithPort,
		strings.TrimPrefix(gs.router.Options.SSOSessionSetURL, "/"))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"sessionId":      sessionId,
		"redirectTarget": pending.RedirectTarget,
		"sessionSetURL":  sessionSetURL,
		"rememberMe":     pending.RememberMe,
	})
}
