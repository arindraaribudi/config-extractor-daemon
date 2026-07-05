package application

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// InstallBinaryUseCase copies the running binary into destDir, preserving
// the executable name. Used by the `--install` CLI flag so the daemon can
// be dropped into a PATH directory without `go build` at deploy time.
type InstallBinaryUseCase struct {
	DestDir string
}

// Overridable in tests so the os.Executable() error branch can be hit
// without poking process-level state.
var executablePath = os.Executable

func (uc InstallBinaryUseCase) Run() error {
	src, err := executablePath()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}

	if err := os.MkdirAll(uc.DestDir, 0755); err != nil {
		return fmt.Errorf("mkdir %q: %w", uc.DestDir, err)
	}
	return installBinary(src, uc.DestDir)
}

// installBinary copies src into destDir under the same basename. Extracted
// from Run() so the I/O error paths can be exercised in tests with fixture
// paths, without needing to mutate the running test binary.
func installBinary(src, destDir string) error {
	dest := filepath.Join(destDir, filepath.Base(src))

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close dest: %w", err)
	}
	if err := os.Chmod(dest, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	return nil
}
