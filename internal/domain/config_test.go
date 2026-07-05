package domain

import (
	"sort"
	"testing"
)

func TestParseEnvString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []EnvPair
	}{
		{name: "basic key=value pairs", input: "FOO=bar\nBAZ=qux\n", expected: []EnvPair{"FOO=bar", "BAZ=qux"}},
		{name: "strips blank lines", input: "FOO=bar\n\nBAZ=qux\n", expected: []EnvPair{"FOO=bar", "BAZ=qux"}},
		{name: "strips comment lines", input: "# comment\nFOO=bar\n# another\nBAZ=qux\n", expected: []EnvPair{"FOO=bar", "BAZ=qux"}},
		{name: "strips leading/trailing whitespace from lines", input: "  FOO=bar  \n  BAZ=qux  \n", expected: []EnvPair{"FOO=bar", "BAZ=qux"}},
		{name: "empty input", input: "", expected: nil},
		{name: "only comments and blanks", input: "# comment\n\n# another\n", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEnvString(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d pairs, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("pair[%d]: got %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestParsePayloadJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "flat JSON object", input: `{"FOO":"bar","BAZ":"qux"}`, expected: []string{"BAZ=qux", "FOO=bar"}},
		{name: "JSON with integer and bool", input: `{"COUNT":42,"ENABLED":true}`, expected: []string{"COUNT=42", "ENABLED=true"}},
		{name: "nested JSON flattened with underscore", input: `{"DB":{"HOST":"localhost","PORT":"5432"}}`, expected: []string{"DB_HOST=localhost", "DB_PORT=5432"}},
		{name: "JSON null value", input: `{"EMPTY":null}`, expected: []string{"EMPTY="}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePayload(tt.input)
			strs := make([]string, len(got))
			for i, p := range got {
				strs[i] = string(p)
			}
			sort.Strings(strs)
			if len(strs) != len(tt.expected) {
				t.Fatalf("got %d pairs, want %d: %v", len(strs), len(tt.expected), strs)
			}
			for i := range strs {
				if strs[i] != tt.expected[i] {
					t.Errorf("pair[%d]: got %q, want %q", i, strs[i], tt.expected[i])
				}
			}
		})
	}
}

func TestParsePayloadYAML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "flat YAML mapping", input: "FOO: bar\nBAZ: qux\n", expected: []string{"BAZ=qux", "FOO=bar"}},
		{name: "YAML with integer value", input: "COUNT: 42\n", expected: []string{"COUNT=42"}},
		{name: "nested YAML flattened with underscore", input: "DB:\n  HOST: localhost\n  PORT: \"5432\"\n", expected: []string{"DB_HOST=localhost", "DB_PORT=5432"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePayload(tt.input)
			strs := make([]string, len(got))
			for i, p := range got {
				strs[i] = string(p)
			}
			sort.Strings(strs)
			if len(strs) != len(tt.expected) {
				t.Fatalf("got %d pairs, want %d: %v", len(strs), len(tt.expected), strs)
			}
			for i := range strs {
				if strs[i] != tt.expected[i] {
					t.Errorf("pair[%d]: got %q, want %q", i, strs[i], tt.expected[i])
				}
			}
		})
	}
}

func TestFlattenToEnv(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		prefix   string
		expected []string
	}{
		{name: "flat map no prefix", input: map[string]any{"FOO": "bar"}, prefix: "", expected: []string{"FOO=bar"}},
		{name: "flat map with prefix", input: map[string]any{"HOST": "localhost"}, prefix: "DB", expected: []string{"DB_HOST=localhost"}},
		{name: "null value becomes empty string", input: map[string]any{"EMPTY": nil}, prefix: "", expected: []string{"EMPTY="}},
		{name: "numeric value converted to string", input: map[string]any{"PORT": 5432}, prefix: "", expected: []string{"PORT=5432"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenToEnv(tt.input, tt.prefix)
			strs := make([]string, len(got))
			for i, p := range got {
				strs[i] = string(p)
			}
			sort.Strings(strs)
			if len(strs) != len(tt.expected) {
				t.Fatalf("got %d pairs, want %d: %v", len(strs), len(tt.expected), strs)
			}
			for i := range strs {
				if strs[i] != tt.expected[i] {
					t.Errorf("pair[%d]: got %q, want %q", i, strs[i], tt.expected[i])
				}
			}
		})
	}
}

func TestParsePayloadUnformatted(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "plain KEY=VALUE", input: "FOO=bar\nBAZ=qux\n", expected: []string{"FOO=bar", "BAZ=qux"}},
		{name: "strips comments and blanks", input: "# comment\nFOO=bar\n\nBAZ=qux\n", expected: []string{"FOO=bar", "BAZ=qux"}},
		{name: "empty input", input: "", expected: nil},
		{name: "whitespace-only input", input: "   \n  \n", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePayload(tt.input)
			strs := make([]string, len(got))
			for i, p := range got {
				strs[i] = string(p)
			}
			if len(strs) != len(tt.expected) {
				t.Fatalf("got %d pairs, want %d: %v", len(strs), len(tt.expected), strs)
			}
			for i := range strs {
				if strs[i] != tt.expected[i] {
					t.Errorf("pair[%d]: got %q, want %q", i, strs[i], tt.expected[i])
				}
			}
		})
	}
}
