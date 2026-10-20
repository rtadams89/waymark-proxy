package main

/*
	admin_guard_custom.go (custom fork): keeps the admin console reachable.

	Wraps the admin UI's API (inside CSRF and the HA read-only check):

	- The admin console rule (admin_rule_custom.go) cannot be disabled or
	  deleted, and its destinations, virtual directories, headers,
	  authentication, health checks, maintenance mode, listening address and
	  extra listening ports cannot be changed. Its required hosts cannot be
	  removed. Its main domain, extra aliases, certificate, access rule,
	  caching, logging and tags can be changed; after every change the
	  fixed settings are put back (adminRuleReconcile).
	- No other rule can take one of its hosts.
	- When the request came through Waymark's proxy, an access rule change
	  that would block the requester from the admin console rule is refused
	  (and undone if it was made).
*/

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/netutils"
)

// Changes to these are refused for the admin console rule.
var adminLockedPaths = map[string]string{
	"/api/proxy/del":                             "deleted",
	"/api/proxy/updateCredentials":               "given basic authentication",
	"/api/proxy/listeningPorts/set":              "given extra listening ports",
	"/api/proxy/upstream/add":                    "given other destinations",
	"/api/proxy/upstream/update":                 "given other destinations",
	"/api/proxy/upstream/remove":                 "given other destinations",
	"/api/proxy/healthcheck/set":                 "given health checks",
	"/api/proxy/maintenance/set":                 "put in maintenance mode",
	"/api/proxy/listenip/set":                    "limited to one listening address",
	"/api/proxy/vdir/add":                        "given virtual directories",
	"/api/proxy/vdir/del":                        "given virtual directories",
	"/api/proxy/vdir/edit":                       "given virtual directories",
	"/api/proxy/header/add":                      "given custom headers",
	"/api/proxy/header/remove":                   "given custom headers",
	"/api/proxy/auth/exceptions/add":             "given authentication exceptions",
	"/api/proxy/auth/exceptions/delete":          "given authentication exceptions",
	"/api/proxy/auth/zorxauth/exceptions/add":    "given authentication exceptions",
	"/api/proxy/auth/zorxauth/exceptions/delete": "given authentication exceptions",
}

const adminLockedSuffix = ": it serves this admin console, and Waymark keeps it working. You can change its main domain, extra aliases, certificate, access rule, caching, logging and tags."

func adminGuardWrap(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler { return mw(adminGuard(h)) }
}

func adminGuardRefuse(w http.ResponseWriter, msg string) {
	js, _ := json.Marshal(map[string]string{"error": msg})
	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

// adminRuleHosts are every host the admin console rule answers on.
func adminRuleHosts(ep *dynamicproxy.ProxyEndpoint) map[string]bool {
	out := map[string]bool{}
	if ep == nil {
		return out
	}
	out[strings.ToLower(ep.RootOrMatchingDomain)] = true
	for _, a := range ep.MatchingDomainAlias {
		out[strings.ToLower(a)] = true
	}
	for _, h := range ep.SystemHosts {
		out[strings.ToLower(h)] = true
	}
	return out
}

// adminHostsTakenByOtherRules returns why one of hosts cannot be used by
// the admin console rule (another rule has it), or "".
func adminHostsTakenByOtherRules(hosts []string) string {
	sys := adminRule()
	for _, ep := range adminAllRules() {
		if ep == sys {
			continue
		}
		names := append([]string{ep.RootOrMatchingDomain}, ep.MatchingDomainAlias...)
		for _, n := range names {
			for _, h := range hosts {
				if h != "" && strings.EqualFold(n, h) {
					return h + " is already used by the proxy rule " + ep.RootOrMatchingDomain + "; remove it there first"
				}
			}
		}
	}
	return ""
}

// adminProxiedClientIP is the requester's address when the request came
// through Waymark's own proxy (the admin console rule), or "".
func adminProxiedClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return ""
	}
	if r.Header.Get("X-Real-Ip") == "" && r.Header.Get("X-Forwarded-For") == "" {
		return ""
	}
	ip := netutils.GetRequesterIP(r)
	if p := net.ParseIP(ip); p == nil || p.IsLoopback() {
		return ""
	}
	return ip
}

// adminAccessRule is the access rule protecting the admin console rule.
func adminAccessRule(ep *dynamicproxy.ProxyEndpoint) *access.AccessRule {
	if ep == nil || accessController == nil {
		return nil
	}
	id := ep.AccessFilterUUID
	if id == "" {
		id = "default"
	}
	rule, err := accessController.GetAccessRuleByID(id)
	if err != nil {
		return nil
	}
	return rule
}

func adminBlockedMessage(ip string) string {
	return "This would block your own address (" + ip + ") from the admin console: its proxy rule uses this access rule. Allow " + ip + " first, or make the change from a computer the rule still allows."
}

type accessSnapshot struct {
	blacklist, whitelist, allowLocal, trustProxy bool
	wlCC, wlIP, blCC, blIP                       map[string]string
}

func copyMap(m *map[string]string) map[string]string {
	out := map[string]string{}
	if m != nil {
		for k, v := range *m {
			out[k] = v
		}
	}
	return out
}

func snapshotAccess(a *access.AccessRule) accessSnapshot {
	return accessSnapshot{a.BlacklistEnabled, a.WhitelistEnabled, a.WhitelistAllowLocalAndLoopback, a.TrustProxyHeadersOnly,
		copyMap(a.WhiteListCountryCode), copyMap(a.WhiteListIP), copyMap(a.BlackListContryCode), copyMap(a.BlackListIP)}
}

