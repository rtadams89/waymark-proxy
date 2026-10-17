package hacluster

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed deploy/*
var deployFiles embed.FS

// DeployFileNames lists the installer files in the order they are listed.
var DeployFileNames = []string{"install.sh", "zoraxy-ha-apply", "check", "notify", "zoraxy-ha-apply.path", "zoraxy-ha-apply.service"}

// ExportInstaller writes the root installer files into dir (created if
// needed) so they can be reviewed and run with: sudo bash <dir>/install.sh
func ExportInstaller(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, name := range DeployFileNames {
		b, err := deployFiles.ReadFile("deploy/" + name)
		if err != nil {
			return err
		}
		// Always LF, whatever the checkout's line endings were
		b = []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
		perm := os.FileMode(0644)
		if !strings.Contains(name, ".") || strings.HasSuffix(name, ".sh") {
			perm = 0755
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, perm); err != nil {
			return err
		}
	}
	return nil
}

// DeployFile returns one installer file (LF line endings), for display.
func DeployFile(name string) (string, error) {
	b, err := deployFiles.ReadFile("deploy/" + name)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}
