package dynamicproxy

import (
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/dynamicproxy/rewrite"
)

/*
	User Defined Header Functions
*/

func (ep *ProxyEndpoint) UserDefinedHeaderExists(key string) bool {
	endpointProxyRewriteRules := GetDefaultHeaderRewriteRules()
	if ep.HeaderRewriteRules != nil {
		endpointProxyRewriteRules = ep.HeaderRewriteRules
	}

	for _, header := range endpointProxyRewriteRules.UserDefinedHeaders {
		if strings.EqualFold(header.Key, key) {
			return true
		}
	}
	return false
}

func (ep *ProxyEndpoint) RemoveUserDefinedHeader(key string) error {
	newHeaderList := []*rewrite.UserDefinedHeader{}
	if ep.HeaderRewriteRules == nil {
		ep.HeaderRewriteRules = GetDefaultHeaderRewriteRules()
	}
	for _, header := range ep.HeaderRewriteRules.UserDefinedHeaders {
		if !strings.EqualFold(header.Key, key) {
			newHeaderList = append(newHeaderList, header)
		}
	}

	ep.HeaderRewriteRules.UserDefinedHeaders = newHeaderList

	return nil
}

// AddUserDefinedHeader adds a header rule, replacing one with the same key.
func (ep *ProxyEndpoint) AddUserDefinedHeader(newHeaderRule *rewrite.UserDefinedHeader) error {
	if ep.UserDefinedHeaderExists(newHeaderRule.Key) {
		ep.RemoveUserDefinedHeader(newHeaderRule.Key)
	}

	if ep.HeaderRewriteRules == nil {
		ep.HeaderRewriteRules = GetDefaultHeaderRewriteRules()
	}
	newHeaderRule.Key = cases.Title(language.Und, cases.NoLower).String(newHeaderRule.Key)
	ep.HeaderRewriteRules.UserDefinedHeaders = append(ep.HeaderRewriteRules.UserDefinedHeaders, newHeaderRule)
	return nil
}

/*
	Virtual Directory Functions
*/

// GetVirtualDirectoryHandlerFromRequestURI returns the virtual directory
// matching requestURI.
func (ep *ProxyEndpoint) GetVirtualDirectoryHandlerFromRequestURI(requestURI string) *VirtualDirectoryEndpoint {
	for _, vdir := range ep.VirtualDirectories {
		if strings.HasPrefix(requestURI, vdir.MatchingPath) {
			thisVdir := vdir
			return thisVdir
		}
	}
	return nil
}

// GetVirtualDirectoryRuleByMatchingPath returns the virtual directory with
// exactly this matching path.
func (ep *ProxyEndpoint) GetVirtualDirectoryRuleByMatchingPath(matchingPath string) *VirtualDirectoryEndpoint {
	for _, vdir := range ep.VirtualDirectories {
		if vdir.MatchingPath == matchingPath {
			thisVdir := vdir
			return thisVdir
		}
	}
	return nil
}

// HasSameTarget reports whether the virtual directory has the given domain
// and TLS settings.
func (vdir *VirtualDirectoryEndpoint) HasSameTarget(domain string, requireTLS bool, skipCertValidations bool) bool {
	return vdir.Domain == domain && vdir.RequireTLS == requireTLS && vdir.SkipCertValidations == skipCertValidations
}

// BulkVdirAction is the action of a bulk virtual directory operation for one host.
type BulkVdirAction string

const (
	BulkVdirCreate   BulkVdirAction = "create"   // directory is missing and should be created
	BulkVdirRemove   BulkVdirAction = "remove"   // an identical directory exists and is removed
	BulkVdirSkip     BulkVdirAction = "skip"     // an identical directory already exists
	BulkVdirConflict BulkVdirAction = "conflict" // a directory with different settings exists at this path
	BulkVdirNoop     BulkVdirAction = "noop"     // nothing to remove
)

// ClassifyBulkVdir returns the action for one host: with ensurePresent the
// directory is created or skipped, otherwise removed or noop. A directory
// at the same path with a different target is a conflict.
func ClassifyBulkVdir(ensurePresent bool, existing *VirtualDirectoryEndpoint, domain string, requireTLS bool, skipCertValidations bool) BulkVdirAction {
	if ensurePresent {
		if existing == nil {
			return BulkVdirCreate
		}
		if existing.HasSameTarget(domain, requireTLS, skipCertValidations) {
			return BulkVdirSkip
		}
		return BulkVdirConflict
	}
	if existing == nil {
		return BulkVdirNoop
	}
	if existing.HasSameTarget(domain, requireTLS, skipCertValidations) {
		return BulkVdirRemove
	}
	return BulkVdirConflict
}

func (ep *ProxyEndpoint) RemoveVirtualDirectoryRuleByMatchingPath(matchingPath string) error {
	entryFound := false
	newVirtualDirectoryList := []*VirtualDirectoryEndpoint{}
	for _, vdir := range ep.VirtualDirectories {
		if vdir.MatchingPath == matchingPath {
			entryFound = true
		} else {
			newVirtualDirectoryList = append(newVirtualDirectoryList, vdir)
		}
	}

	if entryFound {
		ep.VirtualDirectories = newVirtualDirectoryList
		return nil
	}
	return errors.New("target virtual directory routing rule not found")
}

