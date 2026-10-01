//go:build windows

package application

import (
	"os"
	"os/exec"
)

// execProcess on Windows: there is no execve, so run the command as a child and
// hand back its *exec.ExitError for the caller to turn into an exit code. The
// PID-1 signal problem that motivates execve on Linux does not apply here.
func execProcess(args, env []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	return cmd.Run()
}
