package application

import (
	"fmt"
	"os"
	"strings"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// WriteEnvUseCase writes KEY=VALUE pairs to a file with owner-only perms.
// Always writes the file (even when pairs is empty) so downstream processes
// can `source` it without "no such file" errors after a failed fetch.
type WriteEnvUseCase struct {
	Path string
}

func (uc WriteEnvUseCase) Run(pairs []domain.EnvPair) error {
	content := ""
	if len(pairs) > 0 {
		strs := make([]string, len(pairs))
		for i, p := range pairs {
			strs[i] = string(p)
		}
		content = strings.Join(strs, "\n") + "\n"
	}
	if err := os.WriteFile(uc.Path, []byte(content), 0600); err != nil {
		return fmt.Errorf("write %q: %w", uc.Path, err)
	}
	return nil
}
