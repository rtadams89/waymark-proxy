package main

/*
	conftables_custom.go (custom fork): settings kept as files in the config
	folder instead of sys.db.

	Each table below is stored in its own JSON file (see
	mod/database/filetable_custom.go). The handlers that read and write
	these settings are unchanged. On the first start of a version with this
	file, the rows are moved out of sys.db into the files; the same happens
	after restoring a backup whose sys.db still has them.

	sys.db keeps secrets and accounts (admin accounts, Waymark Auth users,
	ACME accounts, DNS credentials, the sticky-session key) and per-node
	state (statistics, counters, sessions).
*/

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

const systemTable = "system" // Geo-IP lookups on/off, asset cache memory limit

var confFileTables = []struct {
	Table string
	File  string
	Perm  os.FileMode
}{
	{"settings", "listener.json", 0640},         // listening port, TLS, HTTP/2, QUIC, timeouts, port 80, development mode
	{systemTable, "system.json", 0640},          // Geo-IP lookups, asset cache memory limit
	{"acmepref", "acme_prefs.json", 0640},       // preferred certificate authority
	{"redirect", "redirect_options.json", 0640}, // redirection rule matching options
	{"webserv", "webserv.json", 0600},           // static web server and WebDAV (holds the WebDAV password hash)
	{customUITable, "ui.json", 0640},            // status page panels
	{"plugins", "plugins.json", 0640},           // plugins turned on and store sources (this node only)
}

// keys that moved from custom_fork into the system table
var systemTableKeys = []string{geoIPEnabledKey, assetCacheLimitKey}

// moveTablesToFiles routes the tables above to their files. Run it right
// after sys.db is opened, before anything reads them.
func moveTablesToFiles() {
	for _, t := range confFileTables {
		path := filepath.Join(CONF_FOLDER, t.File)
		if err := sysdb.UseFileTable(t.Table, path, t.Perm); err != nil {
			log.Fatalf("Cannot load %s: %v (fix or remove the file and start Waymark again)", path, err)
		}
	}
	if !sysdb.TableExists(migrationTable) {
		return
	}
	for _, k := range systemTableKeys {
		if !sysdb.KeyExists(migrationTable, k) {
			continue
		}
		var raw json.RawMessage
		if err := sysdb.Read(migrationTable, k, &raw); err != nil || len(raw) == 0 {
			continue
		}
		if err := sysdb.Write(systemTable, k, raw); err != nil {
			log.Printf("Cannot move setting %s to %s: %v", k, systemTable, err)
			continue
		}
		sysdb.Delete(migrationTable, k)
	}
}
