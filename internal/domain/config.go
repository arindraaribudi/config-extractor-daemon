package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Payload is a raw config payload fetched from a ConfigSource, before
// parsing into KEY=VALUE pairs.
type Payload string

// EnvPair is a single KEY=VALUE string. Keys are not validated here;
// callers downstream (env writer, exec injector) treat the slice as opaque
// strings — keeping this type loose preserves payload fidelity (whitespace,
// quoting) end-to-end.
type EnvPair string

// FetchMode controls how a ConfigSource returns a payload.
//   - "get"    — raw payload, as stored
//   - "render" — payload with template variables resolved (provider-dependent)
const (
	FetchGet    FetchMode = "get"
	FetchRender FetchMode = "render"
)

type FetchMode string

// ParsePayload detects whether content is JSON, YAML, or plain KEY=VALUE
// and always returns KEY=VALUE pairs. Detection order: JSON → YAML → plain.
//
// Detection is content-driven; callers do not need to declare the format.
func ParsePayload(content string) []EnvPair {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	if strings.HasPrefix(trimmed, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
			pairs := flattenToEnv(m, "")
			sort.Slice(pairs, func(i, j int) bool { return pairs[i] < pairs[j] })
			return pairs
		}
	}

	var node any
	if err := yaml.Unmarshal([]byte(content), &node); err == nil {
		if m, ok := node.(map[string]any); ok {
			pairs := flattenToEnv(m, "")
			sort.Slice(pairs, func(i, j int) bool { return pairs[i] < pairs[j] })
			return pairs
		}
	}

	return parseEnvString(content)
}

func parseEnvString(content string) []EnvPair {
	lines := strings.Split(content, "\n")
	pairs := make([]EnvPair, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		pairs = append(pairs, EnvPair(line))
	}
	return pairs
}

func flattenToEnv(m map[string]any, prefix string) []EnvPair {
	var pairs []EnvPair
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "_" + k
		}
		switch val := v.(type) {
		case map[string]any:
			pairs = append(pairs, flattenToEnv(val, key)...)
		case nil:
			pairs = append(pairs, EnvPair(key+"="))
		default:
			pairs = append(pairs, EnvPair(fmt.Sprintf("%s=%v", key, val)))
		}
	}
	return pairs
}
