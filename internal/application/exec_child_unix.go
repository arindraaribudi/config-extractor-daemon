//go:build !windows

package application

import (
	"fmt"
	"os/exec"
	"syscall"
)

// syscallExec is overridable in tests: the real one never returns on success,
// so a unit test could not otherwise observe what it was called with.
var syscallExec = syscall.Exec

// execProcess replaces the current process with args[0]. The path is resolved
// with exec.LookPath against the daemon's own PATH, which is what exec.Command
// did before this became an execve.
func execProcess(args, env []string) error {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	// The real syscall.Exec only returns when the exec failed.
	if err := syscallExec(path, args, env); err != nil {
		return fmt.Errorf("exec %s: %w", path, err)
	}
	return nil
}
