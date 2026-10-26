package main

/*
	acl_api_custom.go (custom fork): the API of ACLs and objects
	(mod/access), and attaching ACLs to proxy rules.

	/api/acl/list        GET  every ACL with its rule count and users
	/api/acl/get         GET  one ACL with hit counts, users and warnings
	/api/acl/create      POST name, description
	/api/acl/duplicate   POST id, name
	/api/acl/save        POST acl (JSON: id, revision, name, description, rules)
	/api/acl/delete      POST id (only when no proxy rule uses it)
	/api/acl/test        GET  id, ip: which rule decides for that address
	/api/acl/attach      POST id, host[, trust]: the ACL of an HTTP proxy rule
	                     (and whether it trusts addresses forwarded by trusted proxies)
	/api/acl/objects     GET  every object with the ACLs using it
	/api/acl/object/save POST object (JSON: id (empty for a new one), revision, name, description, entries)
	/api/acl/object/delete POST id

	Saving an ACL or object, or attaching an ACL, is refused when the
	request came through the admin console rule and the change would deny
	the requester there (admin_guard_custom.go).
*/

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/utils"
)

type aclUser struct {
	Type string `json:"type"` // http, l4 or default (the Default Site)
	Name string `json:"name"`
}

// aclUsers lists the proxy rules using each ACL (by resolved ACL ID).
func aclUsers() map[string][]aclUser {
	out := map[string][]aclUser{}
	if accessController == nil {
		return out
	}
	if dynamicProxyRouter != nil {
		for _, ep := range adminAllRules() {
			id := accessController.Resolve(ep.AccessFilterUUID)
			out[id] = append(out[id], aclUser{Type: "http", Name: ep.RootOrMatchingDomain})
		}
		if root := dynamicProxyRouter.Root; root != nil {
			id := accessController.Resolve(root.AccessFilterUUID)
			out[id] = append(out[id], aclUser{Type: "default", Name: "Default Site"})
		}
	}
	if l4ProxyManager != nil {
		for _, a := range accessController.ListACLs() {
			for _, n := range l4ProxyManager.ACLUsers(a.ID) {
				out[a.ID] = append(out[a.ID], aclUser{Type: "l4", Name: n})
			}
		}
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool { return out[id][i].Name < out[id][j].Name })
	}
	return out
}

func sendJSON(w http.ResponseWriter, v any) {
	js, _ := json.Marshal(v)
	utils.SendJSONResponse(w, string(js))
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "method not allowed")
		return false
	}
	return true
}

func HandleACLList(w http.ResponseWriter, r *http.Request) {
	users := aclUsers()
	type row struct {
		ID            string    `json:"id"`
		Name          string    `json:"name"`
		Description   string    `json:"description"`
		BuiltIn       bool      `json:"builtIn"`
		Rules         int       `json:"rules"`
		Revision      int64     `json:"revision"`
		Users         []aclUser `json:"users"`
		UsesCountries bool      `json:"usesCountries"`
	}
	countries := map[string]bool{}
	for _, n := range accessController.ACLsUsingCountries() {
		countries[n] = true
	}
	out := []row{}
	for _, a := range accessController.ListACLs() {
		u := users[a.ID]
		if u == nil {
			u = []aclUser{}
		}
		out = append(out, row{a.ID, a.Name, a.Description, a.BuiltIn, len(a.Rules), a.Revision, u, countries[a.Name]})
	}
	sendJSON(w, out)
}

// aclWarnings explains rules that may not do what they look like.
func aclWarnings(a *access.ACL) map[string]string {
	out := map[string]string{}
	usesCountry := func(s access.Source) bool {
		return s.Type == access.SourceCountry ||
			(s.Type == access.SourceObject && accessController.ObjectUsesCountries(s.Value))
	}
	geo := geodbStore != nil && geodbStore.Enabled()
	privateAbove := false
	for _, r := range a.Rules {
		if !r.Enabled {
			continue
		}
		if usesCountry(r.Source) {
			switch {
			case !geo:
				out[r.ID] = "GeoIP lookups are off (Utilities, System tab), so a country cannot be known and this rule is skipped."
			case r.Source.Not && r.Action != access.ActionAllow && !privateAbove:
				out[r.ID] = "Private and LAN addresses have no country, so \"not\" a country matches them too. Add \"allow Private networks\" above this rule to keep them out of it."
			}
		}
		if r.Source.Type == access.SourceObject && r.Source.Value == access.PrivateNetworksID && !r.Source.Not && r.Action == access.ActionAllow {
			privateAbove = true
		}
	}
	return out
}

func HandleACLGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	a, err := accessController.GetACL(id)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	u := aclUsers()[a.ID]
	if u == nil {
		u = []aclUser{}
	}
	sendJSON(w, map[string]any{
		"acl":      a,
		"hits":     accessController.Hits(a.ID),
		"users":    u,
		"warnings": aclWarnings(a),
		"geoip":    geodbStore != nil && geodbStore.Enabled(),
	})
}

func HandleACLCreate(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	name, _ := utils.PostPara(r, "name")
	desc, _ := utils.PostPara(r, "description")
	a, err := accessController.CreateACL(name, desc)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	SystemWideLogger.PrintAndLog("access", "ACL "+a.Name+" created", nil)
	sendJSON(w, a)
}

func HandleACLDuplicate(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, _ := utils.PostPara(r, "id")
	name, _ := utils.PostPara(r, "name")
	src, err := accessController.GetACL(id)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	a, err := accessController.CreateACL(name, src.Description)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	a.Rules = src.Rules
	for i := range a.Rules {
		a.Rules[i].ID = ""
	}
	p, err := accessController.PrepareACL(a)
	if err == nil {
		err = accessController.SaveACL(p)
	}
	if err != nil {
		accessController.DeleteACL(a.ID)
		utils.SendErrorResponse(w, err.Error())
		return
	}
	sendJSON(w, p)
}

func HandleACLSave(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	raw, err := utils.PostPara(r, "acl")
	if err != nil {
		utils.SendErrorResponse(w, "no ACL given")
		return
	}
	in := &access.ACL{}
	if err := json.Unmarshal([]byte(raw), in); err != nil {
		utils.SendErrorResponse(w, "the ACL is not valid JSON")
		return
	}
	p, err := accessController.PrepareACL(in)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	if msg := adminACLChangeBlocks(r, p, nil); msg != "" {
		utils.SendErrorResponse(w, msg)
		return
	}
	if err := accessController.SaveACL(p); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	sendJSON(w, p)
}

func HandleACLDelete(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, _ := utils.PostPara(r, "id")
	if id == access.AllowAllID {
		utils.SendErrorResponse(w, "the Allow all ACL cannot be deleted")
		return
	}
	if users := aclUsers()[id]; len(users) > 0 {
		names := []string{}
		for _, u := range users {
			names = append(names, u.Name)
		}
		utils.SendErrorResponse(w, "this ACL is used by "+strings.Join(names, ", ")+"; give them another ACL first")
		return
	}
	a, err := accessController.GetACL(id)
	if err == nil {
		err = accessController.DeleteACL(id)
	}
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	SystemWideLogger.PrintAndLog("access", "ACL "+a.Name+" deleted", nil)
	utils.SendOK(w)
}

func HandleACLTest(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if !accessController.ACLExists(id) {
		utils.SendErrorResponse(w, "ACL not found")
		return
	}
	if ip == "" {
		ip = adminProxiedClientIP(r)
		if ip == "" {
			ip = clientAddress(r)
		}
	}
	if _, err := access.NormalizeValue(access.SourceIP, ip); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	d := accessController.Test(id, ip)
	sendJSON(w, map[string]any{"ip": ip, "decision": d})
}

// clientAddress is the address the request came from (no headers).
func clientAddress(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = strings.Trim(host[:i], "[]")
	}
	return host
}

