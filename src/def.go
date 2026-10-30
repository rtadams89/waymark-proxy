package main

import (
	"embed"
	"flag"
	"net/http"
	"time"

	"imuslab.com/zoraxy/mod/auth/sso/oauth2"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/acme"
	"imuslab.com/zoraxy/mod/auth"
	"imuslab.com/zoraxy/mod/auth/sso/forward"
	"imuslab.com/zoraxy/mod/auth/sso/zorxauth"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/dynamicproxy/redirection"
	"imuslab.com/zoraxy/mod/geodb"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/info/logviewer"
	"imuslab.com/zoraxy/mod/l4proxy"
	"imuslab.com/zoraxy/mod/mdns"
	"imuslab.com/zoraxy/mod/netstat"
	"imuslab.com/zoraxy/mod/pathrule"
	"imuslab.com/zoraxy/mod/plugins"
	"imuslab.com/zoraxy/mod/statistic"
	"imuslab.com/zoraxy/mod/statistic/analytic"
	"imuslab.com/zoraxy/mod/tlscert"
	"imuslab.com/zoraxy/mod/uptime"
	"imuslab.com/zoraxy/mod/webserv"
)

const (
	/* Build Constants */
	SYSTEM_NAME    = "Waymark"
	SYSTEM_VERSION = "3.3.5"

	/* System Constants */
	WEBSERV_DEFAULT_PORT         = 5487
	MDNS_HOSTNAME_PREFIX         = "waymark_"
	MDNS_IDENTIFY_DEVICE_TYPE    = "Network Gateway"
	MDNS_IDENTIFY_DOMAIN         = "waymark-proxy"
	MDNS_IDENTIFY_VENDOR         = "Waymark Proxy"
	MDNS_SCAN_TIMEOUT            = 30 /* Seconds */
	MDNS_SCAN_UPDATE_INTERVAL    = 15 /* Minutes */
	GEODB_CACHE_CLEAR_INTERVAL   = 15 /* Minutes */
	CSRF_COOKIENAME              = "waymark_csrf"
	LOG_PREFIX                   = "waymark"
	STATISTIC_AUTO_SAVE_INTERVAL = 600 /* Seconds */
)

/* System Startup Flags */
var (
	webUIPort                  = flag.String("port", ":8000", "Management web interface listening port")
	databaseBackend            = flag.String("db", "auto", "Database backend to use (leveldb, boltdb, auto) Note that fsdb will be used on unsupported platforms like RISCV")
	noauth                     = flag.Bool("noauth", false, "Disable authentication for management interface")
	showver                    = flag.Bool("version", false, "Show version of this server")
	allowMdnsScanning          = flag.Bool("mdns", true, "Enable mDNS scanner and transponder")
	mdnsName                   = flag.String("mdnsname", "", "mDNS name, leave empty to use default (waymark_{node-uuid}.local)")
	enableHighSpeedGeoIPLookup = flag.Bool("fastgeoip", true, "Enable high speed geoip lookup, disable this if you are using a very small memory footprint device")
	_                          = flag.Bool("cfgupgrade", true, "No longer used (Zoraxy's config migrations were removed in Waymark 2.17.1); accepted so existing service files still start")

	/* Acme Configuration Flags */
	acmeAutoRenewInterval = flag.Int("autorenew", 86400, "ACME auto TLS/SSL certificate renew check interval (seconds)")
	acmeCertAutoRenewDays = flag.Int("earlyrenew", 30, "Number of days to early renew a soon expiring certificate (days)")
	acmeTestMode          = flag.Bool("acmetestmode", false, "Run ACME in test/staging mode")
	acmeKeyFileMode       = flag.String("acmekeymode", "0600", "File mode for ACME private key files (octal)")
	acmePublicFileMode    = flag.String("acmepublicmode", "0644", "File mode for ACME certificate and metadata files (octal)")

	/* Logging Configuration Flags */
	enableLog = flag.Bool("enablelog", true, "Enable system wide logging, set to false for writing log to STDOUT only")

	/* Statistics Configuration Flags */
	statsMaxEntriesPerMap = flag.Int("stats_max_entries", 20000, "Soft-cap per-dimension statistics maps at N entries; when exceeded the entries with the lowest request counts are dropped first (0 = unbounded)")

	/* Default Configuration Flags */
	defaultInboundPort          = flag.Int("default_inbound_port", 443, "Default web server listening port")
	defaultEnableInboundTraffic = flag.Bool("default_inbound_enabled", true, "If web server is enabled by default")

	/* Proxy Timeout Configuration Flags */
	proxyReadHeaderTimeout = flag.Int("readheadertimeout", 30, "Proxy server read header timeout in seconds (0 = disabled)")
	proxyWriteTimeout      = flag.Int("writetimeout", 0, "Proxy server write timeout in seconds (0 = disabled, recommended for long-running requests)")
	proxyIdleTimeout       = flag.Int("idletimeout", 300, "Proxy server idle keep-alive connection timeout in seconds (0 = disabled)")

	/* HTTP/2 Configuration Flags */
	proxyDisableHttp2       = flag.Bool("disablehttp2", false, "Disable HTTP/2 on the TLS listener and serve HTTP/1.1 only")
	proxyH2MaxStreams       = flag.Uint("h2_max_concurrent_streams", 0, "HTTP/2 max concurrent streams per connection (0 = Go default)")
	proxyH2ConnBufferSize   = flag.Int("h2_conn_buffer", 0, "HTTP/2 max upload buffer per connection in bytes, min 65536 (0 = Go default)")
	proxyH2StreamBufferSize = flag.Int("h2_stream_buffer", 0, "HTTP/2 max upload buffer per stream in bytes, min 65536 (0 = Go default)")

	/* HTTP/3 (QUIC) Configuration Flags */
	proxyH3MaxStreams = flag.Uint("h3_max_concurrent_streams", 0, "HTTP/3 max concurrent streams per connection (0 = quic-go default)")

	/* Path Configuration Flags */
	path_database  = flag.String("dbpath", "./sys.db", "Database path")
	path_conf      = flag.String("conf", "./conf", "Configuration folder path")
	path_tmp       = flag.String("tmp", "./tmp", "Temporary folder path")
	path_uuid      = flag.String("uuid", "./sys.uuid", "sys.uuid file path")
	path_logFile   = flag.String("log", "./log", "Log folder path")
	path_webserver = flag.String("webroot", "./www", "Static web server root folder. Only allow change in start paramters")
	path_plugin    = flag.String("plugin", "./plugins", "Plugin folder path")

	/* Maintaince & Development Function Flags */
	geoDbUpdate       = flag.Bool("update_geoip", false, "Download the latest GeoIP data and exit")
	development_build = flag.Bool("dev", false, "Use external web folder for UI development and enable verbose logging")
	reset_account     = flag.Bool("reset_ac", false, "Reset admin account username and password to default and exit")
)

