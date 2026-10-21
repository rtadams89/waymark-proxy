package main

/*
	stickykey_custom.go (custom fork)

	The key that signs sticky session cookies (load balancing). Zoraxy used
	the host UUID, which is also sent to every upstream in the
	X-Forwarded-Server header, so an upstream could forge the cookies.
	The key is random, kept in the database (custom_fork table), and copied
	to standby nodes by HA configuration sync, so after a failover the new
	active node still accepts the cookies. Each node keeps its own host UUID.
*/

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"

	"imuslab.com/zoraxy/mod/database"
)

const stickySessionKeyName = "sticky_session_key"

var stickySessionKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// stickySessionKey returns the saved key, creating one on first use.
func stickySessionKey() string {
	return loadOrCreateStickyKey(sysdb)
}

func loadOrCreateStickyKey(db *database.Database) string {
	if db == nil {
		return ""
	}
	if !db.TableExists(customForkTable) {
		db.NewTable(customForkTable)
	}
	var key string
	if db.KeyExists(customForkTable, stickySessionKeyName) {
		if err := db.Read(customForkTable, stickySessionKeyName, &key); err == nil && stickySessionKeyPattern.MatchString(key) {
			return key
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "" // the load balancer then uses a random key for this run
	}
	key = hex.EncodeToString(b)
	if err := db.Write(customForkTable, stickySessionKeyName, key); err != nil && SystemWideLogger != nil {
		SystemWideLogger.PrintAndLog("load-balancer", "Could not save the sticky session key; sticky sessions reset when Waymark restarts", err)
	}
	return key
}
