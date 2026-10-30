package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	zoraxyPlugin "imuslab.com/zoraxy/mod/plugins/zoraxy_plugin"
)

// checkSupportHotRebuild checks if a Makefile or main.go exists in the given directory
func checkSupportHotRebuild(dir string) bool {
	_, errMakefile := os.Stat(filepath.Join(dir, "Makefile"))
	_, errMainGo := os.Stat(filepath.Join(dir, "main.go"))
	return !os.IsNotExist(errMakefile) || !os.IsNotExist(errMainGo)
}

// LoadPluginSpec loads a plugin specification by running the plugin entry
// point with the -introspect flag.
func (m *Manager) LoadPluginSpec(pluginPath string) (*Plugin, error) {
	pluginEntryPoint, err := m.GetPluginEntryPoint(pluginPath)
	if err != nil {
		return nil, err
	}

	pluginSpec, err := m.GetPluginSpec(pluginEntryPoint)
	if err != nil {
		return nil, err
	}

	err = validatePluginSpec(pluginSpec)
	if err != nil {
		return nil, err
	}

	return &Plugin{
		RootDir:           pluginPath,
		Spec:              pluginSpec,
		Enabled:           false,
		SupportHotRebuild: checkSupportHotRebuild(pluginPath),
	}, nil
}

// GetPluginSpec returns the plugin specification from the entry point
func (m *Manager) GetPluginSpec(entryPoint string) (*zoraxyPlugin.IntroSpect, error) {
	pluginSpec := zoraxyPlugin.IntroSpect{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, entryPoint, "-introspect")
	output, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("plugin introspect timed out")
	}
	if err != nil {
		return nil, err
	}

	// Parse the JSON output into pluginSpec
	err = json.Unmarshal(output, &pluginSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal plugin spec: %v", err)
	}

	return &pluginSpec, nil
}
