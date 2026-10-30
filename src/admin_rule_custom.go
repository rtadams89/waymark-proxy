package main

/*
	The admin console rule: the HTTP proxy rule that serves Waymark's own
	admin console. It is created on first start and kept answering on the
	required hosts, sending to the admin console's listener, with fixed
	settings that cannot be changed. It is only created and changed on the
	active node or a node not in a cluster.
*/

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/assetcache"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/hacluster"
	"imuslab.com/zoraxy/mod/healthcheck"
	"imuslab.com/zoraxy/mod/tlscert"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	adminRuleSystem   = "admin-console"
	adminCertName     = "waymark-admin-console"
	adminSettingsFile = "admin_console.json"
	adminCertValidity = 10 * 365 * 24 * time.Hour
)

var resetAdminRuleFlag = flag.Bool("reset-admin-rule", false, "Custom fork: rebuild the admin console proxy rule (addresses, destination, self-signed certificate, the Allow all ACL) and exit")

var adminMu sync.Mutex

type adminConsoleSettings struct {
	Address  string `json:"address"`
	Hostname string `json:"hostname"`
}

func adminSettingsPath() string { return filepath.Join(CONF_FOLDER, adminSettingsFile) }

func readAdminSettings() (adminConsoleSettings, bool) {
	var s adminConsoleSettings
	b, err := os.ReadFile(adminSettingsPath())
	if err != nil || json.Unmarshal(b, &s) != nil {
		return s, false
	}
	return s, true
}

func writeAdminSettings(s adminConsoleSettings) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return utils.WriteFileAtomic(adminSettingsPath(), append(b, '\n'), 0644)
}

// adminSettings returns this node's settings, guessing and saving them on
// first use.
func adminSettings() adminConsoleSettings {
	if s, ok := readAdminSettings(); ok {
		return s
	}
	s := adminConsoleSettings{Address: adminGuessAddress()}
	s.Hostname = adminGuessHostname(s.Address)
	if err := writeAdminSettings(s); err != nil && SystemWideLogger != nil {
		SystemWideLogger.PrintAndLog("admin-rule", "Cannot save "+adminSettingsPath(), err)
	}
	return s
}

func (s adminConsoleSettings) validate() (adminConsoleSettings, error) {
	s.Address = strings.TrimSpace(s.Address)
	s.Hostname = hacluster.NormalizeHostname(s.Hostname)
	ip := net.ParseIP(s.Address)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return s, errors.New("address must be this node's IPv4 address")
	}
	s.Address = ip.To4().String()
	if !hacluster.ValidHostname(s.Hostname) {
		return s, errors.New("DNS name is not a valid host name")
	}
	return s, nil
}

// adminGuessAddress returns the IPv4 address of the default route, or the
// first non-loopback IPv4 address.
func adminGuessAddress() string {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	for _, ip := range localIPs() {
		if v4 := ip.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsLinkLocalUnicast() {
			return v4.String()
		}
	}
	return ""
}

// adminGuessHostname returns the reverse DNS name of ip, or this machine's
// host name if ip is one of its own addresses.
func adminGuessHostname(ip string) string {
	if net.ParseIP(ip) == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil {
		for _, n := range names {
			n = hacluster.NormalizeHostname(n)
			if hacluster.ValidHostname(n) && n != "localhost" {
				return n
			}
		}
	}
	if cfg := haConfig(); cfg.Enabled {
		for _, f := range cfg.FloatingIPs() {
			if f.Address == ip {
				return ""
			}
		}
	}
	for _, l := range localIPs() {
		if l.String() == ip {
			if h, err := os.Hostname(); err == nil {
				h = hacluster.NormalizeHostname(h)
				if hacluster.ValidHostname(h) && h != "localhost" {
					return h
				}
			}
		}
	}
	return ""
}

