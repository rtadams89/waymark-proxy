// Package assetcache is a shared in-memory cache for static files served
// through proxy rules.
//
// Static files are URLs whose extension is in a rule's list, and optionally
// extensionless URLs with a static content type. Freshness follows the
// upstream's Cache-Control / Expires headers, or the rule's default
// lifetime. Responses marked no-store, no-cache or private, responses that
// set cookies, and partial responses are not stored. Expired files with an
// ETag or Last-Modified are revalidated. All rules share one memory budget
// with least recently used eviction.
package assetcache

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Config is a proxy rule's cache settings (stored in the rule's config file).
type Config struct {
	Enabled           bool     `json:"enabled"`
	Extensions        []string `json:"extensions"`        // lowercase, without the dot
	MatchContentTypes bool     `json:"matchContentTypes"` // also cache extensionless URLs with a static content type
	DefaultTTLSeconds int      `json:"defaultTTLSeconds"` // used when the upstream sends no freshness headers
	MaxFileMB         int      `json:"maxFileMB"`         // larger responses are not stored
}

// DefaultExtensions are the file types cached unless a rule changes the list.
var DefaultExtensions = []string{
	"css", "js", "mjs", "map",
	"png", "jpg", "jpeg", "gif", "webp", "avif", "svg", "ico", "bmp",
	"woff", "woff2", "ttf", "otf", "eot",
	"mp3", "mp4", "webm", "ogg", "wav", "flac", "m4a",
	"wasm",
}

// StaticContentTypes are matched (by prefix) for URLs without an extension
// when MatchContentTypes is on.
var StaticContentTypes = []string{
	"image/", "font/", "audio/", "video/",
	"text/css", "text/javascript", "application/javascript", "application/x-javascript",
	"application/wasm", "application/font-woff", "application/vnd.ms-fontobject", "application/x-font-",
}

const (
	minTTLSeconds = 1
	maxTTLSeconds = 365 * 24 * 3600
	maxFileMBCap  = 1024
)

// DefaultConfig returns the default cache settings (caching off).
func DefaultConfig() *Config {
	return &Config{
		Enabled:           false,
		Extensions:        append([]string{}, DefaultExtensions...),
		MatchContentTypes: true,
		DefaultTTLSeconds: 3600,
		MaxFileMB:         10,
	}
}

var extPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Validate returns a normalized copy of c, or an error for the first
// invalid setting.
func (c *Config) Validate() (*Config, error) {
	if c == nil {
		return nil, errors.New("no settings given")
	}
	v := *c
	seen := map[string]bool{}
	v.Extensions = []string{}
	for _, e := range c.Extensions {
		e = strings.ToLower(strings.TrimSpace(e))
		e = strings.TrimPrefix(e, "*")
		e = strings.TrimPrefix(e, ".")
		if e == "" {
			continue
		}
		if !extPattern.MatchString(e) {
			return nil, fmt.Errorf("invalid file extension %q (letters and digits only, for example: css)", e)
		}
		if !seen[e] {
			seen[e] = true
			v.Extensions = append(v.Extensions, e)
		}
	}
	if v.Enabled && len(v.Extensions) == 0 && !v.MatchContentTypes {
		return nil, errors.New("nothing to cache: add at least one file extension or turn on content type matching")
	}
	if v.DefaultTTLSeconds < minTTLSeconds || v.DefaultTTLSeconds > maxTTLSeconds {
		return nil, fmt.Errorf("default lifetime must be between %d second and %d seconds (1 year)", minTTLSeconds, maxTTLSeconds)
	}
	if v.MaxFileMB < 1 || v.MaxFileMB > maxFileMBCap {
		return nil, fmt.Errorf("largest file size must be between 1 and %d MB", maxFileMBCap)
	}
	return &v, nil
}

// On reports whether caching is turned on (nil-safe).
func (c *Config) On() bool {
	return c != nil && c.Enabled
}

func (c *Config) hasExtension(ext string) bool {
	for _, e := range c.Extensions {
		if e == ext {
			return true
		}
	}
	return false
}

func isStaticContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	for _, p := range StaticContentTypes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}
