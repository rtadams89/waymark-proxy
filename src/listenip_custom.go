package main

// The address an HTTP rule answers on: every address, a floating IP, or
// with HA off one of this host's addresses.

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"imuslab.com/zoraxy/mod/utils"
)

type listenIPOption struct {
	Address  string `json:"address"`
	Label    string `json:"label"`
	Floating bool   `json:"floating"` // a floating IP of the HA cluster
	Held     bool   `json:"held"`     // currently on this node
}

// listenIPOptions are the addresses an HTTP rule can be bound to.
func listenIPOptions() []listenIPOption {
	out := []listenIPOption{}
	cfg := haConfig()
	if cfg.Enabled {
		for _, f := range haFloatingIPsHeld(cfg) {
			addr, _ := f["address"].(string)
			label, _ := f["label"].(string)
			held, _ := f["held"].(bool)
			if addr != "" {
				out = append(out, listenIPOption{Address: addr, Label: label, Floating: true, Held: held})
			}
		}
		return out
	}
	for _, ip := range localIPs() {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		out = append(out, listenIPOption{Address: ip.String(), Held: true})
	}
	return out
}

// normalizeListenIP checks an address for an HTTP rule: "" or one of
// listenIPOptions; keep (the rule's current address) is also accepted.
func normalizeListenIP(v, keep string) (string, error) {
	if v == "" {
		return "", nil
	}
	ip := net.ParseIP(v)
	if ip == nil {
		return "", errors.New("not an IP address: " + v)
	}
	if k := net.ParseIP(keep); k != nil && k.Equal(ip) {
		return keep, nil
	}
	for _, o := range listenIPOptions() {
		if oip := net.ParseIP(o.Address); oip != nil && oip.Equal(ip) {
			return oip.String(), nil
		}
	}
	if haConfig().Enabled {
		return "", errors.New(v + " is not a floating IP of the cluster (System > High Availability)")
	}
	return "", errors.New(v + " is not an address of this host")
}

// HandleProxyListenIPOptions lists the addresses an HTTP rule can answer on.
// GET /api/proxy/listenip/options
func HandleProxyListenIPOptions(w http.ResponseWriter, r *http.Request) {
	js, _ := json.Marshal(map[string]any{"options": listenIPOptions(), "haEnabled": haConfig().Enabled})
	utils.SendJSONResponse(w, string(js))
}

// HandleProxyListenIPSet sets the address an HTTP rule answers on.
// POST /api/proxy/listenip/set  ep=<rule>  ip=<address or empty>
func HandleProxyListenIPSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	epName, err := utils.PostPara(r, "ep")
	if err != nil {
		utils.SendErrorResponse(w, "ep not defined")
		return
	}
	ipStr, _ := utils.PostPara(r, "ip")
	ep, err := dynamicProxyRouter.LoadProxy(epName)
	if err != nil {
		utils.SendErrorResponse(w, "proxy rule not found")
		return
	}
	ip, err := normalizeListenIP(ipStr, ep.ListenIP)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	ep.ListenIP = ip
	if err := SaveReverseProxyConfig(ep); err != nil {
		utils.SendErrorResponse(w, "failed to save config: "+err.Error())
		return
	}
	ep.UpdateToRuntime()
	where := "every address"
	if ip != "" {
		where = ip
	}
	SystemWideLogger.PrintAndLog("proxy-config", "Proxy rule "+ep.RootOrMatchingDomain+" now answers on "+where, nil)
	utils.SendOK(w)
}
