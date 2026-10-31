package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/acme"
	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/utils"
)

// Routing of ACME HTTP-01 challenges and certificate renewal.

// acmeTokenRegex matches ACME HTTP-01 challenge tokens (base64url).
var acmeTokenRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func getRandomPort(minPort int) int {
	return rand.Intn(65535-minPort) + minPort
}

func initACME() *acme.ACMEHandler {
	SystemWideLogger.Println("Starting ACME handler")
	rand.Seed(time.Now().UnixNano())
	port := getRandomPort(30000)

	for acme.IsPortInUse(port) {
		port = getRandomPort(30000)
	}

	keyFileMode := parseACMEFileMode(*acmeKeyFileMode, 0o600, "acmekeymode")
	publicFileMode := parseACMEFileMode(*acmePublicFileMode, 0o644, "acmepublicmode")

	return acme.NewACME(strconv.Itoa(port), sysdb, SystemWideLogger, *acmeTestMode, keyFileMode, publicFileMode)
}

func parseACMEFileMode(raw string, fallback os.FileMode, flagName string) os.FileMode {
	parsed, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		SystemWideLogger.Println("Invalid " + flagName + " value " + raw + ", using fallback " + fmt.Sprintf("%#o", fallback))
		return fallback
	}

	return os.FileMode(parsed)
}

// Routes /.well-known/acme-challenge/ requests to the ACME handler.
func acmeRegisterSpecialRoutingRule() {
	SystemWideLogger.Println("Assigned temporary port:" + acmeHandler.Getport())

	err := dynamicProxyRouter.AddRoutingRules(&dynamicproxy.RoutingRule{
		ID: "acme-autorenew",
		MatchRule: func(r *http.Request) bool {
			parsedURL, err := url.ParseRequestURI(r.RequestURI)
			if err != nil {
				return false
			}
			return strings.HasPrefix(parsedURL.Path, "/.well-known/acme-challenge/")
		},
		RoutingHandler: func(w http.ResponseWriter, r *http.Request) {
			parsedURL, err := url.ParseRequestURI(r.RequestURI)
			if err != nil {
				http.Error(w, "Invalid request URI", http.StatusBadRequest)
				return
			}

			// Rejects challenge requests with a query string.
			if parsedURL.RawQuery != "" {
				http.Error(w, "Query string not allowed in ACME challenge", http.StatusBadRequest)
				return
			}

			prefix := "/.well-known/acme-challenge/"
			token := strings.TrimPrefix(parsedURL.Path, prefix)
			if len(token) == 0 || len(token) > 255 || !acmeTokenRegex.MatchString(token) {
				http.Error(w, "Invalid ACME challenge token", http.StatusBadRequest)
				return
			}

			// Builds the internal URL from the validated path only.
			internalURL := "http://localhost:" + acmeHandler.Getport() + parsedURL.Path

			req, err := http.NewRequest(http.MethodGet, internalURL, nil)
			if err != nil {
				fmt.Printf("client: could not create request: %s\n", err)
				return
			}
			req.Host = r.Host

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Printf("client: error making http request: %s\n", err)
				return
			}
			defer res.Body.Close()

			resBody, err := io.ReadAll(res.Body)
			if err != nil {
				fmt.Printf("error reading: %s\n", err)
				return
			}
			w.Write(resBody)
		},
		Enabled:                true,
		UseSystemAccessControl: false,
	})
	if err != nil {
		SystemWideLogger.PrintAndLog("acme", "Unable register temp port for DNS resolver", err)
	}
}

