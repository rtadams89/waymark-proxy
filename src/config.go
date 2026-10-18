package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"imuslab.com/zoraxy/mod/dynamicproxy"
	"imuslab.com/zoraxy/mod/dynamicproxy/loadbalance"
	"imuslab.com/zoraxy/mod/info/logger"
	"imuslab.com/zoraxy/mod/tlscert"
	"imuslab.com/zoraxy/mod/utils"
)

/*
	Reverse Proxy Configs

	The following section handle
	the reverse proxy configs
*/

/*
Load Reverse Proxy Config from file and append it to current runtime proxy router
*/
func LoadReverseProxyConfig(configFilepath string) error {
	//Load the config file from disk
	endpointConfig, err := os.ReadFile(configFilepath)
	if err != nil {
		return err
	}

	//Parse it into dynamic proxy endpoint
	thisConfigEndpoint := dynamicproxy.GetDefaultProxyEndpoint()
	err = json.Unmarshal(endpointConfig, &thisConfigEndpoint)
	if err != nil {
		return err
	}

	//Make sure the tags are not nil
	if thisConfigEndpoint.Tags == nil {
		thisConfigEndpoint.Tags = []string{}
	}

	//Make sure the TLS options are not nil
	if thisConfigEndpoint.TlsOptions == nil {
		thisConfigEndpoint.TlsOptions = tlscert.GetDefaultHostSpecificTlsBehavior()
	}

	//Matching domain not set. Assume root
	if thisConfigEndpoint.RootOrMatchingDomain == "" {
		thisConfigEndpoint.RootOrMatchingDomain = "/"
	}

	switch thisConfigEndpoint.ProxyType {
	case dynamicproxy.ProxyTypeRoot:
		//This is a root config file
		rootProxyEndpoint, err := dynamicProxyRouter.PrepareProxyRoute(&thisConfigEndpoint)
		if err != nil {
			return err
		}

		dynamicProxyRouter.SetProxyRouteAsRoot(rootProxyEndpoint)

	case dynamicproxy.ProxyTypeHost:
		//This is a host config file
		readyProxyEndpoint, err := dynamicProxyRouter.PrepareProxyRoute(&thisConfigEndpoint)
		if err != nil {
			return err
		}

		dynamicProxyRouter.AddProxyRouteToRuntime(readyProxyEndpoint)
	default:
		return errors.New("not supported proxy type")
	}

	SystemWideLogger.PrintAndLog("proxy-config", thisConfigEndpoint.RootOrMatchingDomain+" -> "+loadbalance.GetUpstreamsAsString(thisConfigEndpoint.ActiveOrigins)+" routing rule loaded", nil)
	return nil
}

func filterProxyConfigFilename(filename string) string {
	//Filter out wildcard characters
	filename = strings.ReplaceAll(filename, "*", "(ST)")
	filename = strings.ReplaceAll(filename, "?", "(QM)")
	filename = strings.ReplaceAll(filename, "[", "(OB)")
	filename = strings.ReplaceAll(filename, "]", "(CB)")
	filename = strings.ReplaceAll(filename, "#", "(HT)")
	return filepath.ToSlash(filename)
}

// validProxyConfigName reports whether a host name can be used as a proxy
// rule's config file name: no path separators or "..", so the file always
// stays in the proxy rule folder.
func validProxyConfigName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\\x00") && !strings.Contains(name, "..")
}

// hostRulePattern is what a proxy rule's host name or alias may contain:
// letters, digits, . - _, the wildcards * and ?, and : [ ] for ports and
// IPv6 addresses.
var hostRulePattern = regexp.MustCompile(`^[A-Za-z0-9._*?:\[\]-]{1,253}$`)

// validHostRuleName reports whether name can be a proxy rule's host name or
// alias. Besides keeping file names in the proxy rule folder, this keeps
// markup out of names the web UI shows.
func validHostRuleName(name string) bool {
	return hostRulePattern.MatchString(name) && validProxyConfigName(name)
}

