package dynamicproxy

/*
	assetcache_custom.go (custom fork)

	Hooks mod/assetcache into hostRequest. The hook runs after access rules,
	rate limiting, CAPTCHA and authentication, so cached files are only
	served to requests that passed them. Virtual directories and WebSocket
	requests are never cached.
*/

import (
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/assetcache"
)

// AssetCache is the memory cache shared by all proxy rules. Its size limit
// is set by the main package at startup.
var AssetCache = assetcache.NewStore(256 << 20)

func (h *ProxyHandler) assetCacheBegin(w http.ResponseWriter, r *http.Request, target *ProxyEndpoint) *assetcache.Session {
	if !target.AssetCache.On() || isWebSocketRequest(r) {
		return nil
	}
	zoraxyBasicAuth := target.AuthenticationProvider != nil && target.AuthenticationProvider.AuthMethod == AuthMethodBasic
	s := AssetCache.Begin(w, r, strings.ToLower(target.RootOrMatchingDomain), target.AssetCache, assetcache.BeginOptions{
		AuthorizationIsZoraxys: zoraxyBasicAuth,
	})
	if s != nil && s.Served() {
		h.Parent.logRequest(r, true, s.ServedStatus(), "host-http", r.Host, "cache", target)
	}
	return s
}
