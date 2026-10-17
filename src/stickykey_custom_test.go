package main

import (
	"path/filepath"
	"testing"

	"imuslab.com/zoraxy/mod/database"
)

func TestStickySessionKey(t *testing.T) {
	db, err := database.NewDatabase(filepath.Join(t.TempDir(), "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	k1 := loadOrCreateStickyKey(db)
	if !stickySessionKeyPattern.MatchString(k1) {
		t.Fatalf("key %q is not 64 hex characters", k1)
	}
	if k2 := loadOrCreateStickyKey(db); k2 != k1 {
		t.Fatal("key changed between starts")
	}
	if k1 == nodeUUID {
		t.Fatal("key must not be the host UUID")
	}
	// A key copied from the active node (HA sync) is used as is
	shared := "ab" + k1[2:]
	db.Write(migrationTable, stickySessionKeyName, shared)
	if got := loadOrCreateStickyKey(db); got != shared {
		t.Fatalf("synced key not used: %q", got)
	}
	// A damaged value is replaced
	db.Write(migrationTable, stickySessionKeyName, "short")
	if got := loadOrCreateStickyKey(db); !stickySessionKeyPattern.MatchString(got) || got == shared {
		t.Fatalf("damaged key not replaced: %q", got)
	}
}
