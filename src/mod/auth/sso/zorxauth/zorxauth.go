package zorxauth

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
)

/*
	Waymark Auth SSO. Unauthenticated requests are redirected to
	SSORedirectURL with the original URL in "redirect". After login the
	gateway redirects to SSOSessionSetURL on the original host with a
	one-time session_id; that request sets the session cookie and redirects
	to the original URL.
*/

// NewAuthRouter creates a new AuthRouter instance
func NewAuthRouter(db *database.Database, log *logger.Logger) *AuthRouter {
	db.NewTable(DB_NAME)
	db.NewTable(DB_USERS_TABLE)
	db.NewTable(DB_BROWSER_SESSIONS_TABLE)
	db.NewTable(DB_GATEWAY_SESSIONS_TABLE)

	if !db.KeyExists(DB_NAME, "options") {
		defaultOptions := getDefaultOptions()
		db.Write(DB_NAME, "options", defaultOptions)
	}

	options := AuthRouterOptions{}
	db.Read(DB_NAME, "options", &options)

	authRouter := &AuthRouter{
		Logger:             log,
		Database:           db,
		Options:            &options,
		rateLimitResetStop: make(chan bool, 1),
	}

	authRouter.loadBrowserSessions()

	authRouter.loadGatewaySessions()

	authRouter.initGroupPolicyStore()

	authRouter.initBrandingStore()

	go authRouter.startLoginRateLimitTicker()

	if options.EnableAuthGateway {
		gatewayServer := NewGatewayServer(authRouter)
		if err := gatewayServer.Start(); err != nil {
			if log != nil {
				log.PrintAndLog("waymark-auth", "Failed to start gateway server: "+err.Error(), err)
			}
		}

		authRouter.gatewayServer = gatewayServer
	}

	return authRouter
}

// generateValidationCodeForSession returns a one-time validation code for
// sessionId, valid for 30 seconds.
func (ar *AuthRouter) generateValidationCodeForSession(sessionId string) string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		// Time-derived code when the random source fails
		buf = []byte(time.Now().Format("20060102150405.000000000"))
	}

	validationCode := hex.EncodeToString(buf)
	ar.sessionIdStore.Store(validationCode, sessionId)

	time.AfterFunc(30*time.Second, func() {
		ar.sessionIdStore.Delete(validationCode)
	})

	return validationCode
}

// startLoginRateLimitTicker resets per-IP login attempt counters every minute.
func (ar *AuthRouter) startLoginRateLimitTicker() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ar.rateLimitResetStop:
			return
		case <-ticker.C:
			ar.loginAttemptCounter.Range(func(key, _ interface{}) bool {
				ar.loginAttemptCounter.Delete(key)
				return true
			})
		}
	}
}

// getLoginAttemptCount returns the number of login attempts made by ip in the current minute window.
func (ar *AuthRouter) getLoginAttemptCount(ip string) int64 {
	v, ok := ar.loginAttemptCounter.Load(ip)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(v.(*int64))
}

// incrementLoginAttempt records a login attempt (successful or not) from ip.
func (ar *AuthRouter) incrementLoginAttempt(ip string) {
	v, _ := ar.loginAttemptCounter.LoadOrStore(ip, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

// incrementLoginFailure records a consecutive login failure for ip and returns the new failure count.
func (ar *AuthRouter) incrementLoginFailure(ip string) int64 {
	v, _ := ar.loginFailureCounter.LoadOrStore(ip, new(int64))
	return atomic.AddInt64(v.(*int64), 1)
}

// loadBrowserSessions loads existing browser sessions from database into memory
func (ar *AuthRouter) loadBrowserSessions() {
	entries, err := ar.Database.ListTable(DB_BROWSER_SESSIONS_TABLE)
	if err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to list browser sessions: "+err.Error(), err)
		}
		return
	}

	now := time.Now()
	loadedCount := 0
	expiredCount := 0

	for _, entry := range entries {
		var session BrowserSession
		key := string(entry[0])
		err := ar.Database.Read(DB_BROWSER_SESSIONS_TABLE, key, &session)
		if err != nil {
			if ar.Logger != nil {
				ar.Logger.PrintAndLog("waymark-auth", "Failed to read browser session "+key+": "+err.Error(), err)
			}
			continue
		}

		if now.After(session.Expiry) {
			expiredCount++
			ar.Database.Delete(DB_BROWSER_SESSIONS_TABLE, key)
			continue
		}

		sessionID := strings.TrimPrefix(key, DB_BROWSER_SESSION_KEY_PREFIX)

		ar.cookieIdStore.Store(sessionID, &session)
		loadedCount++

		timeUntilExpiry := time.Until(session.Expiry)
		if timeUntilExpiry > 0 {
			sessionIDCopy := sessionID
			time.AfterFunc(timeUntilExpiry, func() {
				ar.cookieIdStore.Delete(sessionIDCopy)
			})
		}
	}

	if ar.Logger != nil {
		ar.Logger.PrintAndLog("waymark-auth", "Loaded "+strconv.Itoa(loadedCount)+" browser sessions from database ("+strconv.Itoa(expiredCount)+" expired sessions removed)", nil)
	}
}

