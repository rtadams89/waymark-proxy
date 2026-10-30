package acmewizard

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/utils"
)

// Checks of the ACME setup for the ACME wizard.

func HandleGuidedStepCheck(w http.ResponseWriter, r *http.Request) {
	stepNoStr, err := utils.GetPara(r, "step")
	if err != nil {
		utils.SendErrorResponse(w, "invalid step number given")
		return
	}

	stepNo, err := strconv.Atoi(stepNoStr)
	if err != nil {
		utils.SendErrorResponse(w, "invalid step number given")
		return
	}

	if stepNo == 1 {
		isListening, err := isLocalhostListening()
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}

		js, _ := json.Marshal(isListening)
		utils.SendJSONResponse(w, string(js))
	} else if stepNo == 2 {
		publicIp, err := getPublicIPAddress()
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}

		publicIp = strings.TrimSpace(publicIp)

		httpServerReachable := isHTTPServerAvailable(publicIp)

		js, _ := json.Marshal(httpServerReachable)
		utils.SendJSONResponse(w, string(js))
	} else if stepNo == 3 {
		domain, err := utils.GetPara(r, "domain")
		if err != nil {
			utils.SendErrorResponse(w, "domain cannot be empty")
			return
		}

		domain = strings.TrimSpace(domain)
		if u, err := url.Parse("//" + domain); err != nil || u.Host != domain || u.Port() != "" || u.User != nil || domain == "" {
			utils.SendErrorResponse(w, "invalid domain")
			return
		}

		reachable := isDomainReachable(domain)
		if !reachable {
			utils.SendErrorResponse(w, "domain is not reachable")
			return
		}

		httpServerReachable := isHTTPServerAvailable(domain)
		js, _ := json.Marshal(httpServerReachable)
		utils.SendJSONResponse(w, string(js))
	} else if stepNo == 10 {
		publicIp, err := getPublicIPAddress()
		if err != nil {
			utils.SendErrorResponse(w, err.Error())
			return
		}
		js, _ := json.Marshal(publicIp)
		utils.SendJSONResponse(w, string(js))
	} else {
		utils.SendErrorResponse(w, "invalid step number")
	}
}

func isLocalhostListening() (isListening bool, err error) {
	timeout := 2 * time.Second
	isListening = false
	conn, err := net.DialTimeout("tcp", "localhost:80", timeout)
	if err == nil {
		isListening = true
		conn.Close()
	}

	conn, err = net.DialTimeout("tcp", "localhost:443", timeout)
	if err == nil {
		isListening = true
		conn.Close()
	}

	if isListening {
		return true, nil
	}

	return isListening, err
}

func getPublicIPAddress() (string, error) {
	resp, err := http.Get("http://checkip.amazonaws.com/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(ip)), nil
}

func isHTTPServerAvailable(ipAddress string) bool {
	client := http.Client{
		Timeout: 5 * time.Second,
	}

	urls := []string{
		(&url.URL{Scheme: "http", Host: net.JoinHostPort(ipAddress, "80")}).String(),
		(&url.URL{Scheme: "https", Host: net.JoinHostPort(ipAddress, "443")}).String(),
	}

	for _, target := range urls {
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			fmt.Println(err, target)
			continue
		}

		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return true
		}
	}

	return false
}

func isDomainReachable(domain string) bool {
	_, err := net.LookupHost(domain)
	if err != nil {
		return false
	}
	return true
}
