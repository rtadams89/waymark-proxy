package main

// TLS profile exceptions: client sources that get a different TLS profile.
// The first enabled matching exception applies.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/rtadams89/waymark-proxy/mod/access"
	"github.com/rtadams89/waymark-proxy/mod/dynamicproxy"
	"github.com/rtadams89/waymark-proxy/mod/utils"
)

const (
	tlsExceptionsKey = "tlsProfileExceptions"
	maxTLSExceptions = 100
)

type tlsProfileException struct {
	ID      string                  `json:"id"`
	Enabled bool                    `json:"enabled"`
	Source  access.Source           `json:"source"`
	Profile dynamicproxy.TLSProfile `json:"profile"`
	Comment string                  `json:"comment,omitempty"`
}

var tlsExceptions atomic.Pointer[[]tlsProfileException]

func newTLSExceptionID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func normalizeTLSExceptions(in []tlsProfileException, normalizeSource func(access.Source) (access.Source, error)) ([]tlsProfileException, error) {
	if len(in) > maxTLSExceptions {
		return nil, fmt.Errorf("there can be at most %d exceptions", maxTLSExceptions)
	}
	out := make([]tlsProfileException, 0, len(in))
	ids := map[string]bool{}
	for i, e := range in {
		label := fmt.Sprintf("exception %d", i+1)
		if !access.ValidID(e.ID) || ids[e.ID] {
			e.ID = newTLSExceptionID()
		}
		ids[e.ID] = true
		src, err := normalizeSource(e.Source)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", label, err)
		}
		e.Source = src
		if !e.Profile.IsValid() {
			return nil, fmt.Errorf("%s: choose a TLS profile", label)
		}
		e.Comment = strings.TrimSpace(e.Comment)
		if len(e.Comment) > 200 {
			return nil, fmt.Errorf("%s: the comment is longer than 200 characters", label)
		}
		out = append(out, e)
	}
	return out, nil
}

// loadTLSExceptions reads the saved list and logs invalid exceptions.
func loadTLSExceptions() {
	list := []tlsProfileException{}
	if sysdb.KeyExists("settings", tlsExceptionsKey) {
		if err := sysdb.Read("settings", tlsExceptionsKey, &list); err != nil || list == nil {
			SystemWideLogger.PrintAndLog("tls", "The TLS profile exceptions cannot be read; none are used", err)
			list = []tlsProfileException{}
		}
	}
	for i, e := range list {
		if _, err := accessController.NormalizeSource(e.Source); err != nil || !e.Profile.IsValid() {
			SystemWideLogger.PrintAndLog("tls", fmt.Sprintf("TLS profile exception %d is not valid and is skipped", i+1), err)
		}
	}
	tlsExceptions.Store(&list)
}

func currentTLSExceptions() []tlsProfileException {
	if p := tlsExceptions.Load(); p != nil {
		return *p
	}
	return nil
}

func describeTLSSource(s access.Source) string {
	var d string
	switch s.Type {
	case access.SourceObject:
		d = accessController.ObjectName(s.Value)
	case access.SourceCountry:
		d = "country " + s.Value
	default:
		d = s.Value
	}
	if s.Not {
		d = "not " + d
	}
	return d
}

// tlsExceptionName returns the comment, or the position and source.
func tlsExceptionName(i int, e tlsProfileException) string {
	if e.Comment != "" {
		return e.Comment
	}
	return fmt.Sprintf("#%d %s", i+1, describeTLSSource(e.Source))
}

// matchTLSException returns the index of the first enabled exception
// matching ip, or -1.
func matchTLSException(list []tlsProfileException, ip string) int {
	for i, e := range list {
		if !e.Enabled || !e.Profile.IsValid() {
			continue
		}
		if m, known := accessController.SourceMatches(e.Source, ip); known && m {
			return i
		}
	}
	return -1
}

func chooseTLSProfile(ip string) (dynamicproxy.TLSProfile, string, bool) {
	list := currentTLSExceptions()
	if len(list) == 0 {
		return "", "", false
	}
	i := matchTLSException(list, ip)
	if i < 0 {
		return "", "", false
	}
	return list[i].Profile, tlsExceptionName(i, list[i]), true
}

func tlsExceptionObjectUsers(id string) []string {
	for _, e := range currentTLSExceptions() {
		if e.Source.Type == access.SourceObject && e.Source.Value == id {
			return []string{"the TLS profile exceptions (System > Settings)"}
		}
	}
	return nil
}

// HandleTLSProfileExceptions: GET returns the list; POST exceptions=<JSON
// array> saves it.
func HandleTLSProfileExceptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		type row struct {
			tlsProfileException
			SourceName string `json:"sourceName"`
		}
		out := []row{}
		for _, e := range currentTLSExceptions() {
			out = append(out, row{e, describeTLSSource(e.Source)})
		}
		js, _ := json.Marshal(out)
		utils.SendJSONResponse(w, string(js))
	case http.MethodPost:
		raw, err := utils.PostPara(r, "exceptions")
		if err != nil {
			utils.SendErrorResponse(w, "no exceptions given")
			return
		}
		in := []tlsProfileException{}
		if err := json.Unmarshal([]byte(raw), &in); err != nil || in == nil {
			utils.SendErrorResponse(w, "the exceptions are not a valid JSON list")
			return
		}
		list, err := normalizeTLSExceptions(in, accessController.NormalizeSource)
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
		if err := sysdb.Write("settings", tlsExceptionsKey, list); err != nil {
			utils.SendErrorResponse(w, "cannot save the exceptions")
			return
		}
		tlsExceptions.Store(&list)
		SystemWideLogger.PrintAndLog("tls", fmt.Sprintf("TLS profile exceptions saved (%d)", len(list)), nil)
		utils.SendOK(w)
	default:
		http.Error(w, "405 - Method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleTLSProfileTest returns the profile for ip=<address>.
func HandleTLSProfileTest(w http.ResponseWriter, r *http.Request) {
	raw, err := utils.GetPara(r, "ip")
	if err != nil {
		utils.SendErrorResponse(w, "no address given")
		return
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		utils.SendErrorResponse(w, "enter an IP address, such as 10.0.0.5")
		return
	}
	ip := addr.Unmap().WithZone("").String()
	list := currentTLSExceptions()
	i := matchTLSException(list, ip)
	resp := map[string]any{"ip": ip, "profile": readTLSProfile(), "exception": i, "name": ""}
	if i >= 0 {
		resp["profile"], resp["name"] = list[i].Profile, tlsExceptionName(i, list[i])
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}
