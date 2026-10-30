package forward

import (
	"strings"

	"imuslab.com/zoraxy/mod/pathmatch"
)

// Ignored paths for forward auth: request path prefixes served without
// authentication.

// requestPathWithinPrefix reports whether the normalized request path is
// prefix or below it.
func requestPathWithinPrefix(requestURI string, prefix string) bool {
	return pathmatch.RequestPathWithinPrefix(requestURI, prefix)
}

// IsIgnoredPath reports whether requestURI is within one of the ignored
// path prefixes; empty entries are skipped.
func (ar *AuthRouter) IsIgnoredPath(requestURI string) bool {
	if ar == nil {
		return false
	}
	for _, prefix := range ar.options.IgnoredPaths {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if requestPathWithinPrefix(requestURI, prefix) {
			return true
		}
	}
	return false
}