// Enables port 80 and the HTTPS redirect if needed, renews the certificate,
// then restores those settings.
func AcmeCheckAndHandleRenewCertificate(w http.ResponseWriter, r *http.Request) {
	requireRestoreHttpsRedirect := false
	requireRestorePort80 := false
	progressID, _ := utils.PostPara(r, "progressId")
	acme.AddProgress(progressID, "info", "Certificate request started")
	dnsPara, _ := utils.PostBool(r, "dns")
	if !dnsPara && !dynamicProxyRouter.Running {
		acme.AddProgress(progressID, "warning", "The HTTP proxy is not running, so nothing answers the HTTP-01 challenge on port 80. Start the proxy (save its port in Listeners) or use a DNS challenge.")
	}
	if !dnsPara {
		switch dynamicProxyRouter.Option.Port {
		case 443:
			if !dynamicProxyRouter.Option.ListenOnPort80 {
				SystemWideLogger.PrintAndLog("acme", "Temporarily enabling port 80 listener to handle ACME request ", nil)
				acme.AddProgress(progressID, "info", "Turning on the port 80 listener for the HTTP-01 challenge (restored afterwards)")
				dynamicProxyRouter.UpdatePort80ListenerState(true)
				requireRestorePort80 = true
				time.Sleep(2 * time.Second)
			}

			if !dynamicProxyRouter.Option.ForceHttpsRedirect {
				SystemWideLogger.Println("Temporary enabling HTTP to HTTPS redirect for ACME certificate renew requests")
				acme.AddProgress(progressID, "info", "Turning on the HTTP to HTTPS redirect for the HTTP-01 challenge (restored afterwards)")
				dynamicProxyRouter.UpdateHttpToHttpsRedirectSetting(true)
				requireRestoreHttpsRedirect = true
			}

		case 80:

		default:
			msg := "ACME renew only support web server listening on port 80 (http) or 443 (https)"
			acme.AddProgress(progressID, "error", msg)
			acme.FinishProgress(progressID, errors.New(msg))
			utils.SendErrorResponse(w, msg)
			return
		}
	}

	time.Sleep(2 * time.Second)

	acmeHandler.HandleRenewCertificate(w, r)

	tlsCertManager.UpdateLoadedCertList()

	if requireRestorePort80 {
		SystemWideLogger.PrintAndLog("acme", "Restoring previous port 80 listener settings", nil)
		dynamicProxyRouter.UpdatePort80ListenerState(false)
	}
	if requireRestoreHttpsRedirect {
		SystemWideLogger.PrintAndLog("acme", "Restoring HTTP to HTTPS redirect settings", nil)
		dynamicProxyRouter.UpdateHttpToHttpsRedirectSetting(false)
	}
}

// HandleACMEPreferredCA gets (GET) or sets (POST ca) the preferred CA.
func HandleACMEPreferredCA(w http.ResponseWriter, r *http.Request) {
	type PreferredCA struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		SkipTLS bool   `json:"skipTLS"`
	}

	ca, err := utils.PostPara(r, "set")
	if err != nil {
		prefCA := "Let's Encrypt"
		prefCAURL := ""
		skipTLS := false
		sysdb.Read("acmepref", "prefca", &prefCA)
		sysdb.Read("acmepref", "prefcaurl", &prefCAURL)
		sysdb.Read("acmepref", "skipTLS", &skipTLS)
		js, _ := json.Marshal(PreferredCA{
			Name:    prefCA,
			URL:     prefCAURL,
			SkipTLS: skipTLS,
		})
		utils.SendJSONResponse(w, string(js))
	} else {
		isSupported := acme.IsSupportedCA(ca, *acmeTestMode)

		if !isSupported && ca != "custom" {
			utils.SendErrorResponse(w, "The specified ACME CA is not supported")
			return
		}

		if ca == "custom" {
			customCAURL, err := utils.PostPara(r, "customURL")
			customCAURL = strings.TrimSpace(customCAURL)
			parsedURL, err2 := url.Parse(customCAURL)
			skipTLS, _ := utils.PostBool(r, "skipTLS")

			if err != nil || err2 != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
				utils.SendErrorResponse(w, "Invalid custom CA URL provided")
				return
			}
			sysdb.Write("acmepref", "prefcaurl", customCAURL)
			SystemWideLogger.Println("Updating prefered ACME CA URL to " + customCAURL)
			sysdb.Write("acmepref", "skipTLS", skipTLS)
			SystemWideLogger.Println("Updating prefered skipTLS to " + fmt.Sprintf("%t", skipTLS))
		}
		sysdb.Write("acmepref", "prefca", ca)
		SystemWideLogger.Println("Updating prefered ACME CA to " + ca)
		utils.SendOK(w)
	}
}