func SaveReverseProxyConfig(endpoint *dynamicproxy.ProxyEndpoint) error {
	//Get filename for saving
	filename := filepath.Join(CONF_HTTP_PROXY, "root.config")
	if endpoint.ProxyType != dynamicproxy.ProxyTypeRoot {
		if !validProxyConfigName(endpoint.RootOrMatchingDomain) {
			return errors.New("invalid host name for a proxy rule: " + endpoint.RootOrMatchingDomain)
		}
		filename = filepath.Join(CONF_HTTP_PROXY, endpoint.RootOrMatchingDomain+".config")
	}

	filename = filterProxyConfigFilename(filename)

	//Save config to file
	js, err := json.MarshalIndent(endpoint, "", " ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, js, 0775)
}

func RemoveReverseProxyConfig(endpoint string) error {
	filename := filepath.Join(CONF_HTTP_PROXY, "root.config")
	if endpoint != "/" {
		if !validProxyConfigName(endpoint) {
			return errors.New("invalid proxy rule name")
		}
		filename = filepath.Join(CONF_HTTP_PROXY, endpoint+".config")
	}

	filename = filterProxyConfigFilename(filename)

	if !utils.FileExists(filename) {
		return errors.New("target endpoint not exists")
	}
	return os.Remove(filename)
}

// Get the default root config that point to the internal static web server
// this will be used if root config is not found (new deployment / missing root.config file)
func GetDefaultRootConfig() (*dynamicproxy.ProxyEndpoint, error) {
	//Get the default proxy endpoint
	rootProxyEndpointConfig := dynamicproxy.GetDefaultProxyEndpoint()
	rootProxyEndpointConfig.ProxyType = dynamicproxy.ProxyTypeRoot
	rootProxyEndpointConfig.RootOrMatchingDomain = "/"
	rootProxyEndpointConfig.ActiveOrigins = []*loadbalance.Upstream{
		{
			OriginIpOrDomain:    "127.0.0.1:" + staticWebServer.GetListeningPort(),
			RequireTLS:          false,
			SkipCertValidations: false,
			Weight:              0,
		},
	}
	rootProxyEndpointConfig.DefaultSiteOption = dynamicproxy.DefaultSite_InternalStaticWebServer
	rootProxyEndpointConfig.DefaultSiteValue = ""

	//Default settings
	rootProxyEndpoint, err := dynamicProxyRouter.PrepareProxyRoute(&rootProxyEndpointConfig)
	if err != nil {
		return nil, err
	}

	return rootProxyEndpoint, nil
}

/*
	Importer and Exporter of Zoraxy proxy config
*/

func ExportConfigAsZip(w http.ResponseWriter, r *http.Request) {
	//Include the system database in backup snapshot?
	includeSysDB, _ := utils.GetBool(r, "includeDB")

	// Specify the folder path to be zipped
	if !utils.FileExists(CONF_FOLDER) {
		SystemWideLogger.PrintAndLog("Backup", "Configuration folder not found", nil)
		return
	}
	folderPath := CONF_FOLDER

	// Set the Content-Type header to indicate it's a zip file
	w.Header().Set("Content-Type", "application/zip")
	// Set the Content-Disposition header to specify the file name, add timestamp to the filename
	w.Header().Set("Content-Disposition", "attachment; filename=\"waymark-config-"+time.Now().Format("2006-01-02-15-04-05")+".zip\"")

	// Create a zip writer
	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	// Walk through the folder and add files to the zip
	err := filepath.Walk(folderPath, func(filePath string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if folderPath == filePath {
			//Skip root folder
			return nil
		}

		// Create a new file in the zip
		if !utils.IsDir(filePath) {
			zipFile, err := zipWriter.Create(filePath)
			if err != nil {
				return err
			}

			// Open the file on disk
			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()

			// Copy the file contents to the zip file
			_, err = io.Copy(zipFile, file)
			if err != nil {
				return err
			}
		}

		return nil
	})

	if includeSysDB {
		//Also zip in the sysdb
		zipFile, err := zipWriter.Create("sys.db")
		if err != nil {
			SystemWideLogger.PrintAndLog("Backup", "Unable to zip sysdb", err)
			return
		}

		// Open the file on disk
		file, err := os.Open(*path_database)
		if err != nil {
			SystemWideLogger.PrintAndLog("Backup", "Unable to open sysdb", err)
			return
		}
		defer file.Close()

		// Copy the file contents to the zip file
		_, err = io.Copy(zipFile, file)
		if err != nil {
			SystemWideLogger.Println(err)
			return
		}

	}

	if err != nil {
		// Handle the error and send an HTTP response with the error message
		http.Error(w, fmt.Sprintf("Failed to zip folder: %v", err), http.StatusInternalServerError)
		return
	}
}

func ImportConfigFromZip(w http.ResponseWriter, r *http.Request) {
	// Check if the request is a POST with a file upload
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusBadRequest)
		return
	}

	// Max file size limit (10 MB in this example)
	r.ParseMultipartForm(10 << 20)

	// Get the uploaded file
	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to retrieve uploaded file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	if filepath.Ext(handler.Filename) != ".zip" {
		http.Error(w, "Upload file is not a zip file", http.StatusInternalServerError)
		return
	}
	// Create the target directory to unzip the files
	targetDir := CONF_FOLDER
	if utils.FileExists(targetDir) {
		//Backup the old config to old
		//backupPath := filepath.Dir(*path_conf) + filepath.Base(*path_conf) + ".old_" + strconv.Itoa(int(time.Now().Unix()))
		//os.Rename(*path_conf, backupPath)
		os.Rename(CONF_FOLDER, CONF_FOLDER+".old_"+strconv.Itoa(int(time.Now().Unix())))
	}

	err = os.MkdirAll(targetDir, os.ModePerm)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create target directory: %v", err), http.StatusInternalServerError)
		return
	}

	// Open the zip file
	zipReader, err := zip.NewReader(file, handler.Size)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open zip file: %v", err), http.StatusInternalServerError)
		return
	}

	restoreDatabase := false

	// Extract each file from the zip archive
	for _, zipFile := range zipReader.File {
		// Open the file in the zip archive
		rc, err := zipFile.Open()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to open file in zip: %v", err), http.StatusInternalServerError)
			return
		}
		defer rc.Close()

		// Sanitize the file name to prevent path traversal (zip-slip)
		cleanedName := filepath.Clean(filepath.FromSlash(zipFile.Name))
		cleanedNameSlash := filepath.ToSlash(cleanedName)
		fmt.Println("Restoring: " + cleanedNameSlash)
		if cleanedNameSlash == "sys.db" {
			//Sysdb replacement. Close the database and restore
			sysdb.Close()
			restoreDatabase = true
		} else if !strings.HasPrefix(cleanedNameSlash, "conf/") {
			//Malformed zip file.
			http.Error(w, "Invalid zip file structure or version too old", http.StatusInternalServerError)
			return
		}

		// Resolve to absolute path and verify it stays within the target directory
		absTargetDir, _ := filepath.Abs(targetDir)
		absFilePath, _ := filepath.Abs(cleanedName)
		if cleanedNameSlash != "sys.db" && !strings.HasPrefix(absFilePath, absTargetDir+string(os.PathSeparator)) {
			http.Error(w, "Invalid file path in zip", http.StatusBadRequest)
			return
		}

		//Check if parent dir exists
		if !utils.FileExists(filepath.Dir(cleanedName)) {
			os.MkdirAll(filepath.Dir(cleanedName), 0775)
		}

		//Create the file
		newFile, err := os.Create(cleanedName)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to create file: %v", err), http.StatusInternalServerError)
			return
		}
		defer newFile.Close()

		// Copy the file contents from the zip to the new file
		_, err = io.Copy(newFile, rc)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to extract file from zip: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Send a success response
	w.WriteHeader(http.StatusOK)
	SystemWideLogger.Println("Configuration restored")
	fmt.Fprintln(w, "Configuration restored")

	if restoreDatabase {
		go func() {
			SystemWideLogger.Println("Database altered. Restarting in 3 seconds...")
			time.Sleep(3 * time.Second)
			os.Exit(0)
		}()

	}

}

func handleLoggerConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		logger.HandleGetLogConfig(CONF_LOG_CONFIG)(w, r)
	case http.MethodPost:
		logger.HandleUpdateLogConfig(CONF_LOG_CONFIG, SystemWideLogger)(w, r)
	default:
		utils.SendErrorResponse(w, "Method not allowed")
	}
}
