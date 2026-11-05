package main

import (
	"html"
	"strings"
	"unicode"

	"github.com/rtadams89/waymark-proxy/mod/access"
	"github.com/rtadams89/waymark-proxy/mod/database"
)

const (
	maxTrustedProxyDescLen   = 200
	trustedProxyDescPlainKey = "trusted_proxy_desc_plain"
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

// migrateTrustedProxyDescriptions HTML-decodes the descriptions once.
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
