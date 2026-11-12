package main

// Product name and version (MAJOR.MINOR.PATCH). Release tags are
// v<PRODUCT_VERSION>.

const (
	PRODUCT_NAME       = "Waymark Proxy"
	PRODUCT_SHORT_NAME = "Waymark"
	PRODUCT_VERSION    = "3.5.0"
	PRODUCT_REPO       = "https://github.com/rtadams89/waymark-proxy"

	// DISPLAY_VERSION is shown in the web UI and by -version, and compared
	// between HA nodes.
	DISPLAY_VERSION = PRODUCT_VERSION
)
