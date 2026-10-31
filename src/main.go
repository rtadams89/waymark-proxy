package main

/*
Zoraxy - A general purpose HTTP reverse proxy and forwarding tool
Author: tobychui
License: AGPLv3

--------------------------------------------

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, version 3 of the License or any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

*/

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"imuslab.com/zoraxy/mod/auth"
	csrf "imuslab.com/zoraxy/mod/csrfcompat"
	"imuslab.com/zoraxy/mod/geodb"
	"imuslab.com/zoraxy/mod/utils"
)

// SetupCloseHandler runs the shutdown sequence on SIGTERM.
func SetupCloseHandler() {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-c
		ShutdownSeq()
		os.Exit(0)
	}()
}

func main() {
	flag.Parse()

	TMP_FOLDER = *path_tmp
	CONF_FOLDER = *path_conf
	CONF_HTTP_PROXY = CONF_FOLDER + "/proxy"
	CONF_L4_PROXY = CONF_FOLDER + "/l4proxy"
	CONF_CERT_STORE = CONF_FOLDER + "/certs"
	CONF_REDIRECTION = CONF_FOLDER + "/redirect"
	CONF_ACCESS_RULE = CONF_FOLDER + "/access"
	CONF_ACL = CONF_FOLDER + "/acl"
	CONF_PATH_RULE = CONF_FOLDER + "/rules/pathrules"
	CONF_PLUGIN_GROUPS = CONF_FOLDER + "/plugin_groups.json"
	CONF_GEODB_PATH = CONF_FOLDER + "/geodb"
	CONF_LOG_CONFIG = CONF_FOLDER + "/log_conf.json"
	ACME_AUTORENEW_CONFIG_PATH = CONF_FOLDER + "/acme_conf.json"
	CONF_TRUSTED_PROXIES = CONF_FOLDER + "/trusted_proxies.json"

	/* Maintaince Function Modes */
	if *showver {
		fmt.Println(PRODUCT_NAME + " " + DISPLAY_VERSION + " (based on Zoraxy " + UPSTREAM_BASE + ")")
		os.Exit(0)
	}
	if *geoDbUpdate {
		geodb.DownloadGeoDBUpdate(CONF_GEODB_PATH)
		os.Exit(0)
	}
	if handleHAMaintenanceFlags() {
		os.Exit(0)
	}
	updateStartupCheck()
	if *resetAdminRuleFlag {
		adminRuleResetAndExit()
	}
	if *reset_account {
		auth.ResetAccount(databaseBackend, path_database)
		os.Exit(0)
	}

	if !utils.ValidateListeningAddress(*webUIPort) {
		fmt.Println("Malformed -port (listening address) paramter. Do you mean -port=:" + *webUIPort + "?")
		os.Exit(0)
	}

	SetupCloseHandler()

	uuidRecord := *path_uuid
	if !utils.FileExists(uuidRecord) {
		newSystemUUID := uuid.New().String()
		utils.WriteFileAtomic(uuidRecord, []byte(newSystemUUID), 0644)
	}
	uuidBytes, err := os.ReadFile(uuidRecord)
	if err != nil {
		SystemWideLogger.PrintAndLog("system", "Unable to read system uuid from file system", nil)
		panic(err)
	}
	nodeUUID = string(uuidBytes)

	webminPanelMux = http.NewServeMux()
	pluginAPIMux := http.NewServeMux()
	csrfMiddleware = csrf.Protect(
		[]byte(nodeUUID),
		csrf.CookieName(CSRF_COOKIENAME),
		csrf.Secure(false),
		csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteLaxMode),
	)
	csrfMiddleware = haWithReadOnly(csrfMiddleware)
	csrfMiddleware = adminGuardWrap(csrfMiddleware)

	startupSequence()
	initSelfUpdate()
	initTrafficCapture()

	requireAuth = !(*noauth)
	initAPIs(webminPanelMux)
	initRestAPI(pluginAPIMux)

	entryMux := http.NewServeMux()
	entryMux.Handle("/plugin/", pluginAPIMux)
	entryMux.Handle("/", csrfMiddleware(webminPanelMux)) // requires a CSRF token

	go func() {
		ReverseProxyInit()
	}()

	// Waits for ReverseProxyInit to create dynamicProxyRouter
	<-dynamicProxyRouterReady

	finalSequence()

	if strings.HasPrefix(*webUIPort, ":") {
		SystemWideLogger.Println(SYSTEM_NAME + " started. Visit control panel at http://localhost" + *webUIPort)
	} else {
		SystemWideLogger.Println(SYSTEM_NAME + " started. Visit control panel at http://" + *webUIPort)
	}

	err = http.ListenAndServe(*webUIPort, adminSecurityHeaders(entryMux))

	if err != nil {
		log.Fatal(err)
	}
}
