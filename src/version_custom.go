package main

// Product name and version of Waymark Proxy (semantic versioning:
// MAJOR.MINOR.PATCH). Release tags are v<PRODUCT_VERSION>.
//
// SYSTEM_VERSION (def.go) is the Zoraxy release Waymark was forked from
// (UPSTREAM_BASE), reported to plugins as the host version.

const (
	PRODUCT_NAME       = "Waymark Proxy"
	PRODUCT_SHORT_NAME = "Waymark"
	PRODUCT_VERSION    = "2.19.10"
	PRODUCT_REPO       = "https://github.com/rtadams89/waymark-proxy"

	// UPSTREAM_BASE is the Zoraxy release Waymark was forked from.
	UPSTREAM_BASE = SYSTEM_VERSION

	// DISPLAY_VERSION is shown in the web UI and by `waymark -version`, and
	// compared between HA nodes (they must run the same build).
	DISPLAY_VERSION = PRODUCT_VERSION
)
