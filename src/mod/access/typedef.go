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
	ConfigFolder       string
	LegacyFolder       string // access rules of 2.17 and earlier
	TrustedProxiesFile string
	GeoDB              *geodb.Store
	Database           *database.Database

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

	mu            sync.RWMutex
	acls          map[string]*ACL         // saved ACLs by ID
	objects       map[string]*Object      // saved objects by ID
	compiled      map[string]*compiledACL // every ACL including the built-in one
	hits          map[string]*hitCounter  // by ACL ID + "/" + rule ID
	denyLog       denyLogger
	countryLookup func(ip string) (country string, known bool) // replaces the GeoIP lookup in tests
	converted     *LegacyConversion                            // conversion done at startup

	publicIpTicker     *time.Ticker
	publicIpTickerStop chan bool
	trustedCIDRs       []*net.IPNet
	trustedCIDRMu      sync.RWMutex
}
