package main

// Settings for where system and traffic logs go (conf/log_outputs.json).

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/utils"
)

func logOutputsPath() string { return CONF_FOLDER + "/log_outputs.json" }

// initLogOutputs applies the saved settings to the system logger.
func initLogOutputs() {
	o, err := logger.LoadLogOutputs(logOutputsPath())
	if err != nil {
		SystemWideLogger.PrintAndLog("logger", "Log output settings are invalid, using the defaults (everything to the log file)", err)
	}
	SystemWideLogger.ApplyOutputs(o)
	if n := enabledSyslog(o); n > 0 {
		SystemWideLogger.PrintAndLog("logger", "Sending logs to "+itoa(n)+" syslog server(s)", nil)
	}
}

func enabledSyslog(o *logger.LogOutputs) int {
	n := 0
	for _, d := range o.Syslog {
		if d.Enabled && (d.System || d.Traffic) {
			n++
		}
	}
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }

// HandleLogOutputs reads (GET) or saves and applies (POST outputs=<json>)
// the log output settings.
// /api/logger/outputs
func HandleLogOutputs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		js, _ := json.Marshal(map[string]any{
			"outputs":  SystemWideLogger.Outputs(),
			"status":   SystemWideLogger.SyslogStatuses(),
			"maxSinks": logger.MaxSyslogDestinations,
		})
		utils.SendJSONResponse(w, string(js))
	case http.MethodPost:
		raw, err := utils.PostPara(r, "outputs")
		if err != nil {
			utils.SendErrorResponse(w, "outputs not defined")
			return
		}
		o := logger.DefaultLogOutputs()
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(o); err != nil {
			utils.SendErrorResponse(w, "invalid settings: "+err.Error())
			return
		}
		if err := o.Validate(); err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
		if err := logger.SaveLogOutputs(logOutputsPath(), o); err != nil {
			utils.SendErrorResponse(w, "could not save the settings: "+err.Error())
			return
		}
		SystemWideLogger.ApplyOutputs(o)
		SystemWideLogger.PrintAndLog("logger", "Log outputs updated: system to file "+boolWord(o.SystemToFile)+", traffic to file "+boolWord(o.TrafficToFile)+", "+itoa(enabledSyslog(o))+" syslog server(s)", nil)
		js, _ := json.Marshal(map[string]any{"outputs": o})
		utils.SendJSONResponse(w, string(js))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// HandleLogOutputsTest sends one test message to a syslog server.
// POST /api/logger/outputs/test  destination=<json>
func HandleLogOutputsTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := utils.PostPara(r, "destination")
	if err != nil {
		utils.SendErrorResponse(w, "destination not defined")
		return
	}
	var d logger.SyslogDestination
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		utils.SendErrorResponse(w, "invalid destination: "+err.Error())
		return
	}
	if err := logger.TestSyslog(d); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	msg := "Test message sent to " + d.Host
	if strings.EqualFold(d.Protocol, "udp") {
		msg += " (UDP has no delivery confirmation: check that it arrived)"
	}
	js, _ := json.Marshal(map[string]string{"message": msg})
	utils.SendJSONResponse(w, string(js))
}