// adminRequiredHosts returns the hosts the rule must answer on, DNS names
// first.
func adminRequiredHosts(cfg *hacluster.Config, s adminConsoleSettings) []string {
	out, seen := []string{}, map[string]bool{}
	add := func(hs ...string) {
		for _, h := range hs {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" && !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	if cfg != nil && cfg.Enabled && len(cfg.Nodes) > 0 {
		for _, f := range cfg.FloatingIPs() {
			add(f.Hostname, f.Address)
		}
		for _, n := range cfg.Nodes {
			add(n.Hostname, n.Address)
		}
		return out
	}
	add(s.Hostname, s.Address)
	return out
}

// adminDefaultRoot returns the primary floating IP's DNS name or address
// with HA on, or this node's.
func adminDefaultRoot(cfg *hacluster.Config, s adminConsoleSettings) string {
	if cfg != nil && cfg.Enabled && len(cfg.Nodes) > 0 && cfg.VIP != "" {
		if cfg.VIPHostname != "" {
			return cfg.VIPHostname
		}
		return cfg.VIP
	}
	if s.Hostname != "" {
		return s.Hostname
	}
	if s.Address != "" {
		return s.Address
	}
	return ""
}

// adminDestination returns the admin console listener's address.
func adminDestination() string {
	host, port, err := net.SplitHostPort(*webUIPort)
	if err != nil {
		return "127.0.0.1:8000"
	}
	if ip := net.ParseIP(host); host == "" || host == "localhost" || (ip != nil && (ip.IsUnspecified() || ip.IsLoopback())) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func adminTLSAuto(t *tlscert.HostSpecificTlsBehavior) bool {
	if t == nil || !t.DisableSNI || len(t.PreferredCertificate) == 0 {
		return false
	}
	for _, c := range t.PreferredCertificate {
		if c != adminCertName {
			return false
		}
	}
	return true
}

// adminBuildRule returns the admin console rule with the editable settings
// of existing (nil for a new rule) and the fixed settings.
func adminBuildRule(existing *dynamicproxy.ProxyEndpoint, required []string, defaultRoot, destination string) *dynamicproxy.ProxyEndpoint {
	ep := dynamicproxy.GetDefaultProxyEndpoint()
	ep.System = adminRuleSystem
	ep.SystemHosts = append([]string{}, required...)
	isRequired := map[string]bool{}
	for _, h := range required {
		isRequired[h] = true
	}

	switch {
	case existing == nil:
		ep.RootOrMatchingDomain, ep.SystemAutoRoot = defaultRoot, true
	case existing.System == adminRuleSystem && existing.SystemAutoRoot:
		ep.RootOrMatchingDomain, ep.SystemAutoRoot = defaultRoot, true
	default:
		ep.RootOrMatchingDomain = strings.ToLower(existing.RootOrMatchingDomain)
	}
	if ep.RootOrMatchingDomain == "" && len(required) > 0 {
		ep.RootOrMatchingDomain = required[0]
	}
	root := ep.RootOrMatchingDomain

	// Aliases: existing ones minus no longer required system hosts, then all
	// required hosts
	wasSystem := map[string]bool{}
	if existing != nil && existing.System == adminRuleSystem {
		for _, h := range existing.SystemHosts {
			wasSystem[h] = true
		}
		if existing.SystemAutoRoot {
			wasSystem[strings.ToLower(existing.RootOrMatchingDomain)] = true
		}
	}
	aliases, seen := []string{}, map[string]bool{root: true}
	addAlias := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			aliases = append(aliases, h)
		}
	}
	if existing != nil {
		if old := strings.ToLower(existing.RootOrMatchingDomain); old != root && !(wasSystem[old] && !isRequired[old]) {
			addAlias(old)
		}
		for _, a := range existing.MatchingDomainAlias {
			if wasSystem[strings.ToLower(a)] && !isRequired[strings.ToLower(a)] {
				continue
			}
			addAlias(a)
		}
	}
	for _, h := range required {
		addAlias(h)
	}
	ep.MatchingDomainAlias = aliases

	ep.ActiveOrigins = []*loadbalance.Upstream{{
		OriginIpOrDomain:         destination,
		RequireTLS:               false,
		SkipWebSocketOriginCheck: true,
		Weight:                   1,
	}}

	ep.DisableUptimeMonitor = true
	hc := healthcheck.DefaultConfig()
	hc.ActiveEnabled, hc.PassiveEnabled = false, false
	ep.HealthCheck = hc
	ep.AssetCache = assetcache.DefaultConfig()
	ep.AssetCache.Enabled = true

	// Self-signed certificate for every host unless another is chosen
	allHosts := append([]string{root}, aliases...)
	if existing == nil || adminTLSAuto(existing.TlsOptions) {
		ep.TlsOptions = &tlscert.HostSpecificTlsBehavior{DisableSNI: true, PreferredCertificate: map[string]string{}}
		for _, h := range allHosts {
			ep.TlsOptions.PreferredCertificate[h] = adminCertName
		}
	} else if existing.TlsOptions != nil {
		t := *existing.TlsOptions
		t.PreferredCertificate = map[string]string{}
		for _, h := range allHosts {
			if c, ok := existing.TlsOptions.PreferredCertificate[h]; ok {
				t.PreferredCertificate[h] = c
			}
		}
		ep.TlsOptions = &t
	}

	if existing != nil {
		if existing.AccessFilterUUID != "" {
			ep.AccessFilterUUID = existing.AccessFilterUUID
		}
		if existing.Tags != nil {
			ep.Tags = append([]string{}, existing.Tags...)
		}
		ep.TrustProxyHeaders = existing.TrustProxyHeaders
		ep.DisableLogging = existing.DisableLogging
		ep.DisableStatisticCollection = existing.DisableStatisticCollection
		if existing.AssetCache != nil {
			c := *existing.AssetCache
			c.Extensions = append([]string{}, existing.AssetCache.Extensions...)
			ep.AssetCache = &c
		}
	}
	if ep.Tags == nil {
		ep.Tags = []string{}
	}
	return &ep
}

