package dynamicproxy

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"imuslab.com/zoraxy/mod/capture"
)

/*
	Request handling order: special routing rules (e.g. ACME), redirects,
	host rules (ACL, exploit detection, rate limit, CAPTCHA, authentication,
	plugins, virtual directories, proxy), then the default site.
*/

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Traffic capture
	if cw, cr, done := capture.Begin(w, r); done != nil {
		defer done()
		w, r = cw, cr
	}

	/* Special routing rules */
	matchedRoutingRule := h.Parent.GetMatchingRoutingRule(r)
	if matchedRoutingRule != nil {
		matchedRoutingRule.Route(w, r)
		return
	}

	/* Redirects */
	if h.Parent.Option.RedirectRuleTable.IsRedirectable(r) {
		statusCode := h.Parent.Option.RedirectRuleTable.HandleRedirect(w, r)
		h.Parent.logRequest(r, statusCode != 500, statusCode, "redirect", r.Host, "", nil)
		return
	}

	/* Host rules */
	domainOnly := requestHostname(r.Host)
	sep := h.Parent.GetProxyEndpointForRequest(r) // skips rules bound to another address
	if sep != nil && !sep.Disabled {
		// ACL
		if h.handleAccessRouting(w, r, sep) {
			return
		}

		/* Exploit Detection */
		if sep.detector != nil {
			if sep.detector.CheckIsAttack(w, r) {
				statusCode := 403
				if sep.detector != nil {
					statusCode = sep.detector.GetResponseStatusCode()
				}
				h.Parent.logRequest(r, false, statusCode, "exploit-blocked", domainOnly, "blocked", sep)
				return
			}
		}

		// Rate Limit
		if sep.RequireRateLimit {
			err := h.handleRateLimitRouting(w, r, sep)
			if err != nil {
				if !sep.DisableLogging {
					clientIP := h.Parent.GetClientIPForEndpoint(r, sep)
					h.Parent.Option.Logger.LogHTTPRequest(r, "host", 307, r.Host, "", clientIP)
				}
				return
			}
		}

		// CAPTCHA Gating
		if handled, challenged := h.Parent.handleCaptchaGating(w, r, sep); handled {
			if challenged {
				h.Parent.logRequest(r, false, 403, "captcha-required", domainOnly, "captcha", sep)
			}
			return
		}

		respWritten := handleAuthProviderRouting(sep, w, r, h)
		if respWritten {
			return
		}

		if h.Parent.Option.PluginManager != nil && h.Parent.Option.PluginManager.HandleRoute(w, r, sep.Tags, h.Parent.GetClientIPForEndpoint(r, sep)) {
			return
		}

		proxyingPath := strings.TrimSpace(r.RequestURI)
		targetProxyEndpoint := sep.GetVirtualDirectoryHandlerFromRequestURI(proxyingPath)
		if targetProxyEndpoint != nil && !targetProxyEndpoint.Disabled {
			h.vdirRequest(w, r, targetProxyEndpoint)
			return
		} else if !strings.HasSuffix(proxyingPath, "/") && sep.ProxyType != ProxyTypeRoot {
			potentialProxtEndpoint := sep.GetVirtualDirectoryHandlerFromRequestURI(proxyingPath + "/")
			if potentialProxtEndpoint != nil && !potentialProxtEndpoint.Disabled {
				// Adds the missing trailing slash
				http.Redirect(w, r, r.RequestURI+"/", http.StatusTemporaryRedirect)
				if !sep.DisableLogging {
					clientIP := h.Parent.GetClientIPForEndpoint(r, sep)
					h.Parent.Option.Logger.LogHTTPRequest(r, "redirect", 307, r.Host, "", clientIP)
				}
				return
			}
		}

		h.hostRequest(w, r, sep)
		return
	}

	/* Default site */

	// ACL of the default site
	blocked := h.handleAccessRouting(w, r, h.Parent.Root)
	if blocked {
		return
	}

	proxyingPath := strings.TrimSpace(r.RequestURI)
	if !strings.HasSuffix(proxyingPath, "/") {
		potentialProxtEndpoint := h.Parent.getTargetProxyEndpointFromRequestURI(proxyingPath + "/")
		if potentialProxtEndpoint != nil {
			// Adds the missing trailing slash
			http.Redirect(w, r, r.RequestURI+"/", http.StatusTemporaryRedirect)
		} else {
			h.handleRootRouting(w, r)
		}
	} else {
		h.handleRootRouting(w, r)
	}
}

