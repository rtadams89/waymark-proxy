package netutils_test

import (
	"testing"

	"imuslab.com/zoraxy/mod/netutils"
)

func TestMatchIpWildcard_IPv6(t *testing.T) {
	// MatchIpWildcard returns false for IPv6 addresses
	tests := []struct {
		ip       string
		wildcard string
		want     bool
	}{
		{"fd7a:115c:a1e0::e101:6f0f", "fd7a:115c:a1e0::e101:6f0f", false},
		{"fd7a:115c:a1e0::e101:6f0f", "*:*:*:*:*:*:*:*", false},
	}
	for _, tt := range tests {
		got := netutils.MatchIpWildcard(tt.ip, tt.wildcard)
		if got != tt.want {
			t.Errorf("MatchIpWildcard(%q, %q) = %v, want %v", tt.ip, tt.wildcard, got, tt.want)
		}
	}
}

func TestMatchIpCIDR_IPv6(t *testing.T) {
	tests := []struct {
		ip   string
		cidr string
		want bool
	}{
		{"fd7a:115c:a1e0::e101:6f0f", "fd7a:115c:a1e0::/48", true},
		{"fd7a:115c:a1e0::e101:6f0f", "fd7a:115c:a1e0::/64", true},
		{"fd7a:115c:a1e0::e101:6f0f", "fd7a:115c:a1e1::/48", false},
		{"fd7a:115c:a1e0::e101:6f0f", "fd7a:115c:a1e0::/128", false},
	}
	for _, tt := range tests {
		got := netutils.MatchIpCIDR(tt.ip, tt.cidr)
		if got != tt.want {
			t.Errorf("MatchIpCIDR(%q, %q) = %v, want %v", tt.ip, tt.cidr, got, tt.want)
		}
	}
}
