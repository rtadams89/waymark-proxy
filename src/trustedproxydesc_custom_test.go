package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/access"
	"imuslab.com/zoraxy/mod/database"
	"imuslab.com/zoraxy/mod/info/logger"
)

func TestCleanTrustedProxyDesc(t *testing.T) {
	if got := cleanTrustedProxyDesc("  Office \"A\" & <b>B</b>\x00\n "); got != `Office "A" & <b>B</b>` {
		t.Fatalf("got %q", got)
	}
	if got := cleanTrustedProxyDesc(strings.Repeat("é", 300)); len([]rune(got)) != maxTrustedProxyDescLen {
		t.Fatalf("not limited: %d", len([]rune(got)))
	}
}

func TestMigrateTrustedProxyDescriptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "trusted_proxies.json")
	os.WriteFile(file, []byte(`[{"IP":"10.0.0.1","Desc":"Lab &#34;1&#34; &amp; more"},{"IP":"10.0.0.2","Desc":"Cloudflare"}]`), 0644)
	db, err := database.NewDatabase(filepath.Join(dir, "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lg, _ := logger.NewFmtLogger()
	if SystemWideLogger == nil {
		SystemWideLogger = lg
	}
	c, err := access.NewAccessController(&access.Options{Logger: *lg, Database: db, ConfigFolder: filepath.Join(dir, "acl"), TrustedProxiesFile: file})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	migrateTrustedProxyDescriptions(c, db)
	descs := map[string]string{}
	for _, p := range c.ListTrustedProxies() {
		descs[p.IP] = p.Desc
	}
	if descs["10.0.0.1"] != `Lab "1" & more` || descs["10.0.0.2"] != "Cloudflare" {
		t.Fatalf("not decoded: %v", descs)
	}
	saved, _ := os.ReadFile(file)
	if strings.Contains(string(saved), "&#34;") {
		t.Fatalf("file not rewritten: %s", saved)
	}

	// Runs once: a description typed later with "&amp;" in it stays as typed
	c.RemoveTrustedProxy("10.0.0.2")
	c.AddTrustedProxy("10.0.0.2", "A &amp; B")
	migrateTrustedProxyDescriptions(c, db)
	for _, p := range c.ListTrustedProxies() {
		if p.IP == "10.0.0.2" && p.Desc != "A &amp; B" {
			t.Fatalf("migration ran twice: %q", p.Desc)
		}
	}
}
