package main

import (
	"encoding/json"
	"net/http"

	"imuslab.com/zoraxy/mod/utils"
)

// HandleBrandInfo returns the product name, version and the Zoraxy release
// it is based on, for the web UI's About page.
// GET /api/info/brand
func HandleBrandInfo(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(map[string]string{
		"name":         PRODUCT_NAME,
		"shortName":    PRODUCT_SHORT_NAME,
		"version":      PRODUCT_VERSION,
		"upstreamBase": UPSTREAM_BASE,
		"repo":         PRODUCT_REPO,
	})
	utils.SendJSONResponse(w, string(js))
}
