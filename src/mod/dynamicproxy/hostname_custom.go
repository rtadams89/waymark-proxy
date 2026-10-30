package dynamicproxy

import (
	"net"
	"strings"
)

// requestHostname returns the host of a Host header without the port,
// including IPv6 hosts.
func requestHostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}
