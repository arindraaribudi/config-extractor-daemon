package domain

import "context"

// ProviderKind labels a cloud provider. Typed string so callers don't pass
// arbitrary literals; new providers register their own constants in the
// infrastructure layer.
type ProviderKind string

const (
	ProviderGCP     ProviderKind = "gcp"
	ProviderAWS     ProviderKind = "aws"
	ProviderTencent ProviderKind = "tencent"
)

// Reference describes WHERE a config lives and WHICH version to read.
// Concrete URL/ARN shapes are provider-specific and interpreted inside the
// adapter — the domain only knows it has a location + version.
type Reference struct {
	Location string
	Version  string
}

// ConfigSource is the port for fetching a config payload from a cloud
// provider. Adapters live in internal/infrastructure/source/*.
//
// Implementations must be safe to call once per Fetch; lifecycle of any
// underlying client (e.g. gRPC conn) is the adapter's concern.
type ConfigSource interface {
	// Kind identifies the provider — used for logging and as a key when
	// picking a source from a registry.
	Kind() ProviderKind

	// Fetch retrieves the payload for ref. mode == "render" requests
	// template-rendered output where the provider supports it; "get"
	// returns the raw stored payload.
	Fetch(ctx context.Context, ref Reference, mode FetchMode) (Payload, error)
}

// LocationMatches reports whether `location` looks like it belongs to a
// given provider. Adapters expose a matcher that the registry uses to
// dispatch CONFIG_LOCATION to the right source.
type LocationMatcher func(location string) bool
