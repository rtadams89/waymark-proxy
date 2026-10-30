package loadbalance

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"imuslab.com/zoraxy/mod/dynamicproxy/dpcore"
	"imuslab.com/zoraxy/mod/geodb"
	"imuslab.com/zoraxy/mod/info/logger"
)

type Options struct {
	SessionKey           string // Signs sticky session cookies
	UseActiveHealthCheck bool
	Geodb                *geodb.Store //GeoIP resolver for checking incoming request origin country
	Logger               *logger.Logger
}

type RouteManager struct {
	SessionStore *sessions.CookieStore
	OnlineStatus sync.Map // Online status reported by the uptime monitor
	Options      Options

	cacheTicker     *time.Ticker //Ticker for cache cleanup
	cacheTickerStop chan bool    //Stop the cache cleanup

	inflight sync.Map // destination address -> *atomic.Int64 requests in progress
}

type Upstream struct {
	//Upstream Proxy Configs
	OriginIpOrDomain         string //Target IP address or domain name with port
	RequireTLS               bool
	SkipCertValidations      bool //Set to true to accept self signed certs
	SkipWebSocketOriginCheck bool
	// Name the upstream certificate is checked against and sent as SNI:
	// "" automatic, "upstream" or "proxyhost"
	TLSVerifyName string

	//Load balancing configs
	Weight int // Random weight; 0 for fallback only

	//HTTP Transport Config
	MaxConn     int   // Maximum concurrent requests to this upstream
	RespTimeout int64 //Response header timeout in milliseconds

	proxy *dpcore.ReverseProxy
}

func NewLoadBalancer(options *Options) *RouteManager {
	if options.SessionKey == "" {
		options.SessionKey = uuid.New().String()
	}

	cacheTicker := time.NewTicker(12 * time.Hour)
	cacheTickerStop := make(chan bool)
	go func() {
		options.Logger.PrintAndLog("load-balancer", "Destination state cache ticker started", nil)
		for {
			select {
			case <-cacheTickerStop:
				return
			case <-cacheTicker.C:
				//Clean up the cache
				options.Logger.PrintAndLog("load-balancer", "Cleaning up destination state cache", nil)
			}
		}
	}()

	//Generate a session store for stickySession
	store := sessions.NewCookieStore([]byte(options.SessionKey))
	return &RouteManager{
		SessionStore: store,
		OnlineStatus: sync.Map{},
		Options:      *options,

		cacheTicker:     cacheTicker,
		cacheTickerStop: cacheTickerStop,
	}
}

// UpstreamsReady checks if the group of upstreams contains at least one
// origin server that is ready
func (m *RouteManager) UpstreamsReady(upstreams []*Upstream) bool {
	for _, upstream := range upstreams {
		if upstream.IsReady() {
			return true
		}
	}
	return false
}

// String format and convert a list of upstream into a string representations
func GetUpstreamsAsString(upstreams []*Upstream) string {
	targets := []string{}
	for _, upstream := range upstreams {
		targets = append(targets, upstream.String())
	}
	if len(targets) == 0 {
		return "(no destination config)"
	}
	return strings.Join(targets, ", ")
}

// Reset the current session store and clear all previous sessions
func (m *RouteManager) ResetSessions() {
	m.SessionStore = sessions.NewCookieStore([]byte(m.Options.SessionKey))
}

func (m *RouteManager) Close() {
	//Close the session store
	m.SessionStore.MaxAge(0)

	if m.cacheTicker != nil {
		m.cacheTicker.Stop()
	}
	close(m.cacheTickerStop)
}

// println logs a load balancer message.
func (m *RouteManager) println(message string, err error) {
	m.Options.Logger.PrintAndLog("load-balancer", message, err)
}
