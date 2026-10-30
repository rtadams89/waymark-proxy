package dynamicproxy

import (
	"errors"
	"net/http"
	"regexp"

	"imuslab.com/zoraxy/mod/auth"
	"imuslab.com/zoraxy/mod/netutils"
	"imuslab.com/zoraxy/mod/pathmatch"
)

/*
handleAuthProviderRouting applies the rule's authentication provider;
true means the response was written and the request must not continue.
*/
func handleAuthProviderRouting(sep *ProxyEndpoint, w http.ResponseWriter, r *http.Request, h *ProxyHandler) bool {
	requestHostname := r.Host

	switch sep.AuthenticationProvider.AuthMethod {
	case AuthMethodBasic:
		err := h.handleBasicAuthRouting(w, r, sep)
		if err != nil {
			clientIP := h.Parent.GetClientIPForEndpoint(r, sep)
			h.Parent.Option.Logger.LogHTTPRequest(r, "host-http", 401, requestHostname, "", clientIP)
			return true
		}
	case AuthMethodForward:
		err := h.handleForwardAuth(w, r)
		if err != nil {
			clientIP := h.Parent.GetClientIPForEndpoint(r, sep)
			h.Parent.Option.Logger.LogHTTPRequest(r, "host-http", 401, requestHostname, "", clientIP)
			return true
		}
	case AuthMethodOauth2:
		err := h.handleOAuth2Auth(w, r)
		if err != nil {
			clientIP := h.Parent.GetClientIPForEndpoint(r, sep)
			h.Parent.Option.Logger.LogHTTPRequest(r, "host-http", 401, requestHostname, "", clientIP)
			return true
		}
	case AuthMethodZorxAuth:
		err := h.handleZorxAuth(w, r, sep)
		if err != nil {
			clientIP := h.Parent.GetClientIPForEndpoint(r, sep)
			h.Parent.Option.Logger.LogHTTPRequest(r, "host-http", 401, requestHostname, "", clientIP)
			return true
		}
	}

	return false
}

/* Basic Auth */
func (h *ProxyHandler) handleBasicAuthRouting(w http.ResponseWriter, r *http.Request, pe *ProxyEndpoint) error {
	return handleBasicAuth(w, r, pe)
}

// basicAuthExceptionMatched reports whether an exception rule exempts the
// request from basic auth.
func basicAuthExceptionMatched(pe *ProxyEndpoint, r *http.Request) bool {
	if pe == nil || pe.AuthenticationProvider == nil {
		return false
	}
	requestTarget := pathmatch.RequestTarget(r)
	for _, exceptionRule := range pe.AuthenticationProvider.BasicAuthExceptionRules {
		if exceptionRule == nil {
			continue
		}
		exceptionType := exceptionRule.RuleType
		switch exceptionType {
		case AuthExceptionType_Paths:
			// Case sensitive unless the rule says otherwise
			matched := pathmatch.RequestPathWithinPrefix(requestTarget, exceptionRule.PathPrefix)
			if exceptionRule.CaseInsensitive {
				matched = pathmatch.RequestPathWithinPrefixFold(requestTarget, exceptionRule.PathPrefix)
			}
			if matched {
				return true
			}
		case AuthExceptionType_CIDR:
			// Uses forwarded addresses only when the rule sets UseTrustedProxy
			var requesterIp string
			if exceptionRule.UseTrustedProxy {
				requesterIp = netutils.GetRequesterIP(r)
			} else {
				requesterIp = netutils.GetRequesterIPUntrusted(r)
			}
			if requesterIp != "" {
				if requesterIp == exceptionRule.CIDR {
					return true
				}

				wildcardMatch := netutils.MatchIpWildcard(requesterIp, exceptionRule.CIDR)
				if wildcardMatch {
					return true
				}

				cidrMatch := netutils.MatchIpCIDR(requesterIp, exceptionRule.CIDR)
				if cidrMatch {
					return true
				}
			}
		default:
			continue
		}
	}
	return false
}

// handleBasicAuth checks basic auth; when it returns an error the response
// has been written.
func handleBasicAuth(w http.ResponseWriter, r *http.Request, pe *ProxyEndpoint) error {
	if basicAuthExceptionMatched(pe, r) {
		return nil
	}

	u, p, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		w.WriteHeader(401)
		w.Write([]byte("401 - Unauthorized"))
		return errors.New("unauthorized")
	}

	matchingFound := false
	for _, cred := range pe.AuthenticationProvider.BasicAuthCredentials {
		if u == cred.Username && auth.PasswordMatchesCached(cred.PasswordHash, p) {
			matchingFound = true

			r.Header.Set("X-Remote-User", u)
			break
		}
	}

	if !matchingFound {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		w.WriteHeader(401)
		w.Write([]byte("401 - Unauthorized"))
		return errors.New("unauthorized")
	}

	return nil
}

/* Forward Auth */
func (h *ProxyHandler) handleForwardAuth(w http.ResponseWriter, r *http.Request) error {
	return h.Parent.Option.ForwardAuthRouter.HandleAuthProviderRouting(w, r)
}

/* OAuth2 Auth */
func (h *ProxyHandler) handleOAuth2Auth(w http.ResponseWriter, r *http.Request) error {
	return h.Parent.Option.OAuth2Router.HandleOAuth2Auth(w, r)
}

/* Waymark Auth */
func (h *ProxyHandler) handleZorxAuth(w http.ResponseWriter, r *http.Request, sep *ProxyEndpoint) error {
	if sep != nil && sep.AuthenticationProvider != nil {
		if zorxAuthExceptionMatched(sep.AuthenticationProvider.ZorxAuthExceptionRules, r) {
			return nil
		}
	}
	return h.Parent.Option.ZorxAuthAgentRouter.HandleAuthRouting(w, r)
}

// zorxAuthExceptionMatched reports whether an exception rule exempts the
// request from Waymark Auth.
func zorxAuthExceptionMatched(rules []*ZorxAuthExceptionRule, r *http.Request) bool {
	requestTarget := pathmatch.RequestTarget(r)
	normalizedPath := pathmatch.CleanRequestPath(requestTarget)
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		switch rule.RuleType {
		case AuthExceptionType_Paths:
			if rule.IsRegex {
				matched, err := regexp.MatchString(rule.PathPattern, normalizedPath)
				if err == nil && matched {
					return true
				}
			} else {
				if pathmatch.RequestPathWithinPrefix(requestTarget, rule.PathPattern) {
					return true
				}
			}
		case AuthExceptionType_CIDR:
			var requesterIp string
			if rule.UseTrustedProxy {
				requesterIp = netutils.GetRequesterIP(r)
			} else {
				requesterIp = netutils.GetRequesterIPUntrusted(r)
			}
			if requesterIp != "" {
				if requesterIp == rule.CIDR {
					return true
				}
				if netutils.MatchIpWildcard(requesterIp, rule.CIDR) {
					return true
				}
				if netutils.MatchIpCIDR(requesterIp, rule.CIDR) {
					return true
				}
			}
		}
	}
	return false
}
