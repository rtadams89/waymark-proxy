package zorxauth

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

//go:embed invalid.html
var invalidHTML []byte

func (ar *AuthRouter) RequestIsAuthenticatedInHost(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(ar.Options.CookieName)
	if err != nil {
		return false
	}

	sessionData, exists := ar.cookieIdStore.Load(cookie.Value)
	if !exists {
		return false
	}

	browserSession, ok := sessionData.(*BrowserSession)
	if !ok {
		return false
	}

	if time.Now().After(browserSession.Expiry) {
		ar.cookieIdStore.Delete(cookie.Value)
		return false
	}

	return true
}

func (ar *AuthRouter) RequestIsAuthenticatedInSSO(w http.ResponseWriter, r *http.Request) (bool, string) {
	cookie, err := r.Cookie(ar.Options.CookieName)
	if err != nil {
		return false, ""
	}

	sessionData, exists := ar.gatewaySessionStore.Load(cookie.Value)
	if !exists {
		return false, ""
	}

	gatewaySession, ok := sessionData.(*GatewaySession)
	if !ok {
		return false, ""
	}

	if time.Now().After(gatewaySession.Expiry) {
		username := gatewaySession.Username

		ar.gatewaySessionStore.Delete(cookie.Value)

		// Removes all browser sessions of this user
		ar.cookieIdStore.Range(func(key, value interface{}) bool {
			sessionID, ok := key.(string)
			if !ok {
				return true
			}

			browserSession, ok := value.(*BrowserSession)
			if !ok {
				return true
			}

			if browserSession.Username == username {
				ar.cookieIdStore.Delete(sessionID)
			}

			return true
		})

		return false, ""
	}

	return true, gatewaySession.Username
}

// generateSessionToken returns a random browser session token.
func (ar *AuthRouter) generateSessionToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fallback := time.Now().Format("20060102150405.000000000")
		return hex.EncodeToString([]byte(fallback))
	}
	return hex.EncodeToString(buf)
}

// handleSessionSetCallback checks the SSO validation code, sets the session
// cookie for this domain and redirects to the original URL.
func (ar *AuthRouter) handleSessionSetCallback(w http.ResponseWriter, r *http.Request) error {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		w.Write(invalidHTML)
		return errors.New("missing session_id in callback")
	}

	redirectURL := r.URL.Query().Get("redirect")
	if redirectURL == "" {
		redirectURL = "/"
	}

	usernameObj, exists := ar.sessionIdStore.Load(sessionID)
	if !exists {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(invalidHTML)
		return errors.New("validation session not found or expired")
	}

	username, ok := usernameObj.(string)
	if !ok || username == "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(invalidHTML)
		return errors.New("validation session is invalid")
	}

	// The validation code is used once
	ar.sessionIdStore.Delete(sessionID)

	browserSessionID := ar.generateSessionToken()
	rememberMe := strings.EqualFold(r.URL.Query().Get("remember_me"), "true") || r.URL.Query().Get("remember_me") == "on"
	cookieDuration := ar.Options.CookieDuration
	if rememberMe {
		cookieDuration = ar.Options.CookieDurationRememberMe
	}
	if cookieDuration <= 0 {
		if rememberMe {
			cookieDuration = getDefaultOptions().CookieDurationRememberMe
		} else {
			cookieDuration = getDefaultOptions().CookieDuration
		}
	}

	expiryTime := time.Now().Add(time.Duration(cookieDuration) * time.Second)
	browserSession := &BrowserSession{
		Username: username,
		Expiry:   expiryTime,
	}
	ar.cookieIdStore.Store(browserSessionID, browserSession)

	// Removes the session when it expires
	if cookieDuration > 0 {
		time.AfterFunc(time.Duration(cookieDuration)*time.Second, func() {
			ar.cookieIdStore.Delete(browserSessionID)
		})
	}

	isSecure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     ar.Options.CookieName,
		Value:    browserSessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   cookieDuration,
	})

	// Redirects only to this host
	http.Redirect(w, r, sameHostRedirect(redirectURL, r), http.StatusSeeOther)
	return errors.New("session callback handled")
}

// isAjaxRequest detects if the request is an AJAX/fetch request
func isAjaxRequest(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		return true
	}

	if r.Header.Get("Sec-Fetch-Mode") == "cors" {
		return true
	}

	return false
}

// setAuthHeaders sets the user headers passed to the backend.
func (ar *AuthRouter) setAuthHeaders(r *http.Request, user *User) {
	r.Header.Set("X-Auth-Method", "waymark_sso")
	r.Header.Set("Remote-User", user.Username) //Authelia compatibility
	r.Header.Set("Remote-Name", user.Username) //Authelia compatibility
	r.Header.Del("Remote-Groups")
	if user.Email != "" {
		r.Header.Set("Remote-Email", user.Email) //Authelia compatibility
	} else {
		r.Header.Del("Remote-Email")
	}
}

// buildLoginRedirectURL returns the absolute SSO login URL for an
// unauthenticated request.
func (ar *AuthRouter) buildLoginRedirectURL(r *http.Request) (string, error) {
	if ar.Options.SSORedirectURL == "" {
		return "", errors.New("SSO Redirect URL is not set")
	}

	scheme := requestScheme(r)
	ssoBase, err := ensureAbsoluteHTTPURL(ar.Options.SSORedirectURL, scheme)
	if err != nil {
		return "", errors.New("SSO Redirect URL is invalid: " + err.Error())
	}
	if ssoRedirectWouldLoop(ssoBase, r) {
		return "", errors.New("SSO Redirect URL resolves to the same host as the protected request")
	}

	originalURL := buildOriginalRequestURL(r)
	return buildSSOAuthRedirect(ssoBase, originalURL)
}

// HandleAuthRouting redirects requests not authenticated on this site to the
// SSO login page.
func (ar *AuthRouter) HandleAuthRouting(w http.ResponseWriter, r *http.Request) error {
	if strings.TrimRight(r.URL.Path, "/") == strings.TrimRight(ar.Options.SSOSessionSetURL, "/") {
		// Session-set callback: sets the cookie and redirects to the original URL
		return ar.handleSessionSetCallback(w, r)
	}

	if !ar.RequestIsAuthenticatedInHost(w, r) {
		redirectURL, err := ar.buildLoginRedirectURL(r)
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write(invalidHTML)
			return err
		}

		// AJAX requests get 401 with JSON
		if isAjaxRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":       "Session expired",
				"redirectUrl": redirectURL,
				"requireAuth": true,
			})
			return errors.New("Unauthenticated, AJAX request returned 401")
		}

		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		return errors.New("Unauthenticated, redirecting to SSO login page")
	}

	user, err := ar.GetUserFromRequest(w, r)
	if err != nil {
		// Clears the invalid session cookie
		http.SetCookie(w, &http.Cookie{
			Name:     ar.Options.CookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})

		redirectURL, buildErr := ar.buildLoginRedirectURL(r)
		if buildErr != nil {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write(invalidHTML)
			return buildErr
		}

		// AJAX requests get 401 with JSON
		if isAjaxRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":       "Session expired",
				"redirectUrl": redirectURL,
				"requireAuth": true,
			})
			return errors.New("Invalid session, AJAX request returned 401")
		}

		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		return errors.New("Invalid session, redirecting to SSO login page")
	}

	ar.setAuthHeaders(r, user)
	return nil
}
