package streamproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"imuslab.com/zoraxy/mod/utils"
)

/*
	Handler.go
	Handlers for the tcprox. Remove this file
	if your application do not need any http
	handler.
*/

func (m *Manager) HandleAddProxyConfig(w http.ResponseWriter, r *http.Request) {
	name, err := utils.PostPara(r, "name")
	if err != nil {
		utils.SendErrorResponse(w, "name cannot be empty")
		return
	}

	listenAddr, err := utils.PostPara(r, "listenAddr")
	if err != nil {
		utils.SendErrorResponse(w, "first address cannot be empty")
		return
	}

	proxyAddr, err := utils.PostPara(r, "proxyAddr")
	if err != nil {
		utils.SendErrorResponse(w, "second address cannot be empty")
		return
	}

	timeoutStr, _ := utils.PostPara(r, "timeout")
	timeout := m.Options.DefaultTimeout
	if timeoutStr != "" {
		timeout, err = strconv.Atoi(timeoutStr)
		if err != nil {
			utils.SendErrorResponse(w, "invalid timeout value: "+timeoutStr)
			return
		}
	}

	useTCP, _ := utils.PostBool(r, "useTCP")
	useUDP, _ := utils.PostBool(r, "useUDP")
	ProxyProtocolVersion, _ := utils.PostInt(r, "proxyProtocolVersion")
	enableLogging, _ := utils.PostBool(r, "enableLogging")
	accessRuleUUID, _ := utils.PostPara(r, "accessRuleUUID")

	//Create the target config
	//Custom fork: reject rules that could never listen
	if err := m.validateListener(strings.TrimSpace(listenAddr), useTCP, useUDP, ""); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	if _, err := ValidateUpstreams([]string{proxyAddr}); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	newConfigUUID := m.NewConfig(&ProxyRelayOptions{
		Name:                 name,
		ListeningAddr:        strings.TrimSpace(listenAddr),
		ProxyAddr:            strings.TrimSpace(proxyAddr),
		Timeout:              timeout,
		UseTCP:               useTCP,
		UseUDP:               useUDP,
		ProxyProtocolVersion: convertIntToProxyProtocolVersion(ProxyProtocolVersion),
		EnableLogging:        enableLogging,
		AccessRuleUUID:       accessRuleUUID,
	})

	js, _ := json.Marshal(newConfigUUID)
	utils.SendJSONResponse(w, string(js))
}

func (m *Manager) HandleEditProxyConfigs(w http.ResponseWriter, r *http.Request) {
	// Extract POST parameters using utils.PostPara
	configUUID, err := utils.PostPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "config UUID cannot be empty")
		return
	}

	newName, _ := utils.PostPara(r, "name")
	listenAddr, _ := utils.PostPara(r, "listenAddr")
	proxyAddr, _ := utils.PostPara(r, "proxyAddr")
	useTCP, _ := utils.PostBool(r, "useTCP")
	useUDP, _ := utils.PostBool(r, "useUDP")
	proxyProtocolVersion, _ := utils.PostInt(r, "proxyProtocolVersion")
	enableLogging, _ := utils.PostBool(r, "enableLogging")
	accessRuleUUID, _ := utils.PostPara(r, "accessRuleUUID")

	newTimeoutStr, _ := utils.PostPara(r, "timeout")
	newTimeout := -1
	if newTimeoutStr != "" {
		newTimeout, err = strconv.Atoi(newTimeoutStr)
		if err != nil {
			utils.SendErrorResponse(w, "invalid newTimeout value: "+newTimeoutStr)
			return
		}
	}

	// Create a new ProxyRuleUpdateConfig with the extracted parameters
	//Custom fork: reject rules that could never listen
	if existing, err := m.GetConfigByUUID(configUUID); err == nil {
		effective := strings.TrimSpace(listenAddr)
		if effective == "" {
			effective = existing.View().ListeningAddress
		}
		if err := m.validateListener(effective, useTCP, useUDP, configUUID); err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
	}

	newConfig := &ProxyRuleUpdateConfig{
		InstanceUUID:         configUUID,
		NewName:              newName,
		NewListeningAddr:     listenAddr,
		NewProxyAddr:         proxyAddr,
		UseTCP:               useTCP,
		UseUDP:               useUDP,
		ProxyProtocolVersion: proxyProtocolVersion,
		EnableLogging:        enableLogging,
		NewTimeout:           newTimeout,
		NewAccessRuleUUID:    accessRuleUUID,
	}

	// Call the EditConfig method to modify the configuration
	err = m.EditConfig(newConfig)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	utils.SendOK(w)
}

