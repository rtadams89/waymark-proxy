package main

// The proxy's TLS profile setting.

import (
	"encoding/json"
	"net/http"

	"github.com/rtadams89/waymark-proxy/mod/dynamicproxy"
	"github.com/rtadams89/waymark-proxy/mod/utils"
)

const (
	tlsProfileKey     = "tlsProfile"
	legacyMinTLSKey   = "minTLSVersion"
	newInstallProfile = dynamicproxy.TLSProfileIntermediate
)

// freshInstall is true when this start created sys.uuid.
var freshInstall bool

// profileFromMinTLSVersion maps a saved minimum TLS version to a profile.
func profileFromMinTLSVersion(v string) dynamicproxy.TLSProfile {
	switch v {
	case "1.3":
		return dynamicproxy.TLSProfileModern
	case "1.0", "1.1":
		return dynamicproxy.TLSProfileLegacy
	}
	return dynamicproxy.TLSProfileCompatible
}

// minTLSVersionForProfile returns the minimum TLS version saved with p.
func minTLSVersionForProfile(p dynamicproxy.TLSProfile) string {
	switch p {
	case dynamicproxy.TLSProfileModern:
		return "1.3"
	case dynamicproxy.TLSProfileLegacy:
		return "1.0"
	}
	return "1.2"
}

// readTLSProfile returns the saved profile, or the one matching the saved
// minimum TLS version.
func readTLSProfile() dynamicproxy.TLSProfile {
	var p dynamicproxy.TLSProfile
	if sysdb.KeyExists("settings", tlsProfileKey) {
		if err := sysdb.Read("settings", tlsProfileKey, &p); err == nil && p.IsValid() {
			return p
		}
		SystemWideLogger.Println("The saved TLS profile is not valid; using intermediate")
		return dynamicproxy.TLSProfileIntermediate
	}
	old := "1.2"
	sysdb.Read("settings", legacyMinTLSKey, &old)
	return profileFromMinTLSVersion(old)
}

// initTLSProfileForNewInstall saves the intermediate profile on a new install.
func initTLSProfileForNewInstall() {
	if freshInstall && !sysdb.KeyExists("settings", tlsProfileKey) {
		saveTLSProfile(newInstallProfile)
	}
}

func saveTLSProfile(p dynamicproxy.TLSProfile) error {
	if err := sysdb.Write("settings", tlsProfileKey, p); err != nil {
		return err
	}
	return sysdb.Write("settings", legacyMinTLSKey, minTLSVersionForProfile(p))
}

// HandleTLSProfile: GET returns the profile; POST profile=<name> sets it and
// restarts the HTTPS listener.
func HandleTLSProfile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		js, _ := json.Marshal(readTLSProfile())
		utils.SendJSONResponse(w, string(js))
	case http.MethodPost:
		raw, _ := utils.PostPara(r, "profile")
		p := dynamicproxy.TLSProfile(raw)
		if !p.IsValid() {
			utils.SendErrorResponse(w, "unknown TLS profile")
			return
		}
		if err := saveTLSProfile(p); err != nil {
			utils.SendErrorResponse(w, "cannot save the TLS profile")
			return
		}
		SystemWideLogger.PrintAndLog("tls", "TLS profile set to "+string(p), nil)
		dynamicProxyRouter.SetTLSProfile(p)
		utils.SendOK(w)
	default:
		http.Error(w, "405 - Method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleTLSFailures: GET returns this node's failed TLS handshakes; POST
// action=clear clears them.
func HandleTLSFailures(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		js, _ := json.Marshal(dynamicProxyRouter.TLSFailures())
		utils.SendJSONResponse(w, string(js))
	case http.MethodPost:
		if action, _ := utils.PostPara(r, "action"); action != "clear" {
			utils.SendErrorResponse(w, "unknown action")
			return
		}
		dynamicProxyRouter.ClearTLSFailures()
		utils.SendOK(w)
	default:
		http.Error(w, "405 - Method not allowed", http.StatusMethodNotAllowed)
	}
}