// loadGatewaySessions loads existing gateway sessions from database into memory
func (ar *AuthRouter) loadGatewaySessions() {
	entries, err := ar.Database.ListTable(DB_GATEWAY_SESSIONS_TABLE)
	if err != nil {
		if ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to list gateway sessions: "+err.Error(), err)
		}
		return
	}

	now := time.Now()
	loadedCount := 0
	expiredCount := 0

	for _, entry := range entries {
		var session GatewaySession
		key := string(entry[0])
		err := ar.Database.Read(DB_GATEWAY_SESSIONS_TABLE, key, &session)
		if err != nil {
			if ar.Logger != nil {
				ar.Logger.PrintAndLog("waymark-auth", "Failed to read gateway session "+key+": "+err.Error(), err)
			}
			continue
		}

		if now.After(session.Expiry) {
			expiredCount++
			ar.Database.Delete(DB_GATEWAY_SESSIONS_TABLE, key)
			continue
		}

		sessionID := strings.TrimPrefix(key, DB_GATEWAY_SESSION_KEY_PREFIX)

		ar.gatewaySessionStore.Store(sessionID, &session)
		loadedCount++

		timeUntilExpiry := time.Until(session.Expiry)
		if timeUntilExpiry > 0 {
			sessionIDCopy := sessionID
			time.AfterFunc(timeUntilExpiry, func() {
				ar.gatewaySessionStore.Delete(sessionIDCopy)
			})
		}
	}

	if ar.Logger != nil {
		ar.Logger.PrintAndLog("waymark-auth", "Loaded "+strconv.Itoa(loadedCount)+" gateway sessions from database ("+strconv.Itoa(expiredCount)+" expired sessions removed)", nil)
	}
}

// Close saves all browser sessions to database and performs cleanup
func (ar *AuthRouter) Close() {
	if ar.rateLimitResetStop != nil {
		select {
		case ar.rateLimitResetStop <- true:
		default:
		}
	}

	if ar.gatewayServer != nil {
		ar.gatewayServer.Stop()
	}

	ar.cookieIdStore.Range(func(key, value interface{}) bool {
		sessionID, ok := key.(string)
		if !ok {
			return true
		}

		browserSession, ok := value.(*BrowserSession)
		if !ok {
			return true
		}

		if time.Now().After(browserSession.Expiry) {
			return true
		}

		dbKey := DB_BROWSER_SESSION_KEY_PREFIX + sessionID
		err := ar.Database.Write(DB_BROWSER_SESSIONS_TABLE, dbKey, browserSession)
		if err != nil && ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to save browser session: "+err.Error(), err)
		}

		return true
	})

	ar.gatewaySessionStore.Range(func(key, value interface{}) bool {
		sessionID, ok := key.(string)
		if !ok {
			return true
		}

		gatewaySession, ok := value.(*GatewaySession)
		if !ok {
			return true
		}

		if time.Now().After(gatewaySession.Expiry) {
			return true
		}

		dbKey := DB_GATEWAY_SESSION_KEY_PREFIX + sessionID
		err := ar.Database.Write(DB_GATEWAY_SESSIONS_TABLE, dbKey, gatewaySession)
		if err != nil && ar.Logger != nil {
			ar.Logger.PrintAndLog("waymark-auth", "Failed to save gateway session: "+err.Error(), err)
		}

		return true
	})

	if ar.Logger != nil {
		ar.Logger.PrintAndLog("waymark-auth", "Browser and gateway sessions saved to database", nil)
	}
}
