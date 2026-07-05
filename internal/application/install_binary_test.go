package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallBinaryUseCase(t *testing.T) {
	destDir := t.TempDir()

	if err := (InstallBinaryUseCase{DestDir: destDir}).Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	src, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	dest := filepath.Join(destDir, filepath.Base(src))

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("dest file not found: %v", err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Errorf("dest permissions = %o, want executable bits set", info.Mode().Perm())
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	if info.Size() != srcInfo.Size() {
		t.Errorf("dest size = %d, want %d", info.Size(), srcInfo.Size())
	}
}

func TestInstallBinaryUseCase_DestIsFile(t *testing.T) {
	// destDir is a regular file -> MkdirAll fails.
	tmp := t.TempDir()
	regular := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(regular, []byte("blocker"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := (InstallBinaryUseCase{DestDir: regular}).Run()
	if err == nil {
		t.Fatal("expected error when destDir is a regular file")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error = %v, want it to wrap mkdir failure", err)
	}
}

func TestInstallBinaryUseCase_DestPathExists(t *testing.T) {
	// Use an existing system directory; success path through MkdirAll.
	if err := (InstallBinaryUseCase{DestDir: os.TempDir()}).Run(); err != nil {
		t.Fatalf("Run on existing tmp dir returned error: %v", err)
	}
}

func TestInstallBinaryUseCase_DestIsDirAtDest(t *testing.T) {
	// MkdirAll succeeds (real dir), but the binary path under it is also
	// a directory, so os.OpenFile fails -> "create dest" branch fires.
	tmp := t.TempDir()
	src, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	blob := filepath.Join(tmp, filepath.Base(src))
	if err := os.Mkdir(blob, 0755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	err = (InstallBinaryUseCase{DestDir: tmp}).Run()
	if err == nil {
		t.Fatal("expected error when dest is a directory")
	}
	if !strings.Contains(err.Error(), "create dest") {
		t.Errorf("error = %v, want it to wrap create-dest failure", err)
	}
}

func TestInstallBinary_NonexistentSource(t *testing.T) {
	// installBinary is the extracted I/O helper; open a path that doesn't
	// exist -> "open source" error branch fires.
	dir := t.TempDir()
	err := installBinary(filepath.Join(dir, "does-not-exist"), dir)
	if err == nil {
		t.Fatal("expected open-source error for missing src")
	}
	if !strings.Contains(err.Error(), "open source") {
		t.Errorf("error = %v, want it to wrap open-source failure", err)
	}
}

func TestInstallBinary_HappyPath(t *testing.T) {
	// installBinary copies one file to another directory — bypasses the
	// os.Executable lookup so we exercise only the extracted helper.
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src := filepath.Join(srcDir, "blob.bin")
	if err := os.WriteFile(src, []byte("payload"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := installBinary(src, dstDir); err != nil {
		t.Fatalf("installBinary: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dstDir, "blob.bin"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("copied content = %q, want %q", got, "payload")
	}
}

func TestInstallBinary_SrcIsDirectory(t *testing.T) {
	// src opens successfully (it's a directory), but reading from it
	// errors during io.Copy — covers the Copy error branch.
	dir := t.TempDir()
	dst := t.TempDir()
	err := installBinary(dir, dst)
	if err == nil {
		t.Fatal("expected copy error when src is a directory")
	}
	if !strings.Contains(err.Error(), "copy") {
		t.Errorf("error = %v, want it to wrap copy failure", err)
	}
}

func TestInstallBinaryUseCase_ExecutableError(t *testing.T) {
	orig := executablePath
	executablePath = func() (string, error) { return "", errors.New("forced-exec-fail") }
	defer func() { executablePath = orig }()

	err := (InstallBinaryUseCase{DestDir: t.TempDir()}).Run()
	if err == nil {
		t.Fatal("expected executable error")
	}
	if !strings.Contains(err.Error(), "resolve executable") {
		t.Errorf("error = %v, want it to wrap executable failure", err)
	}
}

func TestInstallBinaryUseCase_EvalSymlinksError(t *testing.T) {
	// Build a circular symlink and point executablePath at it.
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("symlink setup: %v", err)
	}
	orig := executablePath
	executablePath = func() (string, error) { return loop, nil }
	defer func() { executablePath = orig }()

	err := (InstallBinaryUseCase{DestDir: t.TempDir()}).Run()
	if err == nil {
		t.Fatal("expected EvalSymlinks error on circular symlink")
	}
	if !strings.Contains(err.Error(), "eval symlinks") {
		t.Errorf("error = %v, want it to wrap symlink failure", err)
	}
}
