package zorxauth

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// requestScheme returns http or https for the request, using the first
// X-Forwarded-Proto value if present.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto != "" {
		first := strings.TrimSpace(strings.Split(proto, ",")[0])
		if strings.EqualFold(first, "https") {
			return "https"
		}
	}
	return "http"
}

// hasAbsoluteHTTPScheme reports whether raw starts with http:// or https://
// (any case).
func hasAbsoluteHTTPScheme(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// ensureAbsoluteHTTPURL returns raw as an absolute http(s) URL, adding
// fallbackScheme to host-only values.
func ensureAbsoluteHTTPURL(raw, fallbackScheme string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty URL")
	}
	if strings.HasPrefix(raw, "/") {
		return "", errors.New("URL must include a host")
	}
	if fallbackScheme != "http" && fallbackScheme != "https" {
		fallbackScheme = "https"
	}

	if !hasAbsoluteHTTPScheme(raw) {
		raw = fallbackScheme + "://" + strings.TrimLeft(raw, "/")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("URL scheme must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("URL missing host")
	}

	// Drops a lone trailing slash
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

// buildSSOAuthRedirect builds the SSO login URL with a redirect query parameter
// pointing at the original protected resource.
func buildSSOAuthRedirect(ssoBase, originalURL string) (string, error) {
	u, err := url.Parse(ssoBase)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("SSO redirect URL must be absolute")
	}
	q := u.Query()
	q.Set("redirect", originalURL)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// buildOriginalRequestURL returns the absolute URL of the request, with
// nested redirect values unwrapped.
func buildOriginalRequestURL(r *http.Request) string {
	scheme := requestScheme(r)
	candidate := scheme + "://" + r.Host + r.URL.RequestURI()
	return unwrapNestedRedirectTarget(candidate, r.Host)
}

// unwrapNestedRedirectTarget returns the innermost redirect= value pointing
// at the same host whose path is a single host-like segment.
func unwrapNestedRedirectTarget(candidate, host string) string {
	const maxDepth = 8
	for i := 0; i < maxDepth; i++ {
		u, err := url.Parse(candidate)
		if err != nil {
			return candidate
		}
		if !strings.EqualFold(u.Hostname(), hostnameOnly(host)) {
			return candidate
		}

		path := strings.Trim(u.Path, "/")
		if path == "" || strings.Contains(path, "/") || !strings.Contains(path, ".") {
			return candidate
		}

		redirectParam := u.Query().Get("redirect")
		if redirectParam == "" {
			return candidate
		}
		next, err := url.Parse(redirectParam)
		if err != nil || next.Scheme == "" || next.Host == "" {
			return candidate
		}
		if !strings.EqualFold(next.Hostname(), hostnameOnly(host)) {
			return candidate
		}
		candidate = redirectParam
	}
	return candidate
}

// sessionTarget parses raw as an http(s) URL without user info whose host
// is served by this proxy.
func (ar *AuthRouter) sessionTarget(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Hostname() == "" {
		return nil, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, false
	}
	if ar.IsServedHost == nil || !ar.IsServedHost(u.Hostname()) {
		return nil, false
	}
	u.Scheme = scheme
	return u, true
}

func hostnameOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return host
}

// ssoRedirectWouldLoop reports whether ssoBase is on the same host as the
// request.
func ssoRedirectWouldLoop(ssoBase string, r *http.Request) bool {
	u, err := url.Parse(ssoBase)
	if err != nil || u.Host == "" {
		return true
	}
	return strings.EqualFold(u.Hostname(), hostnameOnly(r.Host))
}

// sameHostRedirect returns the path and query of raw on the current host;
// raw may be a path or an http(s) URL with the request's host name, else "/".
func sameHostRedirect(raw string, r *http.Request) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	if u.Scheme != "" || u.Host != "" {
		if (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) || u.User != nil ||
			!strings.EqualFold(u.Hostname(), hostnameOnly(r.Host)) {
			return "/"
		}
	} else if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return "/"
	}
	// Path and query only, leading slashes and backslashes collapsed
	target := &url.URL{Path: "/" + strings.TrimLeft(u.Path, "/\\"), RawQuery: u.RawQuery}
	return target.String()
}
