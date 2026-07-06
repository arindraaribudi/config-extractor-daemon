// Package secrets holds the SecretResolver adapters for each cloud.
//
// Like package source, the registry below is the SINGLE edit point for
// adding a new secret backend. Drop a new file in this package that
// returns a domain.SecretResolver, then append one entry to Resolvers.
// No other package needs to change.
package secrets

import (
	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// ResolverEntry pairs a SecretResolver factory with the provider kind it
// belongs to (for logging only — dispatch is purely on Supports()).
type ResolverEntry struct {
	Kind     domain.ProviderKind
	Resolver domain.SecretResolver
}

// Resolvers is the global list of available secret resolvers. Order does
// NOT matter — dispatch is done by Supports().
//
// To add a new cloud:
//  1. Create a new file in this package, e.g. azure.go, exposing a
//     `NewAzureResolver() domain.SecretResolver` constructor.
//  2. Append one entry below.
var Resolvers = []ResolverEntry{
	{Kind: domain.ProviderGCP, Resolver: NewGCPResolver()},
	{Kind: domain.ProviderAWS, Resolver: NewAWSResolver()},
	{Kind: domain.ProviderTencent, Resolver: NewTencentResolver()},
}