// handleRootRouting handles requests no host rule, virtual directory or
// special routing rule matched, according to the default site option.
func (h *ProxyHandler) handleRootRouting(w http.ResponseWriter, r *http.Request) {
	domainOnly := requestHostname(r.Host)

	proot := h.Parent.Root
	switch proot.DefaultSiteOption {
	case DefaultSite_InternalStaticWebServer:
		fallthrough
	case DefaultSite_ReverseProxy:
		proxyingPath := strings.TrimSpace(r.RequestURI)
		targetProxyEndpoint := proot.GetVirtualDirectoryHandlerFromRequestURI(proxyingPath)
		if targetProxyEndpoint != nil && !targetProxyEndpoint.Disabled {
			h.vdirRequest(w, r, targetProxyEndpoint)
			return
		} else if !strings.HasSuffix(proxyingPath, "/") && proot.ProxyType != ProxyTypeRoot {
			potentialProxtEndpoint := proot.GetVirtualDirectoryHandlerFromRequestURI(proxyingPath + "/")
			if potentialProxtEndpoint != nil && !targetProxyEndpoint.Disabled {
				// Adds the missing trailing slash
				http.Redirect(w, r, r.RequestURI+"/", http.StatusTemporaryRedirect)
				return
			}
		}

		h.hostRequest(w, r, h.Parent.Root)
	case DefaultSite_Redirect:
		redirectTarget := strings.TrimSpace(proot.DefaultSiteValue)
		if redirectTarget == "" {
			redirectTarget = "about:blank"
		}

		if !strings.HasPrefix(redirectTarget, "http://") && !strings.HasPrefix(redirectTarget, "https://") {
			redirectTarget = "http://" + redirectTarget
		}

		// Redirects to itself are sent to the root
		parsedURL, err := url.Parse(redirectTarget)
		if err != nil {
			h.hostRequest(w, r, h.Parent.Root)
			return
		}
		hostname := parsedURL.Hostname()
		if hostname == domainOnly {
			h.Parent.logRequest(r, false, 500, "root-redirect", domainOnly, "", h.Parent.Root)
			http.Error(w, "Loopback redirects due to invalid settings", 500)
			return
		}

		h.Parent.logRequest(r, false, 307, "root-redirect", domainOnly, "", h.Parent.Root)
		http.Redirect(w, r, redirectTarget, http.StatusTemporaryRedirect)
	case DefaultSite_NotFoundPage:
		h.serve404PageWithTemplate(w, r)
	case DefaultSite_NoResponse:
		h.Parent.logRequest(r, false, 444, "root-no_resp", domainOnly, "", h.Parent.Root)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		conn.Close()
	case DefaultSite_TeaPot:
		h.Parent.logRequest(r, false, 418, "root-teapot", domainOnly, "", h.Parent.Root)
		http.Error(w, "I'm a teapot", http.StatusTeapot)
	default:
		h.Parent.logRequest(r, false, 544, "root-unknown", domainOnly, "", h.Parent.Root)
		http.Error(w, "544 - No Route Defined", 544)
	}
}

func (h *ProxyHandler) serve404PageWithTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	template, err := os.ReadFile(filepath.Join(h.Parent.Option.WebDirectory, "templates/notfound.html"))
	if err != nil {
		w.Write(page_hosterror)
	} else {
		w.Write(template)
	}
}
