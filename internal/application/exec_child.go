package application

import (
	"fmt"
	"os"
	"strings"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// ExecChildUseCase runs a command with the given env pairs merged into the
// inherited environment (injected pairs win over inherited ones).
//
// On Linux and macOS this REPLACES the current process (execve). That is
// deliberate: as a container entrypoint the daemon is PID 1, and if it forked
// the command as a child, SIGTERM from the kubelet would hit the daemon, Go
// would exit on it, and the kernel would then SIGKILL the child -- so the app
// never gets to drain in-flight requests. After execve the app IS the process:
// it receives signals directly, reaps its own children, and its exit status is
// the container's. stdin/stdout/stderr are inherited untouched.
//
// On a successful exec this never returns. It returns an error only when the
// command could not be started (not found, not executable, bad environment).
//
// Windows has no execve, so exec_child_windows.go falls back to running the
// command as a child and propagating its exit code.
type ExecChildUseCase struct {
	Args []string
}

func (uc ExecChildUseCase) Run(pairs []domain.EnvPair) error {
	if len(uc.Args) == 0 {
		return fmt.Errorf("exec: no command provided")
	}
	return execProcess(uc.Args, mergeEnv(os.Environ(), pairs))
}

// mergeEnv returns base with pairs applied on top: a pair replaces an inherited
// entry of the same name in place, and a new name is appended. execve passes the
// environment through as-is, and a duplicated name is read differently by
// different runtimes (glibc takes the first, others take the last), so
// duplicates must never reach the child.
func mergeEnv(base []string, pairs []domain.EnvPair) []string {
	out := make([]string, 0, len(base)+len(pairs))
	index := make(map[string]int, len(base)+len(pairs))

	add := func(entry string) {
		name, _, hasEq := strings.Cut(entry, "=")
		if !hasEq {
			out = append(out, entry)
			return
		}
		if i, seen := index[name]; seen {
			out[i] = entry
			return
		}
		index[name] = len(out)
		out = append(out, entry)
	}

	for _, e := range base {
		add(e)
	}
	for _, p := range pairs {
		add(string(p))
	}
	return out
}
