package domain

import "errors"

// Sentinel errors. Adapters wrap them with %w; the application layer uses
// errors.Is to decide between strict-mode abort vs warn-and-continue.
var (
	// ErrNoSource — no registered ConfigSource matches the given location.
	ErrNoSource = errors.New("no config source matches location")

	// ErrUnsupportedMode — provider does not support the requested FetchMode.
	ErrUnsupportedMode = errors.New("fetch mode not supported by provider")

	// ErrStrictFetch — fetch failed and strict-fetch policy is on.
	ErrStrictFetch = errors.New("strict-fetch: config fetch failed")

	// ErrStrictSecretRef — secret ref resolution failed and strict-secret-refs is on.
	ErrStrictSecretRef = errors.New("strict-secret-refs: resolution failed")
)
