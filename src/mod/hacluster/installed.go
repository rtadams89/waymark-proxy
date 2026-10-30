package hacluster

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultHelperPath is where install.sh puts the root helper.
const DefaultHelperPath = "/usr/local/sbin/waymark-ha-apply"

// install.sh rewrites the helper's OWNER default to the service user
var ownerLine = regexp.MustCompile(`(?m)^OWNER="\$\{HA_OWNER:-[A-Za-z0-9._-]*\}"$`)

func normalizeDeployFile(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return ownerLine.ReplaceAllString(s, `OWNER="${HA_OWNER:-}"`)
}

// OutdatedInstalledFiles returns the names of the installed helper and
// keepalived scripts that differ from the ones built in. ok is false when the
// installed files could not be read.
func OutdatedInstalledFiles(helperPath, scriptsDir string) (outdated []string, ok bool) {
	outdated = []string{}
	for name, path := range map[string]string{
		"waymark-ha-apply": helperPath,
		"check":            filepath.Join(scriptsDir, "check"),
		"notify":           filepath.Join(scriptsDir, "notify"),
	} {
		installed, err := os.ReadFile(path)
		if err != nil {
			return nil, false
		}
		built, err := deployFiles.ReadFile("deploy/" + name)
		if err != nil {
			return nil, false
		}
		if normalizeDeployFile(installed) != normalizeDeployFile(built) {
			outdated = append(outdated, name)
		}
	}
	return outdated, true
}
