package main

/*
	conftables_custom.go (custom fork): settings kept as files in the config
	folder instead of sys.db.

	Each table below is stored in its own JSON file (see
	mod/database/filetable_custom.go). The handlers that read and write
	these settings use the database API as before; the routing is done in
	mod/database. The HA settings are in conf/ha.json (ha_custom.go).

	sys.db keeps secrets and accounts (admin accounts, Waymark Auth users,
	ACME accounts and EAB credentials, DNS credentials, the sticky-session
	key) and per-node state (statistics, counters, sessions).
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
	{"settings", "listener.json", 0640},             // listening port, TLS, HTTP/2, QUIC, timeouts, port 80, development mode
	{systemTable, "system.json", 0640},              // Geo-IP lookups, asset cache memory limit
	{"acmepref", "acme_prefs.json", 0640},           // preferred certificate authority
	{"redirect", "redirect_options.json", 0640},     // redirection rule matching options
	{"webserv", "webserv.json", 0600},               // static web server and WebDAV (holds the WebDAV password hash)
	{customUITable, "ui.json", 0640},                // status page panels
	{"plugins", "plugins.json", 0640},               // plugins turned on and store sources (this node only)
	{"oauth2", "sso/oauth2.json", 0600},             // OAuth2 / OIDC provider (holds the client secret)
	{"auth_sso_forward", "sso/forward.json", 0640},  // forward auth server
	{"zorxauth", "sso/zorxauth/options.json", 0640}, // Waymark Auth gateway options
}

// useConfFileTables routes the tables above to their files and checks that
// conf/ha.json can be read. Run it right after sys.db is opened, before
// anything reads them.
func useConfFileTables() {
	for _, t := range confFileTables {
		path := filepath.Join(CONF_FOLDER, t.File)
		if err := sysdb.UseFileTable(t.Table, path, t.Perm); err != nil {
			log.Fatalf("Cannot load %s: %v (fix or remove the file and start Waymark again)", path, err)
		}
	}
	if _, err := readHAConfigFile(); err != nil {
		log.Fatalf("Cannot load the HA settings: %v (fix or remove %s and start Waymark again)", err, haConfigPath())
	}
	cleanupBefore2171()
}

/*
cleanupBefore2171 (2.17.1) tidies up after the moves of 2.15.0 to 2.17.0
once Waymark no longer needs to support going back to those versions:

  - ACME account keys, which were stored with the ACME preferences and so
    ended up in conf/acme_prefs.json, go back to sys.db (the "acme" table,
    with the EAB credentials).
  - The copies left in sys.db of the settings that moved to conf/, the
    "authelia" and "authentik" tables of old Zoraxy versions, and the
    custom_fork keys only used to move settings are deleted.
  - conf/version (Zoraxy's config migration marker) and an empty
    conf/streamproxy folder are removed.

Each step finds nothing to do once it has run. It can be removed once
every node has run 2.17.1.
*/
func cleanupBefore2171() {
	// ACME account keys out of conf/acme_prefs.json
	prefs := map[string]bool{"prefca": true, "prefcaurl": true, "skipTLS": true}
	if rows, err := sysdb.ListTable("acmepref"); err == nil {
		for _, kv := range rows {
			key := string(kv[0])
			if prefs[key] {
				continue
			}
			if !sysdb.TableExists("acme") {
				sysdb.NewTable("acme")
			}
			if err := sysdb.Write("acme", key, json.RawMessage(kv[1])); err != nil {
				SystemWideLogger.PrintAndLog("database", "Cannot move ACME account "+key+" to sys.db", err)
				continue
			}
			sysdb.Delete("acmepref", key)
			SystemWideLogger.PrintAndLog("database", "Moved ACME account "+key+" from "+sysdb.FileTablePath("acmepref")+" to sys.db", nil)
		}
	}

	// Copies of moved settings and old tables in sys.db
	if sysdb.Backend != nil {
		stale := []string{"authelia", "authentik"}
		for _, t := range confFileTables {
			stale = append(stale, t.Table)
		}
		for _, t := range stale {
			if sysdb.Backend.TableExists(t) {
				if err := sysdb.Backend.DropTable(t); err == nil {
					SystemWideLogger.PrintAndLog("database", "Removed the unused table "+t+" from sys.db", nil)
				}
			}
		}
	}
	if sysdb.TableExists(customForkTable) {
		for _, k := range []string{"ha_cluster", "geoip_enabled", "asset_cache_max_mb", "uptime_to_healthcheck_migrated", "conf_copied"} {
			if sysdb.KeyExists(customForkTable, k) {
				sysdb.Delete(customForkTable, k)
			}
		}
	}

	// Files of the config migrations
	os.Remove(filepath.Join(CONF_FOLDER, "version"))
	os.Remove(filepath.Join(CONF_FOLDER, "streamproxy")) // only if empty
}
