package main

// Trusted proxy descriptions are stored as plain text (the UI escapes them
// when it shows them). Before 2.19.10 they were stored HTML-encoded.

import (
	"html"
	"strings"
	"unicode"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/database"
)

const (
	maxTrustedProxyDescLen   = 200
	trustedProxyDescPlainKey = "trusted_proxy_desc_plain" // set once the descriptions are plain text
)

// cleanTrustedProxyDesc trims a description, drops control characters and
// limits its length.
func cleanTrustedProxyDesc(desc string) string {
	desc = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(desc))
	if r := []rune(desc); len(r) > maxTrustedProxyDescLen {
		desc = string(r[:maxTrustedProxyDescLen])
	}
	return desc
}

// migrateTrustedProxyDescriptions decodes the HTML-encoded descriptions of
// earlier versions, once. The marker is in custom_fork, which HA sync
// copies together with conf/trusted_proxies.json.
func migrateTrustedProxyDescriptions(c *access.Controller, db *database.Database) {
	if c == nil || db == nil {
		return
	}
	if !db.TableExists(customForkTable) {
		db.NewTable(customForkTable)
	}
	if db.KeyExists(customForkTable, trustedProxyDescPlainKey) {
		return
	}
	changed := false
	for _, p := range c.ListTrustedProxies() {
		plain := cleanTrustedProxyDesc(html.UnescapeString(p.Desc))
		if plain != p.Desc {
			c.RemoveTrustedProxy(p.IP)
			c.AddTrustedProxy(p.IP, plain)
			changed = true
		}
	}
	if changed {
		if err := c.SaveTrustedProxies(); err != nil {
			SystemWideLogger.PrintAndLog("access", "Cannot save the trusted proxy descriptions as plain text", err)
			return
		}
		SystemWideLogger.Println("Trusted proxy descriptions are now stored as plain text")
	}
	db.Write(customForkTable, trustedProxyDescPlainKey, true)
}
