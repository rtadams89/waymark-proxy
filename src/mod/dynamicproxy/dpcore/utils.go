package dpcore

import (
	"net"
	"net/url"
	"strings"
)

// replaceLocationHost rewrites a backend Location URL to the requested host.
func replaceLocationHost(urlString string, rrr *ResponseRewriteRuleSet, useTLS bool) (string, error) {
	u, err := url.Parse(urlString)
	if err != nil {
		return "", err
	}

	// https when the client connection uses TLS
	if useTLS {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}

	// Leaves Locations to other hosts unchanged
	if rrr.ProxyDomain != u.Host && u.Host != rrr.OriginalHost && !strings.Contains(u.Host, rrr.OriginalHost+":") {
		return urlString, nil
	}

	_, newLocationPort, err := net.SplitHostPort(u.Host)
	if (newLocationPort == "80" || newLocationPort == "443") && err == nil {
		// Ports 80 and 443 are rewritten

	} else if strings.Contains(u.Host, ":") && err == nil && u.Host != rrr.ProxyDomain {
		// Other ports on another host are left unchanged
		return urlString, nil
	}

	// A path in OriginalHost is prepended to the URL path
	if idx := strings.IndexByte(rrr.OriginalHost, '/'); idx != -1 {
		u.Host = rrr.OriginalHost[:idx]
		u.Path = rrr.OriginalHost[idx:] + u.Path
	} else {
		u.Host = rrr.OriginalHost
	}

	if strings.Contains(rrr.ProxyDomain, "/") {
		// Removes the proxy domain's subpath from the Location path

		ProxyDomainURL := "http://" + rrr.ProxyDomain
		if rrr.UseTLS {
			ProxyDomainURL = "https://" + rrr.ProxyDomain
		}
		ru, err := url.Parse(ProxyDomainURL)
		if err == nil {
			u.Path = strings.TrimPrefix(u.Path, ru.Path)
		}
	}

	return u.String(), nil
}

// isExternalDomainName reports whether hostname is a domain name other than
// an IP address or an internal TLD (.local, .internal, .localhost, .home.arpa).
func isExternalDomainName(hostname string) bool {
	host, _, err := net.SplitHostPort(hostname)
	if err != nil {
		ip := net.ParseIP(hostname)
		if ip != nil {
			return false
		}
	} else {
		ip := net.ParseIP(host)
		if ip != nil {
			return false
		}
	}

	internalDNSTLD := []string{".local", ".internal", ".localhost", ".home.arpa"}
	for _, tld := range internalDNSTLD {
		if strings.HasSuffix(strings.ToLower(hostname), tld) {
			return false
		}
	}

	return true
}

// isLocalPath reports whether location is a path on the same host: it starts
// with "/" but not with "//" or "/\".
func isLocalPath(location string) bool {
	return strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "//") && !strings.HasPrefix(location, "/\\")
}