/* Global Variables and Handlers */
var (
	/* System */
	nodeUUID    = "generic"
	bootTime    = time.Now().Unix()
	requireAuth = true

	/* Path variables initialized after flag parsing */
	TMP_FOLDER                 string
	ACME_AUTORENEW_CONFIG_PATH string
	CONF_FOLDER                string
	CONF_HTTP_PROXY            string
	CONF_L4_PROXY              string
	CONF_CERT_STORE            string
	CONF_REDIRECTION           string
	CONF_ACCESS_RULE           string // access rules of 2.17 and earlier, converted to ACLs
	CONF_ACL                   string // ACLs and objects
	CONF_PATH_RULE             string
	CONF_PLUGIN_GROUPS         string
	CONF_GEODB_PATH            string
	CONF_LOG_CONFIG            string
	CONF_TRUSTED_PROXIES       string

	/* mDNS */
	previousmdnsScanResults = []*mdns.NetworkHost{}
	mdnsTickerStop          chan bool

	//go:embed web/*
	webres embed.FS

	sysdb          *database.Database
	authAgent      *auth.AuthAgent
	tlsCertManager *tlscert.Manager
	redirectTable  *redirection.RuleTable
	webminPanelMux *http.ServeMux
	csrfMiddleware func(http.Handler) http.Handler

	pathRuleHandler    *pathrule.Handler
	geodbStore         *geodb.Store
	accessController   *access.Controller
	netstatBuffers     *netstat.NetStatBuffers
	statisticCollector *statistic.Collector
	uptimeMonitor      *uptime.Monitor
	mdnsScanner        *mdns.MDNSHost
	l4ProxyManager     *l4proxy.Manager
	acmeHandler        *acme.ACMEHandler
	acmeAutoRenewer    *acme.AutoRenewer
	staticWebServer    *webserv.WebServer
	loadBalancer       *loadbalance.RouteManager
	pluginManager      *plugins.Manager

	pluginApiKeyManager *auth.APIKeyManager

	forwardAuthRouter *forward.AuthRouter
	oauth2Router      *oauth2.OAuth2Router
	zorxAuthRouter    *zorxauth.AuthRouter

	AnalyticLoader   *analytic.DataLoader
	SystemWideLogger *logger.Logger
	LogViewer        *logviewer.Viewer
)