func HandleACLAttach(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, _ := utils.PostPara(r, "id")
	host, _ := utils.PostPara(r, "host")
	if !accessController.ACLExists(id) {
		utils.SendErrorResponse(w, "ACL not found")
		return
	}
	var ep *dynamicproxy.ProxyEndpoint
	if host == "/" && dynamicProxyRouter.Root != nil {
		ep = dynamicProxyRouter.Root
	} else {
		var err error
		if ep, err = dynamicProxyRouter.LoadProxy(host); err != nil {
			utils.SendErrorResponse(w, "proxy rule not found")
			return
		}
	}
	if ep.System == adminRuleSystem {
		if msg := adminACLAttachBlocks(r, id); msg != "" {
			utils.SendErrorResponse(w, msg)
			return
		}
	}
	ep.AccessFilterUUID = id
	if _, err := utils.PostPara(r, "trust"); err == nil {
		ep.TrustProxyHeaders, _ = utils.PostBool(r, "trust")
	}
	if ep.ProxyType != dynamicproxy.ProxyTypeRoot {
		ep.UpdateToRuntime()
	}
	if err := SaveReverseProxyConfig(ep); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendOK(w)
}

/* Objects */

func HandleACLObjects(w http.ResponseWriter, r *http.Request) {
	type row struct {
		*access.Object
		Users   []string `json:"users"`
		Parents []string `json:"parents"` // objects this one is inside
	}
	out := []row{}
	for _, o := range accessController.ListObjects() {
		out = append(out, row{o, accessController.ObjectUsers(o.ID), accessController.ObjectParents(o.ID)})
	}
	sendJSON(w, map[string]any{"objects": out, "geoip": geodbStore != nil && geodbStore.Enabled()})
}

func HandleACLObjectSave(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	raw, err := utils.PostPara(r, "object")
	if err != nil {
		utils.SendErrorResponse(w, "no object given")
		return
	}
	in := &access.Object{}
	if err := json.Unmarshal([]byte(raw), in); err != nil {
		utils.SendErrorResponse(w, "the object is not valid JSON")
		return
	}
	o, err := accessController.PrepareObject(in)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	if in.ID != "" {
		if msg := adminACLChangeBlocks(r, nil, o); msg != "" {
			utils.SendErrorResponse(w, msg)
			return
		}
	}
	if err := accessController.SaveObject(o); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	sendJSON(w, o)
}

func HandleACLObjectDelete(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, _ := utils.PostPara(r, "id")
	if err := accessController.DeleteObject(id); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendOK(w)
}

/* Conversion of the access rules of 2.17 and earlier */

// applyACLConversion points proxy rules at the ACLs their access rules
// became, and gives HTTP rules whose access rule trusted forwarded
// addresses that option themselves. Run once the proxy rules are loaded.
func applyACLConversion() {
	if accessController == nil {
		return
	}
	conv := accessController.Converted()
	if conv == nil {
		return
	}
	changed := 0
	eps := adminAllRules()
	if dynamicProxyRouter != nil && dynamicProxyRouter.Root != nil {
		eps = append(eps, dynamicProxyRouter.Root)
	}
	for _, ep := range eps {
		old := ep.AccessFilterUUID
		if old == "" {
			old = access.LegacyDefaultID
		}
		dirty := false
		if conv.Trust[old] && !ep.TrustProxyHeaders {
			ep.TrustProxyHeaders, dirty = true, true
		}
		if n, ok := conv.IDMap[old]; ok && n != ep.AccessFilterUUID {
			ep.AccessFilterUUID, dirty = n, true
		}
		if dirty {
			if err := SaveReverseProxyConfig(ep); err != nil {
				SystemWideLogger.PrintAndLog("access", "Cannot save proxy rule "+ep.RootOrMatchingDomain+" after converting access rules", err)
				continue
			}
			changed++
		}
	}
	if l4ProxyManager != nil {
		changed += l4ProxyManager.RemapACLs(conv.IDMap)
	}
	if changed > 0 {
		SystemWideLogger.PrintAndLog("access", "Updated the ACL of "+itoa(changed)+" proxy rule(s) after converting access rules", nil)
	}
}
