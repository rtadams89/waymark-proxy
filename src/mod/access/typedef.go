package access

import (
	"net"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/geodb"
	"imuslab.com/zoraxy/mod/info/logger"
)

type Options struct {
	Logger             logger.Logger
	ConfigFolder       string             //Custom fork: ACLs and objects (conf/acl)
	LegacyFolder       string             //Custom fork: access rules of 2.17 and earlier (conf/access), converted once
	TrustedProxiesFile string             //Path for storing trusted proxy IPs
	GeoDB              *geodb.Store       //For resolving country code
	Database           *database.Database //System key-value database

	/* Public IP monitoring */
	PublicIpCheckInterval int64 //in Seconds
}

type TrustedProxy struct {
	IP   string
	Desc string
}

type Controller struct {
	ServerPublicIP string
	TrustedProxies sync.Map // with structure of map[string]TrustedProxy
	Options        *Options

	//Custom fork: ACLs and objects (acl.go, engine.go, store.go)
	mu            sync.RWMutex
	acls          map[string]*ACL         // saved ACLs, by ID (the built-in one is not stored here)
	objects       map[string]*Object      // saved objects, by ID
	compiled      map[string]*compiledACL // every ACL, built-in included, ready to evaluate
	hits          map[string]*hitCounter  // by ACL ID + "/" + rule ID; kept across recompiles
	denyLog       denyLogger
	countryLookup func(ip string) (country string, known bool) // tests: replaces the Geo-IP lookup
	converted     *LegacyConversion                            // set when old access rules were converted at start-up

	//Internal
	publicIpTicker     *time.Ticker
	publicIpTickerStop chan bool
	trustedCIDRs       []*net.IPNet // Pre-parsed CIDR entries for fast lookup
	trustedCIDRMu      sync.RWMutex // Protects trustedCIDRs
}
