package main

/*
	Floating IP details for the HA page: floating IPs held, whether the
	installed root helper files match this build, and Layer 4 proxies
	listening on a floating IP.
*/

import (
	"net"
	"os"
	"strings"

	"imuslab.com/zoraxy/mod/hacluster"
)

// haFloatingIPsHeld lists every floating IP with whether this node holds it.
func haFloatingIPsHeld(cfg *hacluster.Config) []map[string]any {
	local := localIPs()
	out := []map[string]any{}
	for _, f := range cfg.FloatingIPs() {
		held := false
		if ip := net.ParseIP(f.Address); ip != nil {
			for _, l := range local {
				if l.Equal(ip) {
					held = true
				}
			}
		}
		out = append(out, map[string]any{"address": f.Address, "prefix": f.Prefix, "label": f.Label, "held": held})
	}
	return out
}

// haCountFloatingIPsHeld returns how many floating IPs this node holds.
func haCountFloatingIPsHeld(cfg *hacluster.Config) int {
	n := 0
	for _, f := range haFloatingIPsHeld(cfg) {
		if f["held"] == true {
			n++
		}
	}
	return n
}

// haHelperState reports whether the installed helper files match this build.
func haHelperState() map[string]any {
	outdated, ok := hacluster.OutdatedInstalledFiles(hacluster.DefaultHelperPath, hacluster.DefaultScriptsDir)
	return map[string]any{"checked": ok, "outdated": outdated}
}

// haNonlocalBind reports net.ipv4.ip_nonlocal_bind and the Layer 4 proxies
// that listen on a floating IP.
func haNonlocalBind(cfg *hacluster.Config) map[string]any {
	enabled, known := false, false
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_nonlocal_bind"); err == nil {
		known = true
		enabled = strings.TrimSpace(string(b)) == "1"
	}
	floating := map[string]bool{}
	for _, f := range cfg.FloatingIPs() {
		floating[f.Address] = true
	}
	proxies := []map[string]string{}
	if l4ProxyManager != nil {
		for _, rule := range l4ProxyManager.List() {
			c := rule.View()
			host, _, err := net.SplitHostPort(strings.TrimSpace(c.ListeningAddress))
			if err != nil {
				continue
			}
			if ip := net.ParseIP(host); ip != nil && floating[ip.String()] {
				proxies = append(proxies, map[string]string{"name": c.Name, "address": c.ListeningAddress})
			}
		}
	}
	return map[string]any{"enabled": enabled, "known": known, "l4Proxies": proxies}
}
