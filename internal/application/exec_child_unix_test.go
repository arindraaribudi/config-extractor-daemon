//go:build !windows

package application

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// --- execProcess with a fake syscall.Exec: what it is called with ------------

func TestExecProcess_PassesResolvedPathArgsAndEnv(t *testing.T) {
	var gotPath string
	var gotArgs, gotEnv []string
	old := syscallExec
	syscallExec = func(path string, args, env []string) error {
		gotPath, gotArgs, gotEnv = path, args, env
		return nil
	}
	t.Cleanup(func() { syscallExec = old })

	err := execProcess([]string{"sh", "-c", "true"}, []string{"A=1"})
	if err != nil {
		t.Fatalf("execProcess: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/sh") || !strings.HasPrefix(gotPath, "/") {
		t.Errorf("path = %q, want an absolute path ending in /sh (resolved from PATH)", gotPath)
	}
	if want := []string{"sh", "-c", "true"}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v (argv[0] stays the name as given)", gotArgs, want)
	}
	if want := []string{"A=1"}; !reflect.DeepEqual(gotEnv, want) {
		t.Errorf("env = %v, want %v", gotEnv, want)
	}
}

func TestExecProcess_CommandNotFoundNeverCallsExec(t *testing.T) {
	called := false
	old := syscallExec
	syscallExec = func(string, []string, []string) error { called = true; return nil }
	t.Cleanup(func() { syscallExec = old })

	err := execProcess([]string{"definitely-not-a-real-command-xyz"}, nil)
	if err == nil {
		t.Fatal("expected an error for a command that is not on PATH")
	}
	if called {
		t.Error("syscall.Exec must not be attempted when the command cannot be resolved")
	}
}

func TestExecProcess_ExecFailureIsReturned(t *testing.T) {
	old := syscallExec
	syscallExec = func(string, []string, []string) error { return syscall.EACCES }
	t.Cleanup(func() { syscallExec = old })

	err := execProcess([]string{"sh"}, nil)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("err = %v, want it to wrap EACCES", err)
	}
}

func TestExecChildUseCase_MergesInheritedEnvAndPairs(t *testing.T) {
	t.Setenv("INHERITED_VAR", "from-parent")
	t.Setenv("OVERRIDDEN_VAR", "from-parent")

	var gotEnv []string
	old := syscallExec
	syscallExec = func(_ string, _, env []string) error { gotEnv = env; return nil }
	t.Cleanup(func() { syscallExec = old })

	err := (ExecChildUseCase{Args: []string{"sh"}}).Run([]domain.EnvPair{"OVERRIDDEN_VAR=injected", "NEW_VAR=new"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen := map[string]string{}
	for _, e := range gotEnv {
		k, v, _ := strings.Cut(e, "=")
		if _, dup := seen[k]; dup {
			t.Errorf("%s appears twice in the environment handed to the child", k)
		}
		seen[k] = v
	}
	for k, want := range map[string]string{"INHERITED_VAR": "from-parent", "OVERRIDDEN_VAR": "injected", "NEW_VAR": "new"} {
		if seen[k] != want {
			t.Errorf("child env %s = %q, want %q", k, seen[k], want)
		}
	}
}

// --- real exec, in a throwaway process ---------------------------------------
//
// A real execve replaces the process that calls it, so the test cannot do it in
// itself. Instead the test binary re-runs itself with EXEC_HELPER set; that
// process calls ExecChildUseCase.Run and becomes the command under test, while
// the parent observes it from outside. This is the same pattern os/exec's own
// tests use.

const (
	helperEnv        = "EXEC_CHILD_HELPER_ARGS"
	helperOpenFDsEnv = "EXEC_CHILD_HELPER_OPEN_FDS"
)

func TestExecHelperProcess(t *testing.T) {
	raw := os.Getenv(helperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	args := strings.Split(raw, "\x1f")
	var held []any
	if os.Getenv(helperOpenFDsEnv) != "" {
		// What the daemon holds when it execs: after fetching the configuration, a cloud client
		// leaves open files and sockets behind. Go opens all of them close-on-exec.
		f, err := os.Open(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, f, ln, conn)
	}
	err := (ExecChildUseCase{Args: args}).Run([]domain.EnvPair{"INJECTED=hello"})
	runtime.KeepAlive(held) // keeps the descriptors open until the exec; only reached if it failed
	// Only reached if the exec failed.
	os.Stderr.WriteString("helper: exec failed: " + err.Error() + "\n")
	os.Exit(97)
}

func helperCmd(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"="+strings.Join(args, "\x1f"))
	return cmd
}

func TestExecChildUseCase_RealExec_InjectsEnv(t *testing.T) {
	var out bytes.Buffer
	cmd := helperCmd("sh", "-c", `printf %s "$INJECTED"`)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper failed: %v", err)
	}
	if out.String() != "hello" {
		t.Errorf("the exec'd command saw INJECTED=%q, want %q", out.String(), "hello")
	}
}

func TestExecChildUseCase_RealExec_ExitCodeIsTheCommandsOwn(t *testing.T) {
	err := helperCmd("sh", "-c", "exit 42").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 42 {
		t.Fatalf("exit status = %v, want 42", err)
	}
}

func TestExecChildUseCase_RealExec_CommandNotFound(t *testing.T) {
	err := helperCmd("definitely-not-a-real-command-xyz").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 97 {
		t.Fatalf("exit status = %v, want 97 (the helper's 'exec failed' code)", err)
	}
}

// TestExecChildUseCase_RealExec_SignalReachesTheCommand is the regression test
// for the reason exec replaces the process: SIGTERM sent to the process the
// container runtime started must reach the application. When the daemon forked
// the command as a child, it received the signal itself, died, and the
// application never saw it.
func TestExecChildUseCase_RealExec_SignalReachesTheCommand(t *testing.T) {
	cmd := helperCmd("sh", "-c", `trap 'echo got-TERM; exit 0' TERM; echo ready; while :; do sleep 0.05; done`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	waitFor := func(want string) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("output ended before %q appeared", want)
				}
				if l == want {
					return
				}
			case <-timeout:
				t.Fatalf("timed out waiting for %q", want)
			}
		}
	}

	waitFor("ready") // the shell has installed its trap
	// cmd.Process is the SAME pid that is now running sh: execve keeps the pid.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	waitFor("got-TERM")

	if err := cmd.Wait(); err != nil {
		t.Errorf("the command exited 0 from its trap, but Wait returned %v", err)
	}
}

// TestExecChildUseCase_RealExec_DoesNotLeakFileDescriptors: the application must start with
// exactly the descriptors it would have had if it had been launched directly. A descriptor the
// daemon opened for fetching the configuration (a gRPC socket, a file) must not survive the exec.
func TestExecChildUseCase_RealExec_DoesNotLeakFileDescriptors(t *testing.T) {
	list := func(openFDs bool) string {
		t.Helper()
		cmd := helperCmd("sh", "-c", `for f in /dev/fd/*; do echo "${f##*/}"; done | sort -n | tr '\n' ' '`)
		if openFDs {
			cmd.Env = append(cmd.Env, helperOpenFDsEnv+"=1")
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("helper failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	base, withOpen := list(false), list(true)
	if base == "" {
		t.Skip("cannot list /dev/fd on this system")
	}
	if base != withOpen {
		t.Errorf("descriptors seen by the exec'd command: %q without the daemon holding any, %q with a file and a socket open: they leaked", base, withOpen)
	}
}
