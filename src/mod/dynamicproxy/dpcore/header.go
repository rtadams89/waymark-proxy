package dpcore

import (
	"net"
	"net/http"
	"strings"
)

// removeHeaders removes the headers listed in "Connection" and the hop-by-hop headers.
func removeHeaders(header http.Header) {
	if c := header.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			if f = strings.TrimSpace(f); f != "" {
				header.Del(f)
			}
		}
	}

	for _, h := range hopHeaders {
		if header.Get(h) != "" {
			header.Del(h)
		}
	}

	//Restore the Upgrade header if any
	if header.Get("Zr-Origin-Upgrade") != "" {
		header.Set("Upgrade", header.Get("Zr-Origin-Upgrade"))
		header.Del("Zr-Origin-Upgrade")
	}
}

// setNoCacheHeader sets Cache-Control to no-store.
func setNoCacheHeader(header http.Header) {
	header.Del("Cache-Control")
	header.Set("Cache-Control", "no-store")
}

// rewriteUserAgent sets UA as the User-Agent when the request has none.
func rewriteUserAgent(header http.Header, UA string) {
	if header.Get("User-Agent") == "" {
		header.Del("User-Agent")
		header.Set("User-Agent", UA)
	}
}

// addXForwardedForHeader appends the client IP to X-Forwarded-For and sets
// X-Real-Ip if missing.
func addXForwardedForHeader(req *http.Request) {
	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		// Appends to existing X-Forwarded-For values
		if prior, ok := req.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(prior, ", ") + ", " + clientIP
		}
		req.Header.Set("X-Forwarded-For", clientIP)
		if req.TLS != nil {
			req.Header.Set("X-Forwarded-Proto", "https")
		} else {
			req.Header.Set("X-Forwarded-Proto", "http")
		}

		if req.Header.Get("X-Real-Ip") == "" {
			CF_Connecting_IP := req.Header.Get("CF-Connecting-IP")
			Fastly_Client_IP := req.Header.Get("Fastly-Client-IP")
			if CF_Connecting_IP != "" {
				req.Header.Set("X-Real-Ip", CF_Connecting_IP)
			} else if Fastly_Client_IP != "" {
				req.Header.Set("X-Real-Ip", Fastly_Client_IP)
			} else {
				// First entry of X-Forwarded-For
				ips := strings.Split(clientIP, ",")
				if len(ips) > 0 {
					req.Header.Set("X-Real-Ip", strings.TrimSpace(ips[0]))
				}
			}
		}

	}
}

// injectUserDefinedHeaders sets the user headers; an empty value removes the
// header, and an empty key ends the list.
func injectUserDefinedHeaders(header http.Header, userHeaders [][]string) {
	for _, userHeader := range userHeaders {
		if len(userHeader) == 0 {
			return
		}
		headerKey := userHeader[0]
		headerValue := userHeader[1]
		if headerValue == "" {
			header.Del(headerKey)
			continue
		}

		header.Del(headerKey)
		header.Set(headerKey, headerValue)
	}
}
