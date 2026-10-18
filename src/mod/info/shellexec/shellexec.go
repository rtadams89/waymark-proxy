package shellexec

import (
	"os/exec"
	"sync"
)

/*
	Shell Exec
	author: tobychui

	Resolve a usable POSIX shell for commands that need pipes or
	other shell features. bash is not always available (e.g. a stock
	FreeBSD install), so this falls back to sh when bash cannot be found.

	All commands passed to Command must be POSIX sh compatible.
*/

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
		//Nothing found, let exec report the error on run
		resolvedPath = "sh"
	})
	return resolvedPath
}

// Command returns an exec.Cmd that runs script with the resolved shell
func Command(script string) *exec.Cmd {
	return exec.Command(Shell(), "-c", script)
}
