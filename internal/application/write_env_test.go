package application

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

func TestWriteEnvUseCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	if err := (WriteEnvUseCase{Path: path}).Run([]domain.EnvPair{"FOO=bar", "BAZ=qux"}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	want := "FOO=bar\nBAZ=qux\n"
	if string(data) != want {
		t.Errorf("file content = %q, want %q", string(data), want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func TestWriteEnvUseCaseBadPath(t *testing.T) {
	err := (WriteEnvUseCase{Path: "/no/such/dir/x.env"}).Run([]domain.EnvPair{"A=1"})
	if err == nil {
		t.Fatal("expected error for bad path")
	}
}

func TestWriteEnvUseCaseEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	if err := (WriteEnvUseCase{Path: path}).Run(nil); err != nil {
		t.Fatalf("Run with nil pairs returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read written file: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %q", string(data))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}
