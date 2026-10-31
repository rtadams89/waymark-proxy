package main

// Stores which status page panels are shown, their order and widths (the panel
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
	statusPanelSizesKey = "status_panel_sizes" // panel id -> width
	maxStatusPanelCount = 64
)

var statusPanelIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,47}$`)

// statusPanelWidths are the widths a panel can have (which ones a panel
// offers is decided in statusPanels.js)
var statusPanelWidths = map[string]bool{"quarter": true, "third": true, "half": true, "twothirds": true, "full": true}

// statusPanelLayout is what GET returns.
type statusPanelLayout struct {
	Visible map[string]bool   `json:"visible"`
	Order   []string          `json:"order"`
	Sizes   map[string]string `json:"sizes"`
}

func readStatusPanelLayout(db *database.Database) statusPanelLayout {
	layout := statusPanelLayout{Visible: map[string]bool{}, Order: []string{}, Sizes: map[string]string{}}
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
	if db.KeyExists(customUITable, statusPanelSizesKey) {
		if err := db.Read(customUITable, statusPanelSizesKey, &layout.Sizes); err != nil || layout.Sizes == nil {
			layout.Sizes = map[string]string{}
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

func parseStatusPanelSizes(raw string) (map[string]string, string) {
	sizes := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &sizes); err != nil || sizes == nil {
		return nil, "sizes must be a JSON object of panel id to width"
	}
	if len(sizes) > maxStatusPanelCount {
		return nil, "too many panels"
	}
	for id, w := range sizes {
		if !statusPanelIDPattern.MatchString(id) {
			return nil, "invalid panel id"
		}
		if !statusPanelWidths[w] {
			return nil, "invalid panel width"
		}
	}
	return sizes, ""
}

// saveStatusPanelLayout checks every value before writing any; an empty
// string leaves that value unchanged.
func saveStatusPanelLayout(db *database.Database, rawPanels, rawOrder, rawSizes string) string {
	var panels map[string]bool
	var order []string
	var sizes map[string]string
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
	if rawSizes != "" {
		if sizes, msg = parseStatusPanelSizes(rawSizes); msg != "" {
			return msg
		}
	}
	if panels == nil && order == nil && sizes == nil {
		return "panels, order or sizes not defined"
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
	if sizes != nil {
		if err := db.Write(customUITable, statusPanelSizesKey, sizes); err != nil {
			return "failed to save panel widths"
		}
	}
	return ""
}

// HandleStatusPanels reads (GET: {"visible": {...}, "order": [...],
// "sizes": {...}}) or saves (POST any of panels=<JSON object of id to
// true/false>, order=<JSON array of ids>, sizes=<JSON object of id to
// width>) the status page panel layout.
func HandleStatusPanels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		js, _ := json.Marshal(readStatusPanelLayout(sysdb))
		utils.SendJSONResponse(w, string(js))

	case http.MethodPost:
		rawPanels, _ := utils.PostPara(r, "panels")
		rawOrder, _ := utils.PostPara(r, "order")
		rawSizes, _ := utils.PostPara(r, "sizes")
		if msg := saveStatusPanelLayout(sysdb, rawPanels, rawOrder, rawSizes); msg != "" {
			utils.SendErrorResponse(w, msg)
			return
		}
		utils.SendOK(w)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