func (m *Manager) HandleListConfigs(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(m.List())
	utils.SendJSONResponse(w, string(js))
}

func (m *Manager) HandleStartProxy(w http.ResponseWriter, r *http.Request) {
	uuid, err := utils.PostPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "invalid uuid given")
		return
	}

	targetProxyConfig, err := m.GetConfigByUUID(uuid)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	err = targetProxyConfig.Start()
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	utils.SendOK(w)
}

func (m *Manager) HandleStopProxy(w http.ResponseWriter, r *http.Request) {
	uuid, err := utils.PostPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "invalid uuid given")
		return
	}

	targetProxyConfig, err := m.GetConfigByUUID(uuid)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	if !targetProxyConfig.IsRunning() {
		utils.SendErrorResponse(w, "target proxy service is not running")
		return
	}

	targetProxyConfig.Stop()
	utils.SendOK(w)
}

func (m *Manager) HandleRemoveProxy(w http.ResponseWriter, r *http.Request) {
	uuid, err := utils.PostPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "invalid uuid given")
		return
	}

	targetProxyConfig, err := m.GetConfigByUUID(uuid)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	if targetProxyConfig.IsRunning() {
		utils.SendErrorResponse(w, "Service is running")
		return
	}

	err = m.RemoveConfig(targetProxyConfig.UUID)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	utils.SendOK(w)
}

func (m *Manager) HandleGetProxyStatus(w http.ResponseWriter, r *http.Request) {
	uuid, err := utils.GetPara(r, "uuid")
	if err != nil {
		utils.SendErrorResponse(w, "invalid uuid given")
		return
	}

	targetConfig, err := m.GetConfigByUUID(uuid)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}

	js, _ := json.Marshal(targetConfig)
	utils.SendJSONResponse(w, string(js))
}

// validateListener checks a rule's listening address and protocols (custom
// fork): at least one protocol, a usable host:port or port, and no other
// rule listening on the same address with the same protocol.
func (m *Manager) validateListener(listen string, useTCP, useUDP bool, exceptUUID string) error {
	if !useTCP && !useUDP {
		return errors.New("enable TCP, UDP or both")
	}
	port := listen
	host := ""
	if h, p, err := net.SplitHostPort(listen); err == nil {
		host, port = h, p
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("listening address must be a port or host:port, e.g. :25565 or 127.0.0.1:25565")
	}
	if host != "" && net.ParseIP(host) == nil && !validHostname(host) {
		return errors.New("listening address host must be an IP address or host name")
	}
	norm := func(a string) string {
		h, p, err := net.SplitHostPort(a)
		if err != nil {
			h, p = "", a
		}
		if h == "0.0.0.0" || h == "::" {
			h = ""
		}
		return h + "|" + p
	}
	want := norm(listen)
	for _, c := range m.List() {
		v := c.View()
		if v.UUID == exceptUUID {
			continue
		}
		other := norm(strings.TrimSpace(v.ListeningAddress))
		samePort := strings.SplitN(other, "|", 2)[1] == strings.SplitN(want, "|", 2)[1]
		wildcard := strings.HasPrefix(other, "|") || strings.HasPrefix(want, "|")
		if (other == want || (samePort && wildcard)) && ((useTCP && v.UseTCP) || (useUDP && v.UseUDP)) {
			name := v.Name
			if name == "" {
				name = v.UUID
			}
			return fmt.Errorf("stream proxy %q already uses %s", name, v.ListeningAddress)
		}
	}
	return nil
}
