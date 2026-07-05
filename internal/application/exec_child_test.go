package application

import (
	"path/filepath"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

func TestExecChildUseCase(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "result.txt")

	err := (ExecChildUseCase{Args: []string{"sh", "-c", "echo $TEST_VAR > " + out}}).Run(
		[]domain.EnvPair{"TEST_VAR=hello"},
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	data, err := osReadFile(out)
	if err != nil {
		t.Fatalf("could not read output file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("child saw TEST_VAR = %q, want %q", string(data), "hello\n")
	}
}

func TestExecChildUseCaseNilPairs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "result.txt")

	err := (ExecChildUseCase{Args: []string{"sh", "-c", "echo ok > " + out}}).Run(nil)
	if err != nil {
		t.Fatalf("Run with nil pairs returned error: %v", err)
	}

	data, err := osReadFile(out)
	if err != nil {
		t.Fatalf("could not read output file: %v", err)
	}
	if string(data) != "ok\n" {
		t.Errorf("child output = %q, want %q", string(data), "ok\n")
	}
}

func TestExecChildUseCasePropagatesExitCode(t *testing.T) {
	err := (ExecChildUseCase{Args: []string{"sh", "-c", "exit 42"}}).Run(nil)
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
}

func TestExecChildUseCaseNoArgs(t *testing.T) {
	err := (ExecChildUseCase{Args: nil}).Run(nil)
	if err == nil {
		t.Fatal("expected error for empty args, got nil")
	}
}
