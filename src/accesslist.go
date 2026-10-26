package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/microcosm-cc/bluemonday"

	"imuslab.com/zoraxy/mod/utils"
)

/*
	accesslist.go

	Trusted proxies and the list of busy client addresses (Access).
	Custom fork: ACLs and objects are in acl_api_custom.go.
*/

/*
	Trusted Proxy Management
*/

// List all trusted proxies
func handleListTrustedProxies(w http.ResponseWriter, r *http.Request) {
	proxies := accessController.ListTrustedProxies()
	js, _ := json.Marshal(proxies)
	utils.SendJSONResponse(w, string(js))
}

// Add a new trusted proxy
func handleAddTrustedProxy(w http.ResponseWriter, r *http.Request) {
	ip, err := utils.PostPara(r, "ip")
	if err != nil {
		utils.SendErrorResponse(w, "invalid or empty ip address")
		return
	}

	desc, _ := utils.PostPara(r, "desc")

	//Sanitize inputs
	p := bluemonday.StrictPolicy()
	ip = strings.TrimSpace(ip)
	desc = p.Sanitize(desc)

	//Add the proxy
	added := accessController.AddTrustedProxy(ip, desc)
	if !added {
		utils.SendErrorResponse(w, "proxy already exists")
		return
	}

	//Save to file
	err = accessController.SaveTrustedProxies()
	if err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

// Remove a trusted proxy
func handleRemoveTrustedProxy(w http.ResponseWriter, r *http.Request) {
	ip, err := utils.PostPara(r, "ip")
	if err != nil {
		utils.SendErrorResponse(w, "invalid or empty ip address")
		return
	}

	ip = strings.TrimSpace(ip)

	//Remove the proxy
	removed := accessController.RemoveTrustedProxy(ip)
	if !removed {
		utils.SendErrorResponse(w, "proxy not found")
		return
	}

	//Save to file
	err = accessController.SaveTrustedProxies()
	if err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

// Update a trusted proxy (change IP or description)
func handleUpdateTrustedProxy(w http.ResponseWriter, r *http.Request) {
	originalIP, err := utils.PostPara(r, "original_ip")
	if err != nil {
		utils.SendErrorResponse(w, "original ip is required")
		return
	}

	newIP, err := utils.PostPara(r, "new_ip")
	if err != nil {
		utils.SendErrorResponse(w, "new ip is required")
		return
	}

	desc, _ := utils.PostPara(r, "desc")

	//Sanitize inputs
	p := bluemonday.StrictPolicy()
	originalIP = strings.TrimSpace(originalIP)
	newIP = strings.TrimSpace(newIP)
	desc = p.Sanitize(desc)

	//If IP changed, check if new IP already exists as an entry
	if originalIP != newIP {
		if accessController.TrustedProxyExists(newIP) {
			utils.SendErrorResponse(w, "new ip already exists as trusted proxy")
			return
		}
	}

	//Remove old entry
	removed := accessController.RemoveTrustedProxy(originalIP)
	if !removed {
		utils.SendErrorResponse(w, "original proxy not found")
		return
	}

	//Add new entry
	accessController.AddTrustedProxy(newIP, desc)

	//Save to file
	err = accessController.SaveTrustedProxies()
	if err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

// Bulk-replace the entire trusted proxy list from a CSV body.
// Each non-empty line must be:  ip/cidr, description
// Lines starting with # are treated as comments and skipped.
func handleBulkUpdateTrustedProxies(w http.ResponseWriter, r *http.Request) {
	csv, err := utils.PostPara(r, "csv")
	if err != nil {
		utils.SendErrorResponse(w, "csv parameter is required")
		return
	}

	p := bluemonday.StrictPolicy()

	// Parse the incoming CSV into (ip, desc) pairs before mutating state
	type entry struct {
		ip   string
		desc string
	}
	var entries []entry
	for _, rawLine := range strings.Split(csv, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		ip := strings.TrimSpace(parts[0])
		if ip == "" {
			continue
		}
		desc := ""
		if len(parts) == 2 {
			desc = p.Sanitize(strings.TrimSpace(parts[1]))
		}
		entries = append(entries, entry{ip: ip, desc: desc})
	}

	// Remove every existing entry
	for _, proxy := range accessController.ListTrustedProxies() {
		accessController.RemoveTrustedProxy(proxy.IP)
	}

	// Add the new entries in order
	for _, e := range entries {
		accessController.AddTrustedProxy(e.ip, e.desc)
	}

	// Persist
	if err := accessController.SaveTrustedProxies(); err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

// Reset the trusted proxy list to the built-in defaults and return the new list.
func handleResetDefaultTrustedProxies(w http.ResponseWriter, r *http.Request) {
	// Remove all current entries
	for _, proxy := range accessController.ListTrustedProxies() {
		accessController.RemoveTrustedProxy(proxy.IP)
	}

	// Load defaults
	for _, proxy := range accessController.GetDefaultTrustedProxies() {
		accessController.AddTrustedProxy(proxy.IP, proxy.Desc)
	}

	// Persist
	if err := accessController.SaveTrustedProxies(); err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	// Return the new list so the UI can refresh without an extra round-trip
	result := accessController.ListTrustedProxies()
	js, _ := json.Marshal(result)
	utils.SendJSONResponse(w, string(js))
}

// List all quick ban ip address
func handleListQuickBan(w http.ResponseWriter, r *http.Request) {
	currentSummary := statisticCollector.GetCurrentDailySummary()
	type quickBanEntry struct {
		IpAddr      string
		Count       int
		CountryCode string
	}
	result := []quickBanEntry{}
	currentSummary.RequestClientIp.Range(func(key, value interface{}) bool {
		ip := key.(string)
		count := value.(int)
		thisEntry := quickBanEntry{
			IpAddr: ip,
			Count:  count,
		}

		//Get the country code
		geoinfo, err := geodbStore.ResolveCountryCodeFromIP(ip)
		if err == nil {
			thisEntry.CountryCode = geoinfo.CountryIsoCode
		}

		result = append(result, thisEntry)
		return true
	})

	//Sort result based on count
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	js, _ := json.Marshal(result)
	utils.SendJSONResponse(w, string(js))
}
