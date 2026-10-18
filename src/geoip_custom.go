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
	"sort"
	"strings"

	"imuslab.com/zoraxy/mod/utils"
)

const geoIPEnabledKey = "geoip_enabled"

// geoIPEnabledSetting reads the saved setting (on unless turned off).
func geoIPEnabledSetting() bool {
	enabled := true
	if sysdb != nil && sysdb.TableExists(migrationTable) && sysdb.KeyExists(migrationTable, geoIPEnabledKey) {
		sysdb.Read(migrationTable, geoIPEnabledKey, &enabled)
	}
	return enabled
}

// accessRulesUsingCountries lists access rules whose enabled blacklist or
// whitelist contains country codes.
func accessRulesUsingCountries() []string {
	names := []string{}
	if accessController == nil {
		return names
	}
	for _, rule := range accessController.ListAllAccessRules() {
		uses := (rule.BlacklistEnabled && rule.BlackListContryCode != nil && len(*rule.BlackListContryCode) > 0) ||
			(rule.WhitelistEnabled && rule.WhiteListCountryCode != nil && len(*rule.WhiteListCountryCode) > 0)
		if uses {
			name := rule.Name
			if name == "" {
				name = rule.ID
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
			utils.SendErrorResponse(w, "Remove the countries from these access rules first (they need GeoIP to work): "+strings.Join(rules, ", "))
			return
		}
	}
	if err := geodbStore.SetEnabled(enable); err != nil {
		utils.SendErrorResponse(w, "could not load the GeoIP database: "+err.Error())
		return
	}
	if !sysdb.TableExists(migrationTable) {
		sysdb.NewTable(migrationTable)
	}
	if err := sysdb.Write(migrationTable, geoIPEnabledKey, enable); err != nil {
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

// requireGeoIP wraps the country list endpoints: countries cannot be added
// while GeoIP lookups are off.
func requireGeoIP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if geodbStore != nil && !geodbStore.Enabled() {
			utils.SendErrorResponse(w, "GeoIP lookups are turned off (Utilities, System tab). Turn them on to use country lists.")
			return
		}
		next(w, r)
	}
}
