package application

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// ExecChildUseCase runs a child process with the given env pairs merged
// into the inherited environment. Child stdin/stdout/stderr are passed
// through unchanged.
//
// Exit-code propagation: when the child exits non-zero, returns the
// *exec.ExitError — the caller (cmd entry point) is responsible for
// translating that into os.Exit(childExitCode).
type ExecChildUseCase struct {
	Args []string
}

func (uc ExecChildUseCase) Run(pairs []domain.EnvPair) error {
	if len(uc.Args) == 0 {
		return fmt.Errorf("exec: no command provided")
	}
	cmd := exec.Command(uc.Args[0], uc.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	strs := make([]string, len(pairs))
	for i, p := range pairs {
		strs[i] = string(p)
	}
	cmd.Env = append(os.Environ(), strs...)
	return cmd.Run()
}
