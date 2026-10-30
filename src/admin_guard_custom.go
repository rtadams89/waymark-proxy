package main

/*
	API checks for the admin console rule: its fixed settings and required
	hosts cannot be changed, no other rule can take its hosts, and ACL
	changes that would deny the requester on it are refused.
*/

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/netutils"
)

// API paths refused for the admin console rule, with the action named in the error.
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

const adminLockedSuffix = ": it serves this admin console, and Waymark keeps it working. You can change its main domain, extra aliases, certificate, ACL, caching, logging and tags."

func adminGuardWrap(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler { return mw(adminGuard(h)) }
}

func adminGuardRefuse(w http.ResponseWriter, msg string) {
	js, _ := json.Marshal(map[string]string{"error": msg})
	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

// adminRuleHosts returns every host the admin console rule answers on.
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

// adminHostsTakenByOtherRules returns an error message if another rule has
// one of hosts, or "".
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

// adminProxiedClientIP returns the requester's address when the request came
// through the admin console rule, or "".
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

func adminBlockedMessage(ip string) string {
	return "This would deny your own address (" + ip + ") on the admin console: its proxy rule uses this ACL. Allow " + ip + " first, or make the change from a computer the ACL still allows."
}

// adminACLChangeBlocks returns an error message if the change to acl or obj
// would deny the requester on the admin console rule, or "".
func adminACLChangeBlocks(r *http.Request, acl *access.ACL, obj *access.Object) string {
	ip := adminProxiedClientIP(r)
	sys := adminRule()
	if ip == "" || sys == nil || accessController == nil {
		return ""
	}
	id := accessController.Resolve(sys.AccessFilterUUID)
	if acl != nil && acl.ID != id {
		return ""
	}
	cand := acl
	if cand == nil {
		var err error
		if cand, err = accessController.GetACL(id); err != nil {
			return ""
		}
	}
	var objs map[string]*access.Object
	if obj != nil {
		objs = map[string]*access.Object{obj.ID: obj}
	}
	if d := accessController.TestWith(cand, objs, ip); d.Action != access.ActionAllow {
		return adminBlockedMessage(ip)
	}
	return ""
}

// adminACLAttachBlocks returns an error message if attaching ACL id to the
// admin console rule would deny the requester, or "".
func adminACLAttachBlocks(r *http.Request, id string) string {
	ip := adminProxiedClientIP(r)
	if ip == "" || accessController == nil {
		return ""
	}
	if d := accessController.Test(id, ip); d.Action != access.ActionAllow {
		return adminBlockedMessage(ip)
	}
	return ""
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
		} else if write {
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

		next.ServeHTTP(w, r)

		if isSys && write {
			if p == "/api/proxy/setHostname" {
				if ep := adminRule(); ep != nil && strings.EqualFold(ep.RootOrMatchingDomain, r.Form.Get("newHostname")) {
					ep.SystemAutoRoot = false
				}
			}
			adminRuleReconcile()
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