func adminAllRules() []*dynamicproxy.ProxyEndpoint {
	out := []*dynamicproxy.ProxyEndpoint{}
	if dynamicProxyRouter == nil || dynamicProxyRouter.ProxyEndpoints == nil {
		return out
	}
	dynamicProxyRouter.ProxyEndpoints.Range(func(_, v any) bool {
		if ep, ok := v.(*dynamicproxy.ProxyEndpoint); ok {
			out = append(out, ep)
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].RootOrMatchingDomain < out[j].RootOrMatchingDomain })
	return out
}

func adminRule() *dynamicproxy.ProxyEndpoint {
	for _, ep := range adminAllRules() {
		if ep.System == adminRuleSystem {
			return ep
		}
	}
	return nil
}

// adminIsRuleHost reports whether host is the admin console rule's main domain.
func adminIsRuleHost(host string) bool {
	ep := adminRule()
	return ep != nil && host != "" && strings.EqualFold(strings.TrimSpace(host), ep.RootOrMatchingDomain)
}

func adminEndpointJSON(ep *dynamicproxy.ProxyEndpoint) string {
	b, _ := json.Marshal(ep)
	return string(b)
}

// adminRuleReconcile creates or corrects the admin console rule (not on an
// HA standby).
func adminRuleReconcile() {
	adminMu.Lock()
	defer adminMu.Unlock()
	if dynamicProxyRouter == nil || haIsReadOnly() {
		return
	}
	cfg := haConfig()
	required := adminRequiredHosts(cfg, adminSettings())
	root := adminDefaultRoot(cfg, adminSettings())
	if len(required) == 0 || root == "" {
		SystemWideLogger.PrintAndLog("admin-rule", "No address for the admin console rule; set this node's address on the admin console rule", nil)
		return
	}
	existing, action := adminRule(), "updated"
	if existing == nil {
		action = "created"
	}
	want := adminBuildRule(existing, required, root, adminDestination())
	if accessController != nil && !accessController.ACLExists(want.AccessFilterUUID) {
		want.AccessFilterUUID = access.AllowAllID
	}
	if existing != nil && adminEndpointJSON(existing) == adminEndpointJSON(want) {
		adminCertSync(want)
		return
	}
	if err := adminApply(existing, want); err != nil {
		SystemWideLogger.PrintAndLog("admin-rule", "Cannot update the admin console rule", err)
		return
	}
	adminCertSync(want)
	SystemWideLogger.PrintAndLog("admin-rule", "Admin console rule "+action+": "+want.RootOrMatchingDomain+" (also "+strings.Join(want.MatchingDomainAlias, ", ")+") -> "+adminDestination(), nil)
}

