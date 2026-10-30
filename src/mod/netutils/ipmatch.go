package netutils

import (
	"net"
	"net/http"
	"strings"
)

// Get the requester IP without trusting any proxy headers
func GetRequesterIPUntrusted(r *http.Request) string {
	ip := r.RemoteAddr
	// Trim away the port number
	reqHost, _, err := net.SplitHostPort(ip)
	if err == nil {
		ip = reqHost
	}

	// Check if the IP is a valid IPv4 or IPv6 address
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return ""
	}
	return ip
}

// Get the requester IP, trust the X-Real-IP and X-Forwarded-For headers
func GetRequesterIP(r *http.Request) string {
	candidates := []string{
		r.Header.Get("X-Real-Ip"),
		r.Header.Get("CF-Connecting-IP"),
		r.Header.Get("Fastly-Client-IP"),
		r.Header.Get("X-Forwarded-For"),
	}
	for _, candidate := range candidates {
		if ip := normalizeIP(candidate); ip != "" {
			return ip
		}
	}
	return GetRequesterIPUntrusted(r)
}

/*
Extract the first IP address from a header value, e.g.

	127.0.0.1:61001
	[15c4:cbb4:cc98:4291:ffc1:3a46:06a1:51a7]:61002
	127.0.0.1
	158.250.160.114,109.21.249.211
	[15c4:cbb4:cc98:4291:ffc1:3a46:06a1:51a7],109.21.249.211

Returns empty string if the value is not a valid IP address.
*/
func normalizeIP(raw string) string {
	raw, _, _ = strings.Cut(raw, ",")
	raw = strings.TrimSpace(raw)
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	if net.ParseIP(raw) == nil {
		return ""
	}
	return raw
}

// Match the IP address with a wildcard string
func MatchIpWildcard(ipAddress, wildcard string) bool {
	// Split IP address and wildcard into octets
	ipOctets := strings.Split(ipAddress, ".")
	wildcardOctets := strings.Split(wildcard, ".")

	// Check that both have 4 octets
	if len(ipOctets) != 4 || len(wildcardOctets) != 4 {
		return false
	}

	// Check each octet to see if it matches the wildcard or is an exact match
	for i := 0; i < 4; i++ {
		if wildcardOctets[i] == "*" {
			continue
		}
		if ipOctets[i] != wildcardOctets[i] {
			return false
		}
	}

	return true
}

// Match ip address with CIDR
func MatchIpCIDR(ip string, cidr string) bool {
	// Trim away scope ID if present in IP (e.g. fe80::1%eth0)
	if i := strings.Index(ip, "%"); i != -1 {
		ip = ip[:i]
	}

	// parse the CIDR string
	_, cidrnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}

	// parse the IP address
	ipAddr := net.ParseIP(ip)

	// check if the IP address is within the CIDR range
	return cidrnet.Contains(ipAddr)
}

// Check if an Ip string is ipv6
func IsIPv6(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	return ip.To4() == nil && ip.To16() != nil
}
