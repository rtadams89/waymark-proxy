package main

import (
	"regexp"
	"strings"
	"testing"

	"imuslab.com/zoraxy/mod/update"
)

func TestDisplayVersionFormat(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+-custom\.[1-9]\d*$`).MatchString(DISPLAY_VERSION) {
		t.Errorf("DISPLAY_VERSION %q does not match <x.y.z>-custom.<n>", DISPLAY_VERSION)
	}
	if !strings.HasPrefix(DISPLAY_VERSION, SYSTEM_VERSION+"-custom.") {
		t.Errorf("DISPLAY_VERSION %q must start with the upstream base %q", DISPLAY_VERSION, SYSTEM_VERSION)
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
