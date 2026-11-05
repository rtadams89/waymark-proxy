package main

import (
	"encoding/json"
	"net/http"

	"github.com/rtadams89/waymark-proxy/mod/utils"
)

// HandleBrandInfo returns the product name, version and repository.
func HandleBrandInfo(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(map[string]string{
		"name":      PRODUCT_NAME,
		"shortName": PRODUCT_SHORT_NAME,
		"version":   PRODUCT_VERSION,
		"repo":      PRODUCT_REPO,
	})
	utils.SendJSONResponse(w, string(js))
}
