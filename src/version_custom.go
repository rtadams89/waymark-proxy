package main

/*
	version_custom.go: product name and version of Waymark Proxy.

	Waymark Proxy uses semantic versioning: MAJOR.MINOR.PATCH.
	- PATCH (1.0.1): bug and security fixes only.
	- MINOR (1.1.0): new features that keep existing configuration working.
	- MAJOR (2.0.0): changes that need action when upgrading (renamed paths,
	  removed features, configuration that has to be migrated).
	Release tags are v<PRODUCT_VERSION> (v1.0.0); CI refuses a tag that does
	not match the version built into the binary. Keep CHANGELOG.md in step.

	SYSTEM_VERSION (def.go) is the Zoraxy release Waymark is built on
	(UPSTREAM_BASE). Leave it as a plain x.y.z: Zoraxy converts it to a number
	("3.3.5" -> 335) to run its config migrations, and plugins read it as the
	host version. It only changes when a Zoraxy release is merged in.
*/

const (
	PRODUCT_NAME       = "Waymark Proxy"
	PRODUCT_SHORT_NAME = "Waymark"
	PRODUCT_VERSION    = "2.3.0"
	PRODUCT_REPO       = "https://github.com/rtadams89/waymark-proxy"

	// UPSTREAM_BASE is the Zoraxy release this build is based on.
	UPSTREAM_BASE = SYSTEM_VERSION

	// DISPLAY_VERSION is shown in the web UI and by `waymark -version`, and
	// compared between HA nodes (they must run the same build).
	DISPLAY_VERSION = PRODUCT_VERSION
)
