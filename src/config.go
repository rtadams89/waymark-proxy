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

// LoadReverseProxyConfig loads a proxy rule config file into the router.
func LoadReverseProxyConfig(configFilepath string) error {
	endpointConfig, err := os.ReadFile(configFilepath)
	if err != nil {
		return err
	}

	thisConfigEndpoint := dynamicproxy.GetDefaultProxyEndpoint()
	err = json.Unmarshal(endpointConfig, &thisConfigEndpoint)
	if err != nil {
		return err
	}

	if thisConfigEndpoint.Tags == nil {
		thisConfigEndpoint.Tags = []string{}
	}

	if thisConfigEndpoint.TlsOptions == nil {
		thisConfigEndpoint.TlsOptions = tlscert.GetDefaultHostSpecificTlsBehavior()
	}

	if thisConfigEndpoint.RootOrMatchingDomain == "" {
		thisConfigEndpoint.RootOrMatchingDomain = "/"
	}

	switch thisConfigEndpoint.ProxyType {
	case dynamicproxy.ProxyTypeRoot:
		rootProxyEndpoint, err := dynamicProxyRouter.PrepareProxyRoute(&thisConfigEndpoint)
		if err != nil {
			return err
		}

		dynamicProxyRouter.SetProxyRouteAsRoot(rootProxyEndpoint)

	case dynamicproxy.ProxyTypeHost:
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
	filename = strings.ReplaceAll(filename, "*", "(ST)")
	filename = strings.ReplaceAll(filename, "?", "(QM)")
	filename = strings.ReplaceAll(filename, "[", "(OB)")
	filename = strings.ReplaceAll(filename, "]", "(CB)")
	filename = strings.ReplaceAll(filename, "#", "(HT)")
	return filepath.ToSlash(filename)
}

// validProxyConfigName reports whether a host name is safe as a config file
// name (no path separators, NUL or "..").
func validProxyConfigName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\\x00") && !strings.Contains(name, "..")
}

// hostRulePattern matches allowed proxy rule host names and aliases.
var hostRulePattern = regexp.MustCompile(`^[A-Za-z0-9._*?:\[\]-]{1,253}$`)

// validHostRuleName reports whether name can be a proxy rule's host name or
// alias.
func validHostRuleName(name string) bool {
	return hostRulePattern.MatchString(name) && validProxyConfigName(name)
}

func SaveReverseProxyConfig(endpoint *dynamicproxy.ProxyEndpoint) error {
	filename := filepath.Join(CONF_HTTP_PROXY, "root.config")
	if endpoint.ProxyType != dynamicproxy.ProxyTypeRoot {
		if !validProxyConfigName(endpoint.RootOrMatchingDomain) {
			return errors.New("invalid host name for a proxy rule: " + endpoint.RootOrMatchingDomain)
		}
		filename = filepath.Join(CONF_HTTP_PROXY, endpoint.RootOrMatchingDomain+".config")
	}

	filename = filterProxyConfigFilename(filename)

	js, err := json.MarshalIndent(endpoint, "", " ")
	if err != nil {
		return err
	}

	return utils.WriteFileAtomic(filename, js, 0644)
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

// GetDefaultRootConfig returns the default root rule, pointing to the
// internal static web server.
func GetDefaultRootConfig() (*dynamicproxy.ProxyEndpoint, error) {
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

	rootProxyEndpoint, err := dynamicProxyRouter.PrepareProxyRoute(&rootProxyEndpointConfig)
	if err != nil {
		return nil, err
	}

	return rootProxyEndpoint, nil
}

/* Config export and import */

func ExportConfigAsZip(w http.ResponseWriter, r *http.Request) {
	includeSysDB, _ := utils.GetBool(r, "includeDB")

	if !utils.FileExists(CONF_FOLDER) {
		SystemWideLogger.PrintAndLog("backup", "Configuration folder not found", nil)
		return
	}
	folderPath := CONF_FOLDER

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"waymark-config-"+time.Now().Format("2006-01-02-15-04-05")+".zip\"")

	zipWriter := zip.NewWriter(w)
	defer zipWriter.Close()

	err := filepath.Walk(folderPath, func(filePath string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if folderPath == filePath {
			return nil
		}

		if !utils.IsDir(filePath) {
			zipFile, err := zipWriter.Create(filePath)
			if err != nil {
				return err
			}

			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(zipFile, file)
			if err != nil {
				return err
			}
		}

		return nil
	})

	if includeSysDB {
		zipFile, err := zipWriter.Create("sys.db")
		if err != nil {
			SystemWideLogger.PrintAndLog("backup", "Unable to zip sysdb", err)
			return
		}

		file, err := os.Open(*path_database)
		if err != nil {
			SystemWideLogger.PrintAndLog("backup", "Unable to open sysdb", err)
			return
		}
		defer file.Close()

		_, err = io.Copy(zipFile, file)
		if err != nil {
			SystemWideLogger.Println(err)
			return
		}

	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to zip folder: %v", err), http.StatusInternalServerError)
		return
	}
}

func ImportConfigFromZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusBadRequest)
		return
	}

	// Max upload size 10 MB
	r.ParseMultipartForm(10 << 20)

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
	zipReader, err := zip.NewReader(file, handler.Size)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open zip file: %v", err), http.StatusInternalServerError)
		return
	}

	// Checks every entry and works out where it goes before anything changes
	type restoreEntry struct {
		file *zip.File
		dest string
	}
	entries := []restoreEntry{}
	restoreDatabase := false
	for _, zipFile := range zipReader.File {
		if strings.Contains(zipFile.Name, "..") {
			http.Error(w, "Invalid file path in zip", http.StatusBadRequest)
			return
		}
		name := filepath.ToSlash(filepath.Clean(filepath.FromSlash(zipFile.Name)))
		if name == "sys.db" {
			entries = append(entries, restoreEntry{zipFile, *path_database})
			restoreDatabase = true
			continue
		}
		rel, ok := strings.CutPrefix(name, "conf/")
		if !ok || rel == "" || filepath.IsAbs(rel) {
			http.Error(w, "Invalid zip file structure or version too old", http.StatusInternalServerError)
			return
		}
		if zipFile.FileInfo().IsDir() {
			continue
		}
		entries = append(entries, restoreEntry{zipFile, filepath.Join(CONF_FOLDER, filepath.FromSlash(rel))})
	}

	if utils.FileExists(CONF_FOLDER) {
		// Move the old config folder aside
		os.Rename(CONF_FOLDER, CONF_FOLDER+".old_"+strconv.Itoa(int(time.Now().Unix())))
	}
	if err := os.MkdirAll(CONF_FOLDER, os.ModePerm); err != nil {
		http.Error(w, fmt.Sprintf("Failed to create target directory: %v", err), http.StatusInternalServerError)
		return
	}
	if restoreDatabase {
		sysdb.Close()
	}

	for _, e := range entries {
		SystemWideLogger.Println("Restoring: " + filepath.ToSlash(e.file.Name))
		if err := restoreZipEntry(e.file, e.dest); err != nil {
			http.Error(w, fmt.Sprintf("Failed to extract file from zip: %v", err), http.StatusInternalServerError)
			return
		}
	}

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

// restoreZipEntry writes the content of f to dest, creating its folder.
func restoreZipEntry(f *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0775); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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
