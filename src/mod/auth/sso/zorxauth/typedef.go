package zorxauth

import (
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
)

const (
	DB_NAME                       = "zorxauth"
	DB_USERS_TABLE                = "zorxauth_users"
	DB_USERS_KEY_PREFIX           = "user_"
	DB_BROWSER_SESSIONS_TABLE     = "zorxauth_browser_sessions"
	DB_BROWSER_SESSION_KEY_PREFIX = "session_"
	DB_GATEWAY_SESSIONS_TABLE     = "zorxauth_gateway_sessions"
	DB_GATEWAY_SESSION_KEY_PREFIX = "gateway_"
)

type User struct {
	ID             string   `json:"id"` //uuidv4
	Username       string   `json:"username"`
	Email          string   `json:"email"` // optional, also accepted as login name
	PasswordHash   string   `json:"passwordHash"`
	UseGroupPolicy bool     `json:"useGroupPolicy"` // check access with the group policy instead of AllowedHosts
	GroupID        string   `json:"groupId"`
	AllowedHosts   []string `json:"allowedHosts"` // empty allows all hosts

	/* User Configs */
	Enable2FA  bool   `json:"enable2FA"`  // a TOTP code is required after the password
	TOTPSecret string `json:"totpSecret"` // required if Enable2FA

	/* Passkeys (WebAuthn) */
	PasskeyCredentials []PasskeyCredential `json:"passkeyCredentials"`
}

// PasskeyCredential stores a single WebAuthn credential for a user.
type PasskeyCredential struct {
	ID             []byte   `json:"id"`             // Raw credential ID bytes
	PublicKey      []byte   `json:"publicKey"`      // COSE-encoded public key
	AAGUID         []byte   `json:"aaguid"`         // Authenticator AAGUID
	SignCount      uint32   `json:"signCount"`      // Signature counter (clone detection)
	CloneWarning   bool     `json:"cloneWarning"`   // Possible clone detected
	Transports     []string `json:"transports"`     // e.g. ["internal", "hybrid", "usb"]
	BackupEligible bool     `json:"backupEligible"` // Can be cloud-synced
	BackupState    bool     `json:"backupState"`    // Currently synced to cloud
	Name           string   `json:"name"`           // User-assigned display name
	CreatedAt      int64    `json:"createdAt"`      // Unix timestamp
	LastUsedAt     int64    `json:"lastUsedAt"`     // Unix timestamp
}

// AuthRouterOptions contains configuration for the ZorxAuth router
type AuthRouterOptions struct {
	/* Auth Gateway Options */
	EnableAuthGateway bool `json:"enable_auth_gateway"` // when off, all auth requests are rejected; default false
	GatewayPort       int  `json:"gateway_port"`        // default 5489
	/* Auth Router Options */
	EnableRateLimit          bool   `json:"enable_rate_limit"`       // default true
	RateLimitPerIp           int    `json:"rate_limit_per_ip"`       // attempts per minute per IP; default 60
	UseExpotentialBackoff    bool   `json:"use_exponential_backoff"` // default true
	SSORedirectURL           string `json:"sso_redirect_url"`        // URL of the SSO login endpoint
	SSOSessionSetURL         string `json:"sso_session_set_url"`
	CookieName               string `json:"cookie_name"`
	CookieDuration           int    `json:"cookie_duration"`             // seconds
	CookieDurationRememberMe int    `json:"cookie_duration_remember_me"` // seconds, with "Remember Me"
	/* Storage Options */
	ConfigFolderPath string `json:"config_folder_path"` // group policy folder; default ./conf/sso/zorxauth
}

type BrowserSession struct {
	Username string
	Expiry   time.Time
}

type GatewaySession struct {
	Username string
	Expiry   time.Time
}

// PendingTOTPSession holds login context while waiting for 2FA verification
type PendingTOTPSession struct {
	Username       string
	Host           string
	Port           string
	Protocol       string
	RedirectTarget string
	RememberMe     bool
	IsDirectLogin  bool // no redirect target
	Expiry         time.Time
}

// PendingTOTPSetup holds a newly generated TOTP secret before the user confirms it
type PendingTOTPSetup struct {
	Secret string
	Expiry time.Time
}

// AuthRouter handles ZorxAuth SSO authentication routing
type AuthRouter struct {
	Logger   *logger.Logger
	Database *database.Database
	Options  *AuthRouterOptions

	// IsServedHost reports whether a host name belongs to one of this
	// proxy's rules. Session codes are only sent to such hosts.
	IsServedHost func(host string) bool `json:"-"`

	/* Internal */
	sessionIdStore      sync.Map //sessionId -> userID
	gatewaySessionStore sync.Map //sessionId -> *GatewaySession
	cookieIdStore       sync.Map //browserSessionID (cookie value) -> *BrowserSession
	gatewayServer       *GatewayServer

	/* Group Policies */
	groupPolicies sync.Map // id (string) -> *GroupPolicy

	/* Login page customization */
	branding      *BrandingConfig // nil for defaults
	brandingMutex sync.RWMutex

	/* Login rate limiting */
	loginAttemptCounter sync.Map // IP -> *int64, attempts in the current minute
	loginFailureCounter sync.Map // IP -> *int64, consecutive failures
	rateLimitResetStop  chan bool

	/* 2FA */
	pendingTOTPSessions sync.Map // totp_token (string) -> *PendingTOTPSession (pending login 2FA)
	pendingTOTPSetup    sync.Map // username (string) -> *PendingTOTPSetup (pending 2FA enrollment)

	/* Passkeys (WebAuthn) */
	pendingPasskeyReg  sync.Map // token (string) -> *PendingPasskeyRegistration
	pendingPasskeyAuth sync.Map // token (string) -> *PendingPasskeyAuth
}

func getDefaultOptions() *AuthRouterOptions {
	return &AuthRouterOptions{
		GatewayPort:              5489,
		EnableAuthGateway:        false,
		EnableRateLimit:          true,
		RateLimitPerIp:           60,
		UseExpotentialBackoff:    true,
		SSORedirectURL:           "",
		SSOSessionSetURL:         "/.wellknown/zorxauth/session/set",
		CookieName:               "zr_xauth_session",
		CookieDuration:           3600,
		CookieDurationRememberMe: 604800, // 7 days
		ConfigFolderPath:         "./conf/sso/zorxauth",
	}
}
