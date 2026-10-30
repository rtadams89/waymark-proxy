package shellexec

import (
	"os/exec"
	"sync"
)

// Resolves a POSIX shell (bash, else sh) for commands that need shell
// features. Commands passed to Command must be POSIX sh compatible.

var (
	resolveOnce  sync.Once
	resolvedPath string
)

// Shell returns the path of the shell used to execute commands
func Shell() string {
	resolveOnce.Do(func() {
		candidates := []string{"bash", "sh"}
		for _, c := range candidates {
			if p, err := exec.LookPath(c); err == nil {
				resolvedPath = p
				return
			}
		}
		// Nothing found
		resolvedPath = "sh"
	})
	return resolvedPath
}

// Command returns an exec.Cmd that runs script with the resolved shell
func Command(script string) *exec.Cmd {
	return exec.Command(Shell(), "-c", script)
}