// adminApply replaces old (nil for none) with want in the router and on disk.
func adminApply(old, want *dynamicproxy.ProxyEndpoint) error {
	if !validHostRuleName(want.RootOrMatchingDomain) {
		return fmt.Errorf("%q cannot be a rule's host name", want.RootOrMatchingDomain)
	}
	ready, err := dynamicProxyRouter.PrepareProxyRoute(want)
	if err != nil {
		return err
	}
	if err := SaveReverseProxyConfig(ready); err != nil {
		return err
	}
	if old != nil {
		old.Remove()
		if !strings.EqualFold(old.RootOrMatchingDomain, ready.RootOrMatchingDomain) {
			RemoveReverseProxyConfig(old.RootOrMatchingDomain)
		}
	}
	if err := dynamicProxyRouter.AddProxyRouteToRuntime(ready); err != nil {
		return err
	}
	if uptimeMonitor != nil {
		UpdateUptimeMonitorTargets()
	}
	return nil
}

// adminCertSync writes a new self-signed certificate when the rule uses it
// and it does not cover every host.
func adminCertSync(ep *dynamicproxy.ProxyEndpoint) {
	if ep == nil || !adminTLSAuto(ep.TlsOptions) {
		return
	}
	names := append([]string{ep.RootOrMatchingDomain}, ep.MatchingDomainAlias...)
	sorted := append([]string{}, names...)
	sort.Strings(sorted)
	have := tlscert.CertificateNames(filepath.Join(CONF_CERT_STORE, adminCertName+".pem"))
	if strings.Join(have, ",") == strings.Join(sorted, ",") {
		return
	}
	if err := tlscert.WriteSelfSignedCertificate(CONF_CERT_STORE, adminCertName, names, adminCertValidity); err != nil {
		SystemWideLogger.PrintAndLog("admin-rule", "Cannot write the admin console certificate", err)
		return
	}
	if tlsCertManager != nil {
		tlsCertManager.UpdateLoadedCertList()
	}
	SystemWideLogger.PrintAndLog("admin-rule", "Self-signed certificate "+adminCertName+" written for "+strings.Join(names, ", "), nil)
}

func initAdminRule() {
	adminRuleReconcile()
	go func() {
		for range time.Tick(time.Minute) {
			adminRuleReconcile()
		}
	}()
}

