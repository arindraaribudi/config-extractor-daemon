package application

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// LoadConfigUseCase fetches a config payload from the right cloud source.
//
// Strategy:
//  1. Pick the first registered source whose matcher accepts the location.
//  2. Call Fetch with the requested mode.
//  3. If strict=false and fetch fails, log a warning and return an empty
//     payload (not an error) so the rest of the pipeline still runs.
type LoadConfigUseCase struct {
	Registry []SourceEntry
	Strict   bool
}

type SourceEntry struct {
	Match   domain.LocationMatcher
	Source  domain.ConfigSource
	Factory func(ctx context.Context, mode domain.FetchMode) (domain.ConfigSource, error)
}

type LoadConfigResult struct {
	Provider domain.ProviderKind
	Payload  domain.Payload
}

func (uc LoadConfigUseCase) Run(ctx context.Context, location, version string, mode domain.FetchMode) (LoadConfigResult, error) {
	for _, entry := range uc.Registry {
		if !entry.Match(location) {
			continue
		}
		source := entry.Source
		if source == nil && entry.Factory != nil {
			s, err := entry.Factory(ctx, mode)
			if err != nil {
				return LoadConfigResult{}, fmt.Errorf("%s source factory: %w", entry.KindGuess(), err)
			}
			source = s
		}
		log.Printf("provider=%s location=%s version=%s fetch-mode=%s", source.Kind(), location, version, mode)

		payload, err := source.Fetch(ctx, domain.Reference{Location: location, Version: version}, mode)
		if err != nil {
			if uc.Strict {
				return LoadConfigResult{}, fmt.Errorf("%w: %v", domain.ErrStrictFetch, err)
			}
			log.Printf("WARNING: fetch secret: %v — continuing without config", err)
			return LoadConfigResult{Provider: source.Kind()}, nil
		}
		return LoadConfigResult{Provider: source.Kind(), Payload: payload}, nil
	}
	return LoadConfigResult{}, fmt.Errorf("%w: %q", domain.ErrNoSource, location)
}

// KindGuess returns a placeholder name for the registry entry when the
// adapter's Source is nil and we need to label a factory error. Falls
// back to "unknown" — used only in error paths.
func (e SourceEntry) KindGuess() string {
	if e.Source != nil {
		return string(e.Source.Kind())
	}
	return "unknown"
}

// ErrNoMatchingSource is returned when the registry is empty. Kept here
// for symmetry with domain; not currently surfaced separately.
var ErrNoMatchingSource = errors.New("no source matched location")
