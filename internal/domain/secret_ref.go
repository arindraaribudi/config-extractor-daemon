package domain

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// SecretRefPattern matches __SECRET_REF__(uri) placeholders anywhere inside
// a value. Adapters and the use case both rely on this — do not change
// without updating the on-disk payload contract.
var SecretRefPattern = regexp.MustCompile(`__SECRET_REF__\(([^)]+)\)`)

// SecretResolver is the port for resolving __SECRET_REF__(uri) tokens to
// plaintext. Adapters live in internal/infrastructure/secrets/*.
//
// Supports is consulted for each ref; Resolve is called only when an
// adapter claims ownership. This keeps a single registry of providers
// neutral across clouds.
type SecretResolver interface {
	Supports(ref string) bool
	Resolve(ctx context.Context, ref string) (string, error)
}

// ResolveSecretRefs scans pairs for __SECRET_REF__(...) tokens and replaces
// each one with the secret value fetched from the first matching provider.
//
// Returns:
//   - updated pairs (same length as input)
//   - total number of __SECRET_REF__() tokens resolved
//   - number of pairs that contained at least one placeholder
//   - first error encountered (remaining pairs are left unchanged on error)
func ResolveSecretRefs(ctx context.Context, pairs []EnvPair, providers []SecretResolver) ([]EnvPair, int, int, error) {
	updated := make([]EnvPair, len(pairs))
	totalPlaceholders := 0
	varsUpdated := 0

	for i, pair := range pairs {
		key, value, ok := strings.Cut(string(pair), "=")
		if !ok {
			updated[i] = pair
			continue
		}

		resolved, n, err := replaceAllRefs(ctx, value, providers)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("key %q: %w", key, err)
		}

		if n > 0 {
			totalPlaceholders += n
			varsUpdated++
		}
		updated[i] = EnvPair(key + "=" + resolved)
	}

	return updated, totalPlaceholders, varsUpdated, nil
}

func replaceAllRefs(ctx context.Context, s string, providers []SecretResolver) (string, int, error) {
	var outerErr error
	count := 0

	result := SecretRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		if outerErr != nil {
			return match
		}
		sub := SecretRefPattern.FindStringSubmatch(match)
		ref := NormalizeRef(sub[1])

		for _, p := range providers {
			if p.Supports(ref) {
				val, err := p.Resolve(ctx, ref)
				if err != nil {
					outerErr = fmt.Errorf("resolve %q: %w", ref, err)
					return match
				}
				count++
				return val
			}
		}

		outerErr = fmt.Errorf("no provider supports secret ref %q", ref)
		return match
	})

	if outerErr != nil {
		return "", 0, outerErr
	}
	return result, count, nil
}

// NormalizeRef cleans a raw ref string extracted from __SECRET_REF__(...):
//   - trims surrounding whitespace
//   - strips one layer of surrounding single or double quotes
//   - strips a leading "//" (GCP resource-name URI format)
//
// Runs before any adapter sees the ref, so all providers receive a consistent
// URI regardless of how it was written in the payload.
func NormalizeRef(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 {
		if first, last := s[0], s[len(s)-1]; first == last && (first == '\'' || first == '"') {
			s = s[1 : len(s)-1]
		}
	}
	return strings.TrimPrefix(s, "//")
}
