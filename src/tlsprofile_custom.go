package main

// The proxy's TLS profile (System > Settings > Advanced Settings): the lowest
// TLS version and the TLS 1.2 cipher suites, see
// mod/dynamicproxy/tlsprofile_custom.go. It replaces the minimum TLS version
// setting of Waymark 3.0.0 and earlier, and is stored in the settings table
// (conf/listener.json), which HA config sync copies to the other nodes.

import (
	"encoding/json"
	"net/http"

	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	tlsProfileKey     = "tlsProfile"
	legacyMinTLSKey   = "minTLSVersion" // Waymark 3.0.0 and earlier
	newInstallProfile = dynamicproxy.TLSProfileIntermediate
)

// freshInstall is set in main when this start created sys.uuid.
var freshInstall bool

// profileFromMinTLSVersion is the profile matching a minimum TLS version
// saved by an older Waymark: the same lowest version, with Go's default
// suites as before. 1.1 becomes legacy (TLS 1.0 and up): every client that
// supports 1.1 supports 1.0 too, so no client is turned away.
func profileFromMinTLSVersion(v string) dynamicproxy.TLSProfile {
	switch v {
	case "1.3":
		return dynamicproxy.TLSProfileModern
	case "1.0", "1.1":
		return dynamicproxy.TLSProfileLegacy
	}
	return dynamicproxy.TLSProfileCompatible
}

// minTLSVersionForProfile is what an older Waymark reads, so a node still on
// an older version during a rolling update keeps a matching lowest version.
func minTLSVersionForProfile(p dynamicproxy.TLSProfile) string {
	switch p {
	case dynamicproxy.TLSProfileModern:
		return "1.3"
	case dynamicproxy.TLSProfileLegacy:
		return "1.0"
	}
	return "1.2"
}

// readTLSProfile returns the saved profile. Without one, a minimum TLS
// version saved by an older Waymark decides (an install that never changed it
// keeps the old behaviour: TLS 1.2 with Go's default suites). Nothing is
// written here, so a synced HA standby never differs from the active node.
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

// initTLSProfileForNewInstall saves the intermediate profile on a new
// install, so new installs get Mozilla's recommendation.
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

// HandleTLSProfile returns (GET) or sets (POST profile=<name>) the proxy's
// TLS profile. Setting it restarts the HTTPS listener.
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