// adminRuleResetAndExit rebuilds the rule on disk and exits.
func adminRuleResetAndExit() {
	if _, err := readHAConfigFile(); err != nil {
		fmt.Println("Cannot read the HA settings: " + err.Error())
		os.Exit(1)
	}
	cfg := haConfig()
	s := adminSettings()
	required := adminRequiredHosts(cfg, s)
	root := adminDefaultRoot(cfg, s)
	if len(required) == 0 || root == "" {
		fmt.Println("Cannot reset the admin console rule: no address for this node. Set it in " + adminSettingsPath() + " ({\"address\": \"10.0.0.5\"}) and run this again.")
		os.Exit(1)
	}
	confs, _ := filepath.Glob(filepath.Join(CONF_HTTP_PROXY, "*.config"))
	for _, f := range confs {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		ep := dynamicproxy.GetDefaultProxyEndpoint()
		if json.Unmarshal(b, &ep) != nil {
			continue
		}
		if ep.System == adminRuleSystem {
			if err := os.Remove(f); err == nil {
				fmt.Println("Removed " + f)
			}
		}
	}
	want := adminBuildRule(nil, required, root, adminDestination())
	if err := SaveReverseProxyConfig(want); err != nil {
		fmt.Println("Cannot write the admin console rule: " + err.Error())
		os.Exit(1)
	}
	names := append([]string{want.RootOrMatchingDomain}, want.MatchingDomainAlias...)
	if err := tlscert.WriteSelfSignedCertificate(CONF_CERT_STORE, adminCertName, names, adminCertValidity); err != nil {
		fmt.Println("Cannot write the self-signed certificate: " + err.Error())
		os.Exit(1)
	}
	fmt.Println("Admin console rule reset: " + want.RootOrMatchingDomain + " (also " + strings.Join(want.MatchingDomainAlias, ", ") + ") -> " + adminDestination())
	fmt.Println("It uses a new self-signed certificate and the Allow all ACL. Restart Waymark to load it (sudo systemctl restart waymark).")
	if cfg.Enabled && cfg.SyncEnabled {
		fmt.Println("With HA configuration sync on, do this on the active node: a standby copies the active node's rule.")
	}
	os.Exit(0)
}

// HandleAdminRule: GET describes the admin console rule; POST sets this
// node's address and DNS name.
func HandleAdminRule(w http.ResponseWriter, r *http.Request) {
	cfg := haConfig()
	haOn := cfg.Enabled && len(cfg.Nodes) > 0
	if r.Method == http.MethodPost {
		if haOn {
			utils.SendErrorResponse(w, "with HA on, the addresses and DNS names are set on System > High Availability")
			return
		}
		addr, _ := utils.PostPara(r, "address")
		host, _ := utils.PostPara(r, "hostname")
		s, err := adminConsoleSettings{Address: addr, Hostname: host}.validate()
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
		if msg := adminHostsTakenByOtherRules([]string{s.Address, s.Hostname}); msg != "" {
			utils.SendErrorResponse(w, msg)
			return
		}
		if err := writeAdminSettings(s); err != nil {
			utils.SendErrorResponse(w, "could not save: "+err.Error())
			return
		}
		adminRuleReconcile()
		utils.SendOK(w)
		return
	}
	s := adminSettings()
	out := map[string]any{
		"haEnabled":     haOn,
		"settings":      s,
		"requiredHosts": adminRequiredHosts(cfg, s),
		"defaultRoot":   adminDefaultRoot(cfg, s),
		"destination":   adminDestination(),
		"certName":      adminCertName,
	}
	if ep := adminRule(); ep != nil {
		out["root"] = ep.RootOrMatchingDomain
		out["autoRoot"] = ep.SystemAutoRoot
		out["autoCert"] = adminTLSAuto(ep.TlsOptions)
	}
	if r.URL.Query().Get("guess") == "1" {
		a := adminGuessAddress()
		out["guess"] = adminConsoleSettings{Address: a, Hostname: adminGuessHostname(a)}
	}
	js, _ := json.Marshal(out)
	utils.SendJSONResponse(w, string(js))
}

// HandleDNSGuess returns the reverse DNS name of an IPv4 address.
func HandleDNSGuess(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if p := net.ParseIP(ip); p == nil || p.To4() == nil {
		utils.SendErrorResponse(w, "not an IPv4 address")
		return
	}
	js, _ := json.Marshal(map[string]string{"hostname": adminGuessHostname(ip)})
	utils.SendJSONResponse(w, string(js))
}

func adminConsolePort() string {
	if _, port, err := net.SplitHostPort(*webUIPort); err == nil {
		return port
	}
	return "8000"
}
