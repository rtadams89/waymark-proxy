package assetcache

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cacheControl parses a Cache-Control header into lowercase directives.
// Directives without a value map to "".
func cacheControl(h http.Header) map[string]string {
	d := map[string]string{}
	for _, line := range h.Values("Cache-Control") {
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, v, _ := strings.Cut(part, "=")
			d[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return d
}

func seconds(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// notStorableReason returns why a 200 response must not be stored in a
// shared cache, or "".
func notStorableReason(h http.Header) string {
	cc := cacheControl(h)
	switch {
	case hasKey(cc, "no-store"):
		return "no-store"
	case hasKey(cc, "private"):
		return "private"
	case hasKey(cc, "no-cache"):
		return "no-cache"
	case len(h.Values("Set-Cookie")) > 0:
		return "sets a cookie"
	case h.Get("Content-Range") != "":
		return "partial content"
	}
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if strings.TrimSpace(f) == "*" {
				return "Vary: *"
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(h.Get("Pragma")), "no-cache") && h.Get("Cache-Control") == "" {
		return "no-cache"
	}
	return ""
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

// freshnessLifetime returns how long a response stays fresh from now
// (s-maxage, then max-age, then Expires, else defaultTTL), minus its Age.
// A result <= 0 means stale.
func freshnessLifetime(h http.Header, now time.Time, defaultTTL time.Duration) time.Duration {
	cc := cacheControl(h)
	var lifetime time.Duration
	explicit := false
	if v, ok := cc["s-maxage"]; ok {
		if n, ok := seconds(v); ok {
			lifetime, explicit = time.Duration(n)*time.Second, true
		}
	}
	if !explicit {
		if v, ok := cc["max-age"]; ok {
			if n, ok := seconds(v); ok {
				lifetime, explicit = time.Duration(n)*time.Second, true
			}
		}
	}
	if !explicit && h.Get("Expires") != "" {
		explicit = true
		exp, err := http.ParseTime(h.Get("Expires"))
		if err != nil {
			lifetime = 0 // invalid Expires: stale
		} else {
			date := now
			if d, err := http.ParseTime(h.Get("Date")); err == nil {
				date = d
			}
			lifetime = exp.Sub(date)
		}
	}
	if !explicit {
		lifetime = defaultTTL
	}
	if n, ok := seconds(h.Get("Age")); ok {
		lifetime -= time.Duration(n) * time.Second
	}
	return lifetime
}

// requestWantsFresh reports whether the client sent Cache-Control:
// no-cache or max-age=0.
func requestWantsFresh(r *http.Request) bool {
	cc := cacheControl(r.Header)
	if hasKey(cc, "no-cache") {
		return true
	}
	if v, ok := cc["max-age"]; ok && strings.TrimSpace(v) == "0" {
		return true
	}
	return r.Header.Get("Cache-Control") == "" && strings.EqualFold(strings.TrimSpace(r.Header.Get("Pragma")), "no-cache")
}

func requestNoStore(r *http.Request) bool {
	return hasKey(cacheControl(r.Header), "no-store")
}
