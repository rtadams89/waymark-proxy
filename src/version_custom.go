package main

/*
	version_custom.go (custom fork)

	Version numbering for this fork:  <upstream base>-custom.<revision>
	e.g. 3.3.5-custom.1, 3.3.5-custom.2, ... then 3.3.6-custom.1 after
	rebasing onto upstream 3.3.6.

	- SYSTEM_VERSION (def.go) stays the upstream base version. Do not add a
	  suffix there: Zoraxy converts it to an integer ("3.3.5" -> 335) to run
	  config migrations (mod/update), and plugins and mDNS also read it.
	- FORK_REVISION is bumped with every change to this fork that ships, and
	  reset to 1 when SYSTEM_VERSION changes after an upstream rebase.
	- DISPLAY_VERSION is what people see: the web UI, `zoraxy -version`, and
	  the release tag (v<DISPLAY_VERSION>), which CI checks matches this file.

	Keep CHANGELOG-custom.md (repo root) in step with FORK_REVISION.
*/

const (
	FORK_REVISION   = "14"
	DISPLAY_VERSION = SYSTEM_VERSION + "-custom." + FORK_REVISION
)
