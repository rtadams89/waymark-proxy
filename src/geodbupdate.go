package main

import (
	"encoding/json"
	"net/http"

	"imuslab.com/zoraxy/mod/utils"
)

// API of the GeoIP database update (manual and scheduled).

func HandleGeoDBUpdateStatus(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(geodbStore.GetUpdaterStatus())
	utils.SendJSONResponse(w, string(js))
}

// HandleGeoDBUpdateNow updates the GeoIP database and reloads it.
func HandleGeoDBUpdateNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "Method not allowed")
		return
	}

	err := geodbStore.UpdateGeoDBNow()
	if err != nil {
		SystemWideLogger.PrintAndLog("geodb", "Geo-IP database update failed", err)
		utils.SendErrorResponse(w, err.Error())
		return
	}

	SystemWideLogger.PrintAndLog("geodb", "Geo-IP database updated by administrator", nil)
	utils.SendOK(w)
}

// HandleGeoDBAutoUpdate gets (GET) or sets (POST enable) the scheduled
// GeoIP database update.
func HandleGeoDBAutoUpdate(w http.ResponseWriter, r *http.Request) {
	enable, err := utils.PostBool(r, "enable")
	if err != nil {
		js, _ := json.Marshal(geodbStore.GetUpdaterStatus().AutoUpdateEnabled)
		utils.SendJSONResponse(w, string(js))
		return
	}

	err = geodbStore.SetGeoDBAutoUpdate(enable)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	if enable {
		SystemWideLogger.PrintAndLog("geodb", "Scheduled Geo-IP database update enabled", nil)
	} else {
		SystemWideLogger.PrintAndLog("geodb", "Scheduled Geo-IP database update disabled", nil)
	}
	utils.SendOK(w)
}
