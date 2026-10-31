package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"imuslab.com/zoraxy/mod/utils"
)

// Trusted proxies and the list of busy client addresses.

/*
	Trusted Proxy Management
*/

func handleListTrustedProxies(w http.ResponseWriter, r *http.Request) {
	proxies := accessController.ListTrustedProxies()
	js, _ := json.Marshal(proxies)
	utils.SendJSONResponse(w, string(js))
}

func handleAddTrustedProxy(w http.ResponseWriter, r *http.Request) {
	ip, err := utils.PostPara(r, "ip")
	if err != nil {
		utils.SendErrorResponse(w, "invalid or empty ip address")
		return
	}

	desc, _ := utils.PostPara(r, "desc")

	ip = strings.TrimSpace(ip)
	desc = cleanTrustedProxyDesc(desc)

	added := accessController.AddTrustedProxy(ip, desc)
	if !added {
		utils.SendErrorResponse(w, "proxy already exists")
		return
	}

	err = accessController.SaveTrustedProxies()
	if err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

func handleRemoveTrustedProxy(w http.ResponseWriter, r *http.Request) {
	ip, err := utils.PostPara(r, "ip")
	if err != nil {
		utils.SendErrorResponse(w, "invalid or empty ip address")
		return
	}

	ip = strings.TrimSpace(ip)

	removed := accessController.RemoveTrustedProxy(ip)
	if !removed {
		utils.SendErrorResponse(w, "proxy not found")
		return
	}

	err = accessController.SaveTrustedProxies()
	if err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

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

	originalIP = strings.TrimSpace(originalIP)
	newIP = strings.TrimSpace(newIP)
	desc = cleanTrustedProxyDesc(desc)

	if originalIP != newIP {
		if accessController.TrustedProxyExists(newIP) {
			utils.SendErrorResponse(w, "new ip already exists as trusted proxy")
			return
		}
	}

	removed := accessController.RemoveTrustedProxy(originalIP)
	if !removed {
		utils.SendErrorResponse(w, "original proxy not found")
		return
	}

	accessController.AddTrustedProxy(newIP, desc)

	err = accessController.SaveTrustedProxies()
	if err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

// Replaces the trusted proxy list from a CSV body ("ip/cidr, description" per
// line; lines starting with # are skipped).
func handleBulkUpdateTrustedProxies(w http.ResponseWriter, r *http.Request) {
	csv, err := utils.PostPara(r, "csv")
	if err != nil {
		utils.SendErrorResponse(w, "csv parameter is required")
		return
	}

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
			desc = cleanTrustedProxyDesc(parts[1])
		}
		entries = append(entries, entry{ip: ip, desc: desc})
	}

	for _, proxy := range accessController.ListTrustedProxies() {
		accessController.RemoveTrustedProxy(proxy.IP)
	}

	for _, e := range entries {
		accessController.AddTrustedProxy(e.ip, e.desc)
	}

	if err := accessController.SaveTrustedProxies(); err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	utils.SendOK(w)
}

// Resets the trusted proxy list to the defaults and returns it.
func handleResetDefaultTrustedProxies(w http.ResponseWriter, r *http.Request) {
	for _, proxy := range accessController.ListTrustedProxies() {
		accessController.RemoveTrustedProxy(proxy.IP)
	}

	for _, proxy := range accessController.GetDefaultTrustedProxies() {
		accessController.AddTrustedProxy(proxy.IP, proxy.Desc)
	}

	if err := accessController.SaveTrustedProxies(); err != nil {
		utils.SendErrorResponse(w, "failed to save trusted proxies: "+err.Error())
		return
	}

	result := accessController.ListTrustedProxies()
	js, _ := json.Marshal(result)
	utils.SendJSONResponse(w, string(js))
}

// Lists today's client addresses by request count.
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

		geoinfo, err := geodbStore.ResolveCountryCodeFromIP(ip)
		if err == nil {
			thisEntry.CountryCode = geoinfo.CountryIsoCode
		}

		result = append(result, thisEntry)
		return true
	})

	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	js, _ := json.Marshal(result)
	utils.SendJSONResponse(w, string(js))
}
