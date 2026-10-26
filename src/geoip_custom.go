package main

/*
	geoip_custom.go (custom fork)

	Setting to turn GeoIP lookups off. While off, the GeoIP dataset is not
	loaded (saving memory and a lookup per request), statistics record no
	visitor countries, and access rules cannot use country lists.

	Turning lookups off is refused while an enabled access rule list still
	contains countries, because those entries would silently stop working
	(a country blacklist would stop blocking; a country whitelist would
	block everyone it was meant to allow).
*/

import (
	"encoding/json"
	"net/http"
	"strings"

	"imuslab.com/zoraxy/mod/utils"
)

const geoIPEnabledKey = "geoip_enabled"

// geoIPEnabledSetting reads the saved setting (on unless turned off).
func geoIPEnabledSetting() bool {
	enabled := true
	if sysdb != nil && sysdb.KeyExists(systemTable, geoIPEnabledKey) {
		sysdb.Read(systemTable, geoIPEnabledKey, &enabled)
	}
	return enabled
}

// accessRulesUsingCountries lists the ACLs with an enabled rule that needs
// a country (Custom fork: ACLs).
func accessRulesUsingCountries() []string {
	if accessController == nil {
		return []string{}
	}
	return accessController.ACLsUsingCountries()
}

// HandleGeoIPEnabled reads (GET) or sets (POST enable=true/false) whether
// GeoIP lookups are on.
func HandleGeoIPEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		js, _ := json.Marshal(map[string]any{
			"enabled":             geodbStore.Enabled(),
			"rulesUsingCountries": accessRulesUsingCountries(),
		})
		utils.SendJSONResponse(w, string(js))
		return
	}
	enable, err := utils.PostBool(r, "enable")
	if err != nil {
		utils.SendErrorResponse(w, "enable not defined")
		return
	}
	if !enable {
		if rules := accessRulesUsingCountries(); len(rules) > 0 {
			utils.SendErrorResponse(w, "Remove the country rules from these ACLs first (they need GeoIP to work): "+strings.Join(rules, ", "))
			return
		}
	}
	if err := geodbStore.SetEnabled(enable); err != nil {
		utils.SendErrorResponse(w, "could not load the GeoIP database: "+err.Error())
		return
	}
	if err := sysdb.Write(systemTable, geoIPEnabledKey, enable); err != nil {
		utils.SendErrorResponse(w, "setting applied but could not be saved")
		return
	}
	if enable {
		SystemWideLogger.PrintAndLog("geodb", "GeoIP lookups turned on", nil)
	} else {
		SystemWideLogger.PrintAndLog("geodb", "GeoIP lookups turned off; GeoIP database unloaded", nil)
	}
	utils.SendOK(w)
}
