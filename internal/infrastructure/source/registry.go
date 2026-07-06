// Package source contains the ConfigSource adapters that fetch config
// payloads from each cloud. The registry below is the SINGLE edit point
// for adding a new cloud — drop a new adapter file in this package and
// append one line to Sources. The use-case layer iterates this slice;
// no switch/case lives outside this file.
package source

import (
	"context"
	"strings"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// SourceFactory builds a fresh ConfigSource for a given FetchMode. The
// returned source may capture the mode (e.g. for "render" vs "get") so
// the use case can defer construction until after flags/env are parsed.
type SourceFactory func(ctx context.Context, mode domain.FetchMode) (domain.ConfigSource, error)

// Entry is one row in the Sources registry. Order matters: the first
// entry whose Match returns true for a CONFIG_LOCATION wins.
type Entry struct {
	Kind    domain.ProviderKind
	Match   domain.LocationMatcher
	Factory SourceFactory
}

// Sources is the global list of available config providers.
//
// To add a new cloud (e.g. Azure):
//  1. Create a new file in this package that exports a
//     `NewXxxSource(ctx, mode) (domain.ConfigSource, error)` factory.
//  2. Append one entry below.
//
// Domain, application, and cmd/ require zero changes.
var Sources = []Entry{
	{
		Kind:    domain.ProviderAWS,
		Match:   isSSMLocation,
		Factory: NewAWSSource,
	},
	{
		Kind:    domain.ProviderTencent,
		Match:   isTencentLocation,
		Factory: NewTencentSource,
	},
	{
		Kind:    domain.ProviderGCP,
		Match:   func(string) bool { return true }, // GCP is the fallback for any non-AWS, non-Tencent location
		Factory: NewGCPSource,
	},
}

// isSSMLocation matches both ARN form (arn:aws:ssm:...) and bare
// parameter-name form (/{path}) used by AWS SSM Parameter Store.
func isSSMLocation(location string) bool {
	return strings.HasPrefix(location, "arn:aws:ssm:") || strings.HasPrefix(location, "/")
}
