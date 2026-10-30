package main

import (
	"encoding/json"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/tlscert"
)

func testCluster() *hacluster.Config {
	c := hacluster.DefaultConfig()
	c.Enabled, c.VIP, c.VIPHostname = true, "10.10.20.4", "waymark.home.example"
	c.Nodes = []hacluster.Node{
		{Name: "a", Address: "10.10.20.5", Hostname: "waymark01.home.example", Priority: 150},
		{Name: "b", Address: "10.10.20.6", Hostname: "waymark02.home.example", Priority: 100},
	}
	return c
}

func TestAdminRuleNewSingleNode(t *testing.T) {
	s := adminConsoleSettings{Address: "10.0.0.5", Hostname: "proxy.lan"}
	req := adminRequiredHosts(nil, s)
	if strings.Join(req, ",") != "proxy.lan,10.0.0.5" {
		t.Fatalf("required %v", req)
	}
	ep := adminBuildRule(nil, req, adminDefaultRoot(nil, s), "127.0.0.1:8000")
	if ep.RootOrMatchingDomain != "proxy.lan" || strings.Join(ep.MatchingDomainAlias, ",") != "10.0.0.5" || !ep.SystemAutoRoot || ep.System != adminRuleSystem {
		t.Fatalf("rule %s %v", ep.RootOrMatchingDomain, ep.MatchingDomainAlias)
	}
	if len(ep.ActiveOrigins) != 1 || ep.ActiveOrigins[0].OriginIpOrDomain != "127.0.0.1:8000" || ep.Disabled {
		t.Fatal("destination")
	}
	if !adminTLSAuto(ep.TlsOptions) || ep.TlsOptions.PreferredCertificate["10.0.0.5"] != adminCertName {
		t.Fatal("self-signed certificate not used")
	}
	if ep.AssetCache == nil || !ep.AssetCache.Enabled || ep.HealthCheck.ActiveEnabled || !ep.DisableUptimeMonitor {
		t.Fatal("cache on, health checks off")
	}
}

func TestAdminRuleBecomesClusterRule(t *testing.T) {
	s := adminConsoleSettings{Address: "10.10.20.5", Hostname: "waymark01.home.example"}
	ep := adminBuildRule(nil, adminRequiredHosts(nil, s), adminDefaultRoot(nil, s), "127.0.0.1:8000")
	ep.MatchingDomainAlias = append(ep.MatchingDomainAlias, "extra.example")
	c := testCluster()
	req := adminRequiredHosts(c, s)
	ep2 := adminBuildRule(ep, req, adminDefaultRoot(c, s), "127.0.0.1:8000")
	if ep2.RootOrMatchingDomain != "waymark.home.example" {
		t.Fatalf("main domain should follow the floating IP while chosen by Waymark: %s", ep2.RootOrMatchingDomain)
	}
	got := strings.Join(ep2.MatchingDomainAlias, ",")
	for _, h := range []string{"10.10.20.4", "10.10.20.5", "10.10.20.6", "waymark01.home.example", "waymark02.home.example", "extra.example"} {
		if !strings.Contains(got, h) {
			t.Errorf("%s missing from %s", h, got)
		}
	}
	// Node b leaves: its hosts go, the user's alias stays
	c.Nodes = c.Nodes[:1]
	ep3 := adminBuildRule(ep2, adminRequiredHosts(c, s), adminDefaultRoot(c, s), "127.0.0.1:8000")
	got = strings.Join(ep3.MatchingDomainAlias, ",")
	if strings.Contains(got, "10.10.20.6") || strings.Contains(got, "waymark02") || !strings.Contains(got, "extra.example") {
		t.Fatalf("after removing node b: %s", got)
	}
	// Stable: building again changes nothing
	ep4 := adminBuildRule(ep3, adminRequiredHosts(c, s), adminDefaultRoot(c, s), "127.0.0.1:8000")
	if adminEndpointJSON(ep3) != adminEndpointJSON(ep4) {
		t.Fatal("not stable")
	}
}

