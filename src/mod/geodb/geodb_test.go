package geodb

import (
	"testing"

	"imuslab.com/zoraxy/mod/info/logger"
)

func TestResolveCountryCodeFromIP(t *testing.T) {
	store, err := NewGeoDb(nil, &StoreOptions{
		AllowSlowIpv4LookUp:          false, //false to use fast geodb mode for ipv4 lookup
		AllowSlowIpv6Lookup:          false, //false to use fast geodb mode for ipv6 lookup
		Logger:                       &logger.Logger{},
		SlowLookupCacheClearInterval: 0,
	})
	if err != nil {
		t.Errorf("error creating store: %v", err)
		return
	}

	// Test an IP address that should return a valid country code
	knownIpCountryMap := [][]string{
		{"3.224.220.101", "US"},
		{"176.113.115.113", "RU"},
		{"65.21.233.213", "FI"},
		{"94.23.207.193", "FR"},
		{"77.131.21.232", "FR"},
		{"1.64.26.128", "HK"},
		{"1.66.46.73", "JP"},
		{"1.68.12.37", "CN"},
		{"1.74.243.23", "JP"},
		{"2a01:4f9:3070:124a:1234:5678:9abc:def0", "DE"},
		{"2a01:4f9:3070:124b:dead:beef:cafe:1", "DE"},
		{"2a00:20:4290:164d:a4a3:efb6:aea1:4b8e", "DE"},
		{"100.67.181.105", "CarrierNAT"},
	}

	for _, testcase := range knownIpCountryMap {
		ip := testcase[0]
		expected := testcase[1]
		info, err := store.ResolveCountryCodeFromIP(ip)
		if err != nil {
			t.Errorf("error resolving country code for IP %s: %v", ip, err)
			return
		}
		if info.CountryIsoCode != expected {
			t.Errorf("expected country code %s, but got %s for IP %s", expected, info.CountryIsoCode, ip)
		}
	}

	// Test reserved IP addresses that should return zone names
	reservedIpZoneMap := [][]string{
		{"127.0.0.1", "Loopback"},
		{"10.0.0.1", "Private"},
		{"192.168.1.1", "Private"},
		{"172.16.0.1", "Private"},
		{"169.254.1.1", "LinkLocal"},
		{"224.0.0.1", "Multicast"},
		{"::1", "Loopback"},
		{"fe80::1", "LinkLocal"},
		{"fc00::1", "UniqueLocal"},
		{"ff00::1", "Multicast"},
	}

	for _, testcase := range reservedIpZoneMap {
		ip := testcase[0]
		expected := testcase[1]
		info, err := store.ResolveCountryCodeFromIP(ip)
		if err != nil {
			t.Errorf("error resolving zone for reserved IP %s: %v", ip, err)
			return
		}
		if info.CountryIsoCode != expected {
			t.Errorf("expected zone %s, but got %s for reserved IP %s", expected, info.CountryIsoCode, ip)
		}
	}

	// Resolve country codes from 100 concurrent goroutines
	for i := 0; i < 100; i++ {
		go func() {
			for _, testcase := range knownIpCountryMap {
				ip := testcase[0]
				expected := testcase[1]
				info, err := store.ResolveCountryCodeFromIP(ip)
				if err != nil {
					t.Errorf("error resolving country code for IP %s: %v", ip, err)
					return
				}
				if info.CountryIsoCode != expected {
					t.Errorf("expected country code %s, but got %s for IP %s", expected, info.CountryIsoCode, ip)
				}
			}
		}()
	}

}
