package dynamicproxy

import (
	"github.com/google/uuid"
	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/dynamicproxy/rewrite"
)

func GetDefaultAuthenticationProvider() *AuthenticationProvider {
	return &AuthenticationProvider{
		AuthMethod:                        AuthMethodNone,
		BasicAuthCredentials:              []*BasicAuthCredentials{},
		BasicAuthExceptionRules:           []*BasicAuthExceptionRule{},
		BasicAuthGroupIDs:                 []string{},
		ForwardAuthURL:                    "",
		ForwardAuthResponseHeaders:        []string{},
		ForwardAuthResponseClientHeaders:  []string{},
		ForwardAuthRequestHeaders:         []string{},
		ForwardAuthRequestExcludedCookies: []string{},
	}
}

func GetDefaultHeaderRewriteRules() *HeaderRewriteRules {
	return &HeaderRewriteRules{
		UserDefinedHeaders:            []*rewrite.UserDefinedHeader{},
		RequestHostOverwrite:          "",
		HSTSMaxAge:                    0,
		EnablePermissionPolicyHeader:  false,
		PermissionPolicy:              nil,
		DisableHopByHopHeaderRemoval:  false,
		DisableUserAgentHeaderRemoval: false,
	}
}

func GetDefaultProxyEndpoint() ProxyEndpoint {
	randomPrefix := uuid.New().String()
	return ProxyEndpoint{
		ProxyType:                    ProxyTypeHost,
		RootOrMatchingDomain:         randomPrefix + ".internal",
		MatchingDomainAlias:          []string{},
		ActiveOrigins:                []*loadbalance.Upstream{},
		InactiveOrigins:              []*loadbalance.Upstream{},
		UseStickySession:             false,
		UseActiveLoadBalance:         false,
		Disabled:                     false,
		BypassGlobalTLS:              false,
		VirtualDirectories:           []*VirtualDirectoryEndpoint{},
		HeaderRewriteRules:           GetDefaultHeaderRewriteRules(),
		EnableWebsocketCustomHeaders: false,
		AuthenticationProvider:       GetDefaultAuthenticationProvider(),
		RequireRateLimit:             false,
		RateLimit:                    0,
		DisableUptimeMonitor:         false,
		DisableAutoFallback:          false,
		AccessFilterUUID:             access.AllowAllID,
		DefaultSiteOption:            DefaultSite_InternalStaticWebServer,
		DefaultSiteValue:             "",
	}
}
