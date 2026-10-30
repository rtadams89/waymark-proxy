package dynamicproxy

// Static file caching in hostRequest, after the ACL, rate limiting, CAPTCHA
// and authentication; not for virtual directories or WebSocket requests.

import (
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/assetcache"
)

// AssetCache is the memory cache shared by all proxy rules.
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
