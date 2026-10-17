package main

/*
	capture_custom.go (custom fork)

	Traffic capture (mod/capture) from the web interface: start a capture
	with filters and limits, watch it, stop it, download or delete the
	files. Files are kept in ./captures on this node only (readable only by
	Waymark's user), never synced to other HA nodes, and deleted after
	captureRetention. Captures can be run on a read-only HA standby.

	Starting, stopping, downloading and deleting are written to the system
	log with the filters used.
*/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/capture"
	"imuslab.com/zoraxy/mod/utils"
)

const (
	captureDir       = "./captures"
	captureRetention = 24 * time.Hour
)

var captureManager *capture.Manager

func initTrafficCapture() {
	m, err := capture.NewManager(captureDir, captureRetention,
		func() string { return haNodeName(haConfig()) },
		func(msg string) { SystemWideLogger.PrintAndLog("capture", msg, nil) })
	if err != nil {
		SystemWideLogger.PrintAndLog("capture", "Traffic capture is unavailable: cannot use "+captureDir, err)
		return
	}
	captureManager = m
}

// stopTrafficCapture finishes a running capture before Waymark stops or
// restarts (HA sync, update).
func stopTrafficCapture() {
	if captureManager != nil {
		captureManager.Stop("Waymark stopped or restarted")
	}
}

func captureUnavailable(w http.ResponseWriter) bool {
	if captureManager == nil {
		utils.SendErrorResponse(w, "traffic capture is unavailable on this node (see the system log)")
		return true
	}
	return false
}

// describeCapture summarises a capture's filters for the log.
func describeCapture(c capture.Config) string {
	parts := []string{"scope " + c.Scope, "sides " + c.Sides}
	if len(c.Hosts) > 0 {
		parts = append(parts, "hosts "+strings.Join(c.Hosts, ","))
	}
	if len(c.StreamRules) > 0 {
		names := []string{}
		for _, id := range c.StreamRules {
			names = append(names, streamRuleDisplay("stream:"+id))
		}
		parts = append(parts, "stream rules "+strings.Join(names, ","))
	}
	if len(c.Clients) > 0 {
		parts = append(parts, "clients "+strings.Join(c.Clients, ","))
	}
	parts = append(parts, fmt.Sprintf("up to %s and %d MB", time.Duration(c.DurationSeconds)*time.Second, c.MaxMB))
	if c.Redact {
		parts = append(parts, "credentials redacted")
	}
	return strings.Join(parts, ", ")
}

func requestUser(r *http.Request) string {
	if authAgent != nil {
		if u, err := authAgent.GetUserName(nil, r); err == nil && u != "" {
			return u
		}
	}
	return "admin"
}

// HandleCaptureStatus returns the running capture, the stored files, and
// the rules a capture can be limited to.
// GET /api/capture/status
func HandleCaptureStatus(w http.ResponseWriter, r *http.Request) {
	if captureUnavailable(w) {
		return
	}
	resp := map[string]any{
		"files":          captureManager.Files(),
		"retentionHours": int(captureManager.Retention().Hours()),
		"maxDurationMin": capture.MaxDurationSeconds / 60,
		"maxSizeMb":      capture.MaxSizeMB,
	}
	if st, ok := captureManager.Running(); ok {
		resp["running"] = st
	}
	httpRules := []map[string]any{}
	if dynamicProxyRouter != nil {
		for _, ep := range dynamicProxyRouter.GetProxyEndpointsAsMap() {
			hosts := append([]string{ep.RootOrMatchingDomain}, ep.MatchingDomainAlias...)
			httpRules = append(httpRules, map[string]any{"name": ep.RootOrMatchingDomain, "hosts": hosts, "disabled": ep.Disabled})
		}
	}
	streamRules := []map[string]any{}
	if streamProxyManager != nil {
		for _, c := range streamProxyManager.Configs {
			streamRules = append(streamRules, map[string]any{"uuid": c.UUID, "name": streamRuleName(c), "running": c.Running, "tcp": c.UseTCP, "udp": c.UseUDP})
		}
	}
	resp["httpRules"], resp["streamRules"] = httpRules, streamRules
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleCaptureStart starts a capture.
// POST /api/capture/start  config=<json capture.Config>
func HandleCaptureStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if captureUnavailable(w) {
		return
	}
	raw, err := utils.PostPara(r, "config")
	if err != nil {
		utils.SendErrorResponse(w, "config not defined")
		return
	}
	cfg := capture.Config{}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		utils.SendErrorResponse(w, "invalid settings: "+err.Error())
		return
	}
	st, err := captureManager.Start(cfg)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	SystemWideLogger.PrintAndLog("capture", "Traffic capture "+st.File+" started by "+requestUser(r)+": "+describeCapture(st.Config), nil)
	js, _ := json.Marshal(st)
	utils.SendJSONResponse(w, string(js))
}

// HandleCaptureStop stops the running capture.
// POST /api/capture/stop
func HandleCaptureStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if captureUnavailable(w) {
		return
	}
	file, ok := captureManager.Stop("stopped by " + requestUser(r))
	if !ok {
		utils.SendErrorResponse(w, "no capture is running")
		return
	}
	js, _ := json.Marshal(map[string]string{"file": file})
	utils.SendJSONResponse(w, string(js))
}

// HandleCaptureDownload sends a capture file.
// GET /api/capture/download?name=<file>
func HandleCaptureDownload(w http.ResponseWriter, r *http.Request) {
	if captureUnavailable(w) {
		return
	}
	name, _ := utils.GetPara(r, "name")
	p, err := captureManager.Path(name)
	if err != nil {
		http.Error(w, "unknown capture file", http.StatusNotFound)
		return
	}
	if st, ok := captureManager.Running(); ok && st.File == name {
		http.Error(w, "the capture is still running; stop it first", http.StatusConflict)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "cannot read the capture file", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	SystemWideLogger.PrintAndLog("capture", "Traffic capture "+name+" downloaded by "+requestUser(r), nil)
	w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// HandleCaptureDelete deletes a capture file.
// POST /api/capture/delete  name=<file>
func HandleCaptureDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if captureUnavailable(w) {
		return
	}
	name, _ := utils.PostPara(r, "name")
	if err := captureManager.Delete(name); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	SystemWideLogger.PrintAndLog("capture", "Traffic capture "+name+" deleted by "+requestUser(r), nil)
	utils.SendOK(w)
}