// AddVirtualDirectoryRule adds a virtual directory and applies the rule.
func (ep *ProxyEndpoint) AddVirtualDirectoryRule(vdir *VirtualDirectoryEndpoint) (*ProxyEndpoint, error) {
	if ep.GetVirtualDirectoryRuleByMatchingPath(vdir.MatchingPath) != nil {
		return nil, errors.New("rule with same matching path already exists")
	}

	ep.VirtualDirectories = append(ep.VirtualDirectories, vdir)

	parentRouter := ep.parent
	readyRoutingRule, err := parentRouter.PrepareProxyRoute(ep)
	if err != nil {
		return nil, err
	}

	switch ep.ProxyType {
	case ProxyTypeRoot:
		parentRouter.Root = readyRoutingRule
	case ProxyTypeHost:
		ep.Remove()
		parentRouter.AddProxyRouteToRuntime(readyRoutingRule)
	default:
		return nil, errors.New("unsupported proxy type")
	}

	return readyRoutingRule, nil
}

/* Upstreams */
func (ep *ProxyEndpoint) UpstreamOriginExists(originURL string) bool {
	for _, origin := range ep.ActiveOrigins {
		if origin.OriginIpOrDomain == originURL {
			return true
		}
	}
	for _, origin := range ep.InactiveOrigins {
		if origin.OriginIpOrDomain == originURL {
			return true
		}
	}
	return false
}

func (ep *ProxyEndpoint) GetUpstreamOriginByMatchingIP(originIpOrDomain string) (*loadbalance.Upstream, error) {
	for _, origin := range ep.ActiveOrigins {
		if origin.OriginIpOrDomain == originIpOrDomain {
			return origin, nil
		}
	}

	for _, origin := range ep.InactiveOrigins {
		if origin.OriginIpOrDomain == originIpOrDomain {
			return origin, nil
		}
	}
	return nil, errors.New("target destination not found")
}

// upstreamTLSServerName returns the upstream TLS server name from the Host
// header override, or "".
func (ep *ProxyEndpoint) upstreamTLSServerName() string {
	if ep.HeaderRewriteRules == nil {
		return ""
	}
	return ep.HeaderRewriteRules.RequestHostOverwrite
}

// AddUpstreamOrigin adds an upstream, started and active if activate.
func (ep *ProxyEndpoint) AddUpstreamOrigin(newOrigin *loadbalance.Upstream, activate bool) error {
	if ep.UpstreamOriginExists(newOrigin.OriginIpOrDomain) {
		return errors.New("destination with same origin already exists")
	}

	if activate {
		err := newOrigin.StartProxy(ep.upstreamTLSServerName())
		if err != nil {
			return err
		}
		ep.ActiveOrigins = append(ep.ActiveOrigins, newOrigin)
	} else {
		ep.InactiveOrigins = append(ep.InactiveOrigins, newOrigin)
	}

	ep.UpdateToRuntime()
	return nil
}

func (ep *ProxyEndpoint) RemoveUpstreamOrigin(originIpOrDomain string) error {
	originIpOrDomain = strings.TrimSpace(originIpOrDomain)

	if !ep.UpstreamOriginExists(originIpOrDomain) {
		return nil
	}

	newActiveOriginList := []*loadbalance.Upstream{}
	for _, origin := range ep.ActiveOrigins {
		if origin.OriginIpOrDomain != originIpOrDomain {
			newActiveOriginList = append(newActiveOriginList, origin)
		}
	}

	newInactiveOriginList := []*loadbalance.Upstream{}
	for _, origin := range ep.InactiveOrigins {
		if origin.OriginIpOrDomain != originIpOrDomain {
			newInactiveOriginList = append(newInactiveOriginList, origin)
		}
	}
	ep.ActiveOrigins = newActiveOriginList
	ep.InactiveOrigins = newInactiveOriginList
	ep.UpdateToRuntime()
	return nil
}

// ContainsWildcardName reports whether the host name, or unless
// skipAliasCheck an alias, contains a wildcard.
func (ep *ProxyEndpoint) ContainsWildcardName(skipAliasCheck bool) bool {
	hostname := ep.RootOrMatchingDomain
	aliasHostnames := ep.MatchingDomainAlias

	wildcardCheck := func(hostname string) bool {
		return len(hostname) > 0 && hostname[0] == '*'
	}

	if wildcardCheck(hostname) {
		return true
	}

	if !skipAliasCheck {
		for _, aliasHostname := range aliasHostnames {
			if wildcardCheck(aliasHostname) {
				return true
			}
		}
	}

	return false
}

// Clone returns a deep copy of the rule, not yet prepared.
func (ep *ProxyEndpoint) Clone() *ProxyEndpoint {
	clonedProxyEndpoint := ProxyEndpoint{}
	js, _ := json.Marshal(ep)
	json.Unmarshal(js, &clonedProxyEndpoint)
	return &clonedProxyEndpoint
}

func (ep *ProxyEndpoint) Remove() error {
	lookupHostname := strings.ToLower(ep.RootOrMatchingDomain)
	ep.parent.ProxyEndpoints.Delete(lookupHostname)
	return nil
}

func (ep *ProxyEndpoint) IsEnabled() bool {
	return !ep.Disabled
}

// UpdateToRuntime replaces the running rule without preparing it again.
func (ep *ProxyEndpoint) UpdateToRuntime() {
	lookupHostname := strings.ToLower(ep.RootOrMatchingDomain)
	ep.parent.ProxyEndpoints.Store(lookupHostname, ep)
}
