package main

import (
	"path/filepath"
	"testing"

	"github.com/rtadams89/waymark-proxy/mod/database"
	"github.com/rtadams89/waymark-proxy/mod/dynamicproxy"
	"github.com/rtadams89/waymark-proxy/mod/info/logger"
)

func TestProfileFromMinTLSVersion(t *testing.T) {
	cases := map[string]dynamicproxy.TLSProfile{
		"1.0": dynamicproxy.TLSProfileLegacy,
		"1.1": dynamicproxy.TLSProfileLegacy,
		"1.2": dynamicproxy.TLSProfileCompatible,
		"1.3": dynamicproxy.TLSProfileModern,
		"":    dynamicproxy.TLSProfileCompatible,
		"x":   dynamicproxy.TLSProfileCompatible,
	}
	for v, want := range cases {
		if got := profileFromMinTLSVersion(v); got != want {
			t.Errorf("%q: got %s, want %s", v, got, want)
		}
	}
	for _, p := range []dynamicproxy.TLSProfile{dynamicproxy.TLSProfileModern, dynamicproxy.TLSProfileCompatible, dynamicproxy.TLSProfileLegacy} {
		if got := profileFromMinTLSVersion(minTLSVersionForProfile(p)); got != p {
			t.Errorf("%s round-trips to %s", p, got)
		}
	}
	if minTLSVersionForProfile(dynamicproxy.TLSProfileIntermediate) != "1.2" {
		t.Error("intermediate should write 1.2 for older nodes")
	}
}

func TestReadAndSaveTLSProfile(t *testing.T) {
	dir := t.TempDir()
	db, err := database.NewDatabase(filepath.Join(dir, "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UseFileTable("settings", filepath.Join(dir, "listener.json"), 0640); err != nil {
		t.Fatal(err)
	}
	if SystemWideLogger == nil {
		SystemWideLogger, _ = logger.NewFmtLogger()
	}
	oldDB, oldFresh := sysdb, freshInstall
	sysdb = db
	defer func() { sysdb, freshInstall = oldDB, oldFresh }()

	// Existing install without a saved profile
	freshInstall = false
	initTLSProfileForNewInstall()
	if db.KeyExists("settings", tlsProfileKey) {
		t.Fatal("a profile was saved on an existing install")
	}
	if p := readTLSProfile(); p != dynamicproxy.TLSProfileCompatible {
		t.Fatalf("existing install: got %s", p)
	}
	db.Write("settings", legacyMinTLSKey, "1.3")
	if p := readTLSProfile(); p != dynamicproxy.TLSProfileModern {
		t.Fatalf("minimum 1.3: got %s", p)
	}

	// New install
	db.Delete("settings", legacyMinTLSKey)
	freshInstall = true
	initTLSProfileForNewInstall()
	if p := readTLSProfile(); p != dynamicproxy.TLSProfileIntermediate {
		t.Fatalf("new install: got %s", p)
	}

	if err := saveTLSProfile(dynamicproxy.TLSProfileLegacy); err != nil {
		t.Fatal(err)
	}
	var old string
	db.Read("settings", legacyMinTLSKey, &old)
	if p := readTLSProfile(); p != dynamicproxy.TLSProfileLegacy || old != "1.0" {
		t.Fatalf("after saving legacy: %s, minTLSVersion %q", p, old)
	}

	db.Write("settings", tlsProfileKey, "bogus")
	if p := readTLSProfile(); p != dynamicproxy.TLSProfileIntermediate {
		t.Fatalf("invalid saved profile: got %s", p)
	}
}