func TestAdminRuleKeepsEditableSettings(t *testing.T) {
	var old dynamicproxy.ProxyEndpoint
	json.Unmarshal([]byte(`{"ProxyType":1,"System":"admin-console","RootOrMatchingDomain":"waymark.home.example",
		"MatchingDomainAlias":["10.10.20.4","10.10.20.5","10.10.20.6","waymark01.home.example","waymark02.home.example"],
		"ActiveOrigins":[{"OriginIpOrDomain":"127.0.0.1:8000","Weight":1}],"Disabled":true,"ListenIP":"10.10.20.4",
		"TlsOptions":{"DisableSNI":false,"PreferredCertificate":{"zoraxy01.home.example":"zoraxy","waymark.home.example":"wild"}},
		"AuthenticationProvider":{"AuthMethod":1},"AccessFilterUUID":"728ce19b","Tags":["infra"],
		"AssetCache":{"enabled":true,"extensions":["css"],"defaultTTLSeconds":60,"maxFileMB":1}}`), &old)
	c := testCluster()
	c.VIPHostname, c.Nodes[0].Hostname, c.Nodes[1].Hostname = "", "", ""
	ep := adminBuildRule(&old, adminRequiredHosts(c, adminConsoleSettings{}), adminDefaultRoot(c, adminConsoleSettings{}), "127.0.0.1:8000")
	if ep.RootOrMatchingDomain != "waymark.home.example" || ep.SystemAutoRoot {
		t.Fatal("a main domain set by the user is kept")
	}
	if strings.Join(ep.MatchingDomainAlias, ",") != "10.10.20.4,10.10.20.5,10.10.20.6,waymark01.home.example,waymark02.home.example" {
		t.Fatalf("aliases %v", ep.MatchingDomainAlias)
	}
	if ep.Disabled || ep.ListenIP != "" || ep.AuthenticationProvider.AuthMethod != dynamicproxy.AuthMethodNone {
		t.Fatal("fixed settings not put back")
	}
	if ep.AccessFilterUUID != "728ce19b" || ep.Tags[0] != "infra" || ep.AssetCache.DefaultTTLSeconds != 60 {
		t.Fatal("editable settings not kept")
	}
	if adminTLSAuto(ep.TlsOptions) || len(ep.TlsOptions.PreferredCertificate) != 1 || ep.TlsOptions.PreferredCertificate["waymark.home.example"] != "wild" {
		t.Fatalf("certificate choice: %v", ep.TlsOptions.PreferredCertificate)
	}
}

func TestAdminRuleUserRoot(t *testing.T) {
	s := adminConsoleSettings{Address: "10.0.0.5"}
	ep := adminBuildRule(nil, adminRequiredHosts(nil, s), adminDefaultRoot(nil, s), "127.0.0.1:8000")
	ep.RootOrMatchingDomain, ep.SystemAutoRoot = "admin.example", false
	ep2 := adminBuildRule(ep, adminRequiredHosts(nil, s), adminDefaultRoot(nil, s), "127.0.0.1:8000")
	if ep2.RootOrMatchingDomain != "admin.example" || strings.Join(ep2.MatchingDomainAlias, ",") != "10.0.0.5" {
		t.Fatalf("%s %v", ep2.RootOrMatchingDomain, ep2.MatchingDomainAlias)
	}
	if ep2.TlsOptions.PreferredCertificate["admin.example"] != adminCertName {
		t.Fatal("new main domain not on the self-signed certificate")
	}
}

func TestAdminDestination(t *testing.T) {
	saved := *webUIPort
	defer func() { *webUIPort = saved }()
	for in, want := range map[string]string{":8000": "127.0.0.1:8000", "0.0.0.0:9000": "127.0.0.1:9000", "127.0.0.1:8001": "127.0.0.1:8001", "10.0.0.5:8000": "10.0.0.5:8000", "localhost:8002": "127.0.0.1:8002"} {
		*webUIPort = in
		if got := adminDestination(); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestSelfSignedIPs(t *testing.T) {
	dir := t.TempDir()
	if err := tlscert.WriteSelfSignedCertificate(dir, "x", []string{"a.example", "10.0.0.5"}, adminCertValidity); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tlscert.CertificateNames(dir+"/x.pem"), ","); got != "10.0.0.5,a.example" {
		t.Fatal(got)
	}
}
