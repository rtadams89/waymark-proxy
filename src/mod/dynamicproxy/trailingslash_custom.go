package dynamicproxy

import (
	"net/http"
	"strings"
)

// trailingSlashLocation returns the request's path with a trailing slash,
// with the query kept and leading slashes and backslashes collapsed to one.
func trailingSlashLocation(r *http.Request) string {
	loc := "/" + strings.TrimLeft(r.URL.EscapedPath(), "/\\") + "/"
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	return loc
}
