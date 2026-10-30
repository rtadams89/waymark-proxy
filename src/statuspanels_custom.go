package main

// Stores which status page panels are shown (the panel list, labels and
// defaults are in web/script/statusPanels.js).

import (
	"encoding/json"
	"net/http"
	"regexp"

	"imuslab.com/zoraxy/mod/utils"
)

const (
	customUITable       = "custom_ui"
	statusPanelsKey     = "status_panels"
	maxStatusPanelCount = 64
)

var statusPanelIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,47}$`)

// HandleStatusPanels reads (GET) or saves (POST panels=<json map id->bool>)
// the status page panel visibility settings.
func HandleStatusPanels(w http.ResponseWriter, r *http.Request) {
	if !sysdb.TableExists(customUITable) {
		sysdb.NewTable(customUITable)
	}

	switch r.Method {
	case http.MethodGet:
		panels := map[string]bool{}
		if sysdb.KeyExists(customUITable, statusPanelsKey) {
			if err := sysdb.Read(customUITable, statusPanelsKey, &panels); err != nil {
				panels = map[string]bool{}
			}
		}
		js, _ := json.Marshal(panels)
		utils.SendJSONResponse(w, string(js))

	case http.MethodPost:
		raw, err := utils.PostPara(r, "panels")
		if err != nil {
			utils.SendErrorResponse(w, "panels not defined")
			return
		}
		panels := map[string]bool{}
		if err := json.Unmarshal([]byte(raw), &panels); err != nil {
			utils.SendErrorResponse(w, "panels must be a JSON object of panel id to true/false")
			return
		}
		if len(panels) > maxStatusPanelCount {
			utils.SendErrorResponse(w, "too many panels")
			return
		}
		for id := range panels {
			if !statusPanelIDPattern.MatchString(id) {
				utils.SendErrorResponse(w, "invalid panel id")
				return
			}
		}
		if err := sysdb.Write(customUITable, statusPanelsKey, panels); err != nil {
			utils.SendErrorResponse(w, "failed to save panel settings")
			return
		}
		utils.SendOK(w)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
