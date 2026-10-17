package main

import (
	"regexp"
	"testing"

	"imuslab.com/zoraxy/mod/update"
)

func TestProductVersionIsSemVer(t *testing.T) {
	if !regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`).MatchString(PRODUCT_VERSION) {
		t.Errorf("PRODUCT_VERSION %q is not MAJOR.MINOR.PATCH", PRODUCT_VERSION)
	}
	if DISPLAY_VERSION != PRODUCT_VERSION {
		t.Errorf("DISPLAY_VERSION %q must equal PRODUCT_VERSION %q", DISPLAY_VERSION, PRODUCT_VERSION)
	}
}

// Config migrations parse SYSTEM_VERSION as an integer. A suffix there would
// parse as 0 and make the next upgrade re-run every migration.
func TestSystemVersionStaysMigrationSafe(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(SYSTEM_VERSION) {
		t.Fatalf("SYSTEM_VERSION %q must stay a plain upstream x.y.z version", SYSTEM_VERSION)
	}
	if update.GetVersionIntFromVersionNumber(SYSTEM_VERSION) <= 0 {
		t.Fatalf("SYSTEM_VERSION %q does not convert to a migration version number", SYSTEM_VERSION)
	}
}
