package main

/*
	conftables_custom.go (custom fork): settings kept as files in the config
	folder instead of sys.db.

	Each table below is stored in its own JSON file (see
	mod/database/filetable_custom.go). The handlers that read and write
	these settings are unchanged. On the first start of a version with this
	file, the rows are copied from sys.db into the files (see confCopiedKey);
	the same happens after restoring a backup from an earlier version.

	The HA settings are in conf/ha.json (ha_custom.go).

	sys.db keeps secrets and accounts (admin accounts, Waymark Auth users,
	ACME accounts, DNS credentials, the sticky-session key) and per-node
	state (statistics, counters, sessions).
*/

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
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
	// SSO (2.16.0)
	{"oauth2", "sso/oauth2.json", 0600},             // OAuth2 / OIDC provider (holds the client secret)
	{"auth_sso_forward", "sso/forward.json", 0640},  // forward auth server
	{"zorxauth", "sso/zorxauth/options.json", 0640}, // Waymark Auth gateway options
}

// keys that moved from custom_fork into the system table
var systemTableKeys = []string{geoIPEnabledKey, assetCacheLimitKey}

// confCopiedKey (in custom_fork, per node) records which tables and keys
// have been copied from sys.db into files. Each is copied once: after that
// the file is what counts, and the copy left in sys.db is only for an
// earlier version put back by a roll back. A sys.db without the record
// (restored from an older backup) is copied again.
const confCopiedKey = "conf_copied"

// moveTablesToFiles routes the tables above to their files. Run it right
// after sys.db is opened, before anything reads them.
func moveTablesToFiles() {
	copied := map[string]int64{}
	if sysdb.TableExists(migrationTable) && sysdb.KeyExists(migrationTable, confCopiedKey) {
		sysdb.Read(migrationTable, confCopiedKey, &copied)
	}
	now := time.Now().Unix()
	for _, t := range confFileTables {
		path := filepath.Join(CONF_FOLDER, t.File)
		if err := sysdb.UseFileTable(t.Table, path, t.Perm, copied[t.Table] == 0); err != nil {
			log.Fatalf("Cannot load %s: %v (fix or remove the file and start Waymark again)", path, err)
		}
		if copied[t.Table] == 0 {
			copied[t.Table] = now
		}
	}
	haKey := migrationTable + "/" + haConfigKey
	if err := moveHAConfigToFile(copied[haKey] == 0); err != nil {
		log.Fatalf("Cannot load the HA settings: %v (fix or remove %s and start Waymark again)", err, haConfigPath())
	}
	if copied[haKey] == 0 {
		copied[haKey] = now
	}
	for _, k := range systemTableKeys {
		ck := migrationTable + "/" + k
		if copied[ck] != 0 {
			continue
		}
		copied[ck] = now
		if !sysdb.TableExists(migrationTable) || !sysdb.KeyExists(migrationTable, k) {
			continue
		}
		var raw json.RawMessage
		if err := sysdb.Read(migrationTable, k, &raw); err != nil || len(raw) == 0 {
			continue
		}
		if err := sysdb.Write(systemTable, k, raw); err != nil {
			log.Printf("Cannot copy setting %s to %s: %v", k, systemTable, err)
		}
	}
	if !sysdb.TableExists(migrationTable) {
		sysdb.NewTable(migrationTable)
	}
	if err := sysdb.Write(migrationTable, confCopiedKey, copied); err != nil {
		log.Printf("Cannot record the settings copied to files: %v", err)
	}
}
