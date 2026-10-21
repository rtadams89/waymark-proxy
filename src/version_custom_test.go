package main

import (
	"regexp"
	"testing"
)

func TestProductVersionIsSemVer(t *testing.T) {
	if !regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`).MatchString(PRODUCT_VERSION) {
		t.Errorf("PRODUCT_VERSION %q is not MAJOR.MINOR.PATCH", PRODUCT_VERSION)
	}
	if DISPLAY_VERSION != PRODUCT_VERSION {
		t.Errorf("DISPLAY_VERSION %q must equal PRODUCT_VERSION %q", DISPLAY_VERSION, PRODUCT_VERSION)
	}
}