func (s accessSnapshot) restore(a *access.AccessRule) {
	a.BlacklistEnabled, a.WhitelistEnabled, a.WhitelistAllowLocalAndLoopback, a.TrustProxyHeadersOnly = s.blacklist, s.whitelist, s.allowLocal, s.trustProxy
	wlCC, wlIP, blCC, blIP := s.wlCC, s.wlIP, s.blCC, s.blIP
	a.WhiteListCountryCode, a.WhiteListIP, a.BlackListContryCode, a.BlackListIP = &wlCC, &wlIP, &blCC, &blIP
	a.SaveChanges()
}

// bufferedResponse holds a handler's response until it is known to stand.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }
func (b *bufferedResponse) flush(w http.ResponseWriter) {
	for k, v := range b.header {
		w.Header()[k] = v
	}
	if b.status != 0 {
		w.WriteHeader(b.status)
	}
	w.Write(b.body.Bytes())
}

func adminGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		sys := adminRule()
		if sys == nil || !strings.HasPrefix(p, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		r.ParseForm()
		isSys := false
		for _, k := range []string{"ep", "rootname", "domain", "oldHostname", "host"} {
			if adminIsRuleHost(r.Form.Get(k)) {
				isSys = true
			}
		}
		write := r.Method != http.MethodGet && r.Method != http.MethodHead
		root := sys.RootOrMatchingDomain

		if isSys {
			if what, ok := adminLockedPaths[p]; ok {
				adminGuardRefuse(w, "The rule "+root+" cannot be "+what+adminLockedSuffix)
				return
			}
			if write && strings.HasPrefix(p, "/api/proxy/header/handle") {
				adminGuardRefuse(w, "The headers of "+root+" cannot be changed"+adminLockedSuffix)
				return
			}
			if p == "/api/proxy/toggle" && r.Form.Get("enable") == "false" {
				adminGuardRefuse(w, "The rule "+root+" cannot be disabled"+adminLockedSuffix)
				return
			}
			if p == "/api/proxy/setAlias" {
				var aliases []string
				json.Unmarshal([]byte(r.Form.Get("alias")), &aliases)
				have := map[string]bool{}
				for _, a := range aliases {
					have[strings.ToLower(strings.TrimSpace(a))] = true
				}
				for _, h := range sys.SystemHosts {
					if !have[h] && !strings.EqualFold(h, root) {
						adminGuardRefuse(w, h+" cannot be removed from "+root+": it is an address or DNS name of this "+adminHostSource()+". Change it on "+adminHostPage()+".")
						return
					}
				}
				if msg := adminHostsTakenByOtherRules(aliases); msg != "" {
					adminGuardRefuse(w, msg)
					return
				}
			}
			if p == "/api/proxy/setHostname" {
				if msg := adminHostsTakenByOtherRules([]string{r.Form.Get("newHostname")}); msg != "" {
					adminGuardRefuse(w, msg)
					return
				}
			}
			if p == "/api/access/attach" {
				if ip := adminProxiedClientIP(r); ip != "" && accessController != nil {
					if rule, err := accessController.GetAccessRuleByID(r.Form.Get("id")); err == nil && !rule.AllowIpAccess(ip) {
						adminGuardRefuse(w, adminBlockedMessage(ip))
						return
					}
				}
			}
		} else if write {
			// Another rule taking one of the admin console rule's hosts
			taken := adminRuleHosts(sys)
			var claimed []string
			switch p {
			case "/api/proxy/add":
				for _, h := range strings.Split(r.Form.Get("rootname"), ",") {
					claimed = append(claimed, h)
				}
			case "/api/proxy/setAlias":
				json.Unmarshal([]byte(r.Form.Get("alias")), &claimed)
			case "/api/proxy/setHostname":
				claimed = []string{r.Form.Get("newHostname")}
			}
			for _, h := range claimed {
				if taken[strings.ToLower(strings.TrimSpace(h))] {
					adminGuardRefuse(w, strings.TrimSpace(h)+" is used by the admin console rule "+root+", so another rule cannot have it")
					return
				}
			}
		}

		// Access rule changes that could lock out the requester
		if write && (strings.HasPrefix(p, "/api/blacklist/") || strings.HasPrefix(p, "/api/whitelist/") || p == "/api/access/remove") {
			if ip := adminProxiedClientIP(r); ip != "" {
				if rule := adminAccessRule(sys); rule != nil && strings.EqualFold(r.Form.Get("id"), rule.ID) {
					if p == "/api/access/remove" {
						adminGuardRefuse(w, "This access rule protects the admin console rule "+root+". Choose another access rule for it first.")
						return
					}
					snap := snapshotAccess(rule)
					buf := &bufferedResponse{header: http.Header{}}
					next.ServeHTTP(buf, r)
					if !rule.AllowIpAccess(ip) {
						snap.restore(rule)
						adminGuardRefuse(w, adminBlockedMessage(ip))
						return
					}
					buf.flush(w)
					return
				}
			}
		}

		next.ServeHTTP(w, r)

		if isSys && write {
			if p == "/api/proxy/setHostname" {
				if ep := adminRule(); ep != nil && strings.EqualFold(ep.RootOrMatchingDomain, r.Form.Get("newHostname")) {
					ep.SystemAutoRoot = false // the main domain is now the user's choice
				}
			}
			adminRuleReconcile() // put back anything fixed the change touched
		}
	})
}

func adminHostSource() string {
	if c := haConfig(); c.Enabled && len(c.Nodes) > 0 {
		return "cluster"
	}
	return "node"
}

func adminHostPage() string {
	if adminHostSource() == "cluster" {
		return "System > High Availability"
	}
	return "the Admin console section of this rule's Host page"
}
