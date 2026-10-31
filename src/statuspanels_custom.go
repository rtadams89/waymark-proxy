package main

// Stores which status page panels are shown and their order (the panel
// list, labels and defaults are in web/script/statusPanels.js). The
// custom_ui table is conf/ui.json, which HA config sync copies from the
// active node to the others.

import (
	"encoding/json"
	"net/http"
	"regexp"

	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	customUITable       = "custom_ui"
	statusPanelsKey     = "status_panels"      // panel id -> shown
	statusPanelOrderKey = "status_panel_order" // panel ids, first to last
	maxStatusPanelCount = 64
)

var statusPanelIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,47}$`)

// statusPanelLayout is what GET returns.
type statusPanelLayout struct {
	Visible map[string]bool `json:"visible"`
	Order   []string        `json:"order"`
}

func readStatusPanelLayout(db *database.Database) statusPanelLayout {
	layout := statusPanelLayout{Visible: map[string]bool{}, Order: []string{}}
	if !db.TableExists(customUITable) {
		return layout
	}
	if db.KeyExists(customUITable, statusPanelsKey) {
		if err := db.Read(customUITable, statusPanelsKey, &layout.Visible); err != nil || layout.Visible == nil {
			layout.Visible = map[string]bool{}
		}
	}
	if db.KeyExists(customUITable, statusPanelOrderKey) {
		if err := db.Read(customUITable, statusPanelOrderKey, &layout.Order); err != nil || layout.Order == nil {
			layout.Order = []string{}
		}
	}
	return layout
}

func parseStatusPanelVisibility(raw string) (map[string]bool, string) {
	panels := map[string]bool{}
	if err := json.Unmarshal([]byte(raw), &panels); err != nil || panels == nil {
		return nil, "panels must be a JSON object of panel id to true/false"
	}
	if len(panels) > maxStatusPanelCount {
		return nil, "too many panels"
	}
	for id := range panels {
		if !statusPanelIDPattern.MatchString(id) {
			return nil, "invalid panel id"
		}
	}
	return panels, ""
}

func parseStatusPanelOrder(raw string) ([]string, string) {
	order := []string{}
	if err := json.Unmarshal([]byte(raw), &order); err != nil || order == nil {
		return nil, "order must be a JSON array of panel ids"
	}
	if len(order) > maxStatusPanelCount {
		return nil, "too many panels"
	}
	seen := map[string]bool{}
	for _, id := range order {
		if !statusPanelIDPattern.MatchString(id) {
			return nil, "invalid panel id"
		}
		if seen[id] {
			return nil, "a panel is listed twice"
		}
		seen[id] = true
	}
	return order, ""
}

// saveStatusPanelLayout checks both values before writing either; an empty
// string leaves that value unchanged.
func saveStatusPanelLayout(db *database.Database, rawPanels, rawOrder string) string {
	var panels map[string]bool
	var order []string
	var msg string
	if rawPanels != "" {
		if panels, msg = parseStatusPanelVisibility(rawPanels); msg != "" {
			return msg
		}
	}
	if rawOrder != "" {
		if order, msg = parseStatusPanelOrder(rawOrder); msg != "" {
			return msg
		}
	}
	if panels == nil && order == nil {
		return "panels or order not defined"
	}
	if !db.TableExists(customUITable) {
		db.NewTable(customUITable)
	}
	if panels != nil {
		if err := db.Write(customUITable, statusPanelsKey, panels); err != nil {
			return "failed to save panel settings"
		}
	}
	if order != nil {
		if err := db.Write(customUITable, statusPanelOrderKey, order); err != nil {
			return "failed to save panel order"
		}
	}
	return ""
}

// HandleStatusPanels reads (GET: {"visible": {...}, "order": [...]}) or
// saves (POST panels=<JSON object of id to true/false> and/or
// order=<JSON array of ids>) the status page panel layout.
func HandleStatusPanels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		js, _ := json.Marshal(readStatusPanelLayout(sysdb))
		utils.SendJSONResponse(w, string(js))

	case http.MethodPost:
		rawPanels, _ := utils.PostPara(r, "panels")
		rawOrder, _ := utils.PostPara(r, "order")
		if msg := saveStatusPanelLayout(sysdb, rawPanels, rawOrder); msg != "" {
			utils.SendErrorResponse(w, msg)
			return
		}
		utils.SendOK(w)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
