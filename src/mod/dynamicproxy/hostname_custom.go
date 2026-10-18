package dynamicproxy

import (
	"net"
	"strings"
)

// requestHostname returns the host of a request's Host header without the
// port (custom fork). The old code cut at the first colon, which turned an
// IPv6 host such as [fd00::1]:443 into "[".
func requestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}
