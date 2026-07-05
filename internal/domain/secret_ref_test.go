package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockResolver is a test-only SecretResolver backed by a static map.
type mockResolver struct {
	prefix  string
	secrets map[string]string
}

func (m *mockResolver) Supports(ref string) bool {
	return strings.HasPrefix(ref, m.prefix)
}

func (m *mockResolver) Resolve(_ context.Context, ref string) (string, error) {
	val, ok := m.secrets[ref]
	if !ok {
		return "", fmt.Errorf("mock: unknown ref %q", ref)
	}
	return val, nil
}

func TestResolveSecretRefs_NoPlaceholders(t *testing.T) {
	pairs := []EnvPair{"FOO=bar", "BAZ=qux"}
	providers := []SecretResolver{&mockResolver{prefix: "mock://", secrets: map[string]string{}}}

	got, placeholders, varsUpdated, err := ResolveSecretRefs(context.Background(), pairs, providers)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if placeholders != 0 {
		t.Errorf("placeholders = %d, want 0", placeholders)
	}
	if varsUpdated != 0 {
		t.Errorf("varsUpdated = %d, want 0", varsUpdated)
	}
	for i, p := range got {
		if p != pairs[i] {
			t.Errorf("pair[%d] = %q, want %q", i, p, pairs[i])
		}
	}
}

func TestResolveSecretRefs_SingleRef(t *testing.T) {
	provider := &mockResolver{
		prefix:  "mock://",
		secrets: map[string]string{"mock://db-password": "s3cr3t"},
	}
	pairs := []EnvPair{`DB_PASS=__SECRET_REF__(mock://db-password)`, "FOO=bar"}

	got, placeholders, varsUpdated, err := ResolveSecretRefs(context.Background(), pairs, []SecretResolver{provider})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if placeholders != 1 {
		t.Errorf("placeholders = %d, want 1", placeholders)
	}
	if varsUpdated != 1 {
		t.Errorf("varsUpdated = %d, want 1", varsUpdated)
	}
	if got[0] != "DB_PASS=s3cr3t" {
		t.Errorf("got[0] = %q, want %q", got[0], "DB_PASS=s3cr3t")
	}
	if got[1] != "FOO=bar" {
		t.Errorf("got[1] = %q, want %q", got[1], "FOO=bar")
	}
}

func TestResolveSecretRefs_MultipleRefsInSameValue(t *testing.T) {
	provider := &mockResolver{
		prefix: "mock://",
		secrets: map[string]string{
			"mock://host": "localhost",
			"mock://port": "5432",
		},
	}
	pairs := []EnvPair{`DSN=__SECRET_REF__(mock://host):__SECRET_REF__(mock://port)`}

	got, placeholders, varsUpdated, err := ResolveSecretRefs(context.Background(), pairs, []SecretResolver{provider})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if placeholders != 2 {
		t.Errorf("placeholders = %d, want 2", placeholders)
	}
	if varsUpdated != 1 {
		t.Errorf("varsUpdated = %d, want 1", varsUpdated)
	}
	if got[0] != "DSN=localhost:5432" {
		t.Errorf("got[0] = %q, want %q", got[0], "DSN=localhost:5432")
	}
}

func TestResolveSecretRefs_MultipleVarsUpdated(t *testing.T) {
	provider := &mockResolver{
		prefix: "mock://",
		secrets: map[string]string{
			"mock://user": "admin",
			"mock://pass": "hunter2",
		},
	}
	pairs := []EnvPair{
		`USER=__SECRET_REF__(mock://user)`,
		`PASS=__SECRET_REF__(mock://pass)`,
		`PLAIN=unchanged`,
	}

	got, placeholders, varsUpdated, err := ResolveSecretRefs(context.Background(), pairs, []SecretResolver{provider})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if placeholders != 2 {
		t.Errorf("placeholders = %d, want 2", placeholders)
	}
	if varsUpdated != 2 {
		t.Errorf("varsUpdated = %d, want 2", varsUpdated)
	}
	if got[0] != "USER=admin" {
		t.Errorf("got[0] = %q, want %q", got[0], "USER=admin")
	}
	if got[1] != "PASS=hunter2" {
		t.Errorf("got[1] = %q, want %q", got[1], "PASS=hunter2")
	}
	if got[2] != "PLAIN=unchanged" {
		t.Errorf("got[2] = %q, want %q", got[2], "PLAIN=unchanged")
	}
}

func TestResolveSecretRefs_UnknownProvider(t *testing.T) {
	pairs := []EnvPair{`KEY=__SECRET_REF__(unknown://secret)`}
	_, _, _, err := ResolveSecretRefs(context.Background(), pairs, []SecretResolver{})

	if err == nil {
		t.Fatal("expected error for unrecognised provider, got nil")
	}
}

func TestResolveSecretRefs_ProviderError(t *testing.T) {
	provider := &mockResolver{
		prefix:  "mock://",
		secrets: map[string]string{},
	}
	pairs := []EnvPair{`KEY=__SECRET_REF__(mock://missing)`}

	_, _, _, err := ResolveSecretRefs(context.Background(), pairs, []SecretResolver{provider})

	if err == nil {
		t.Fatal("expected error from provider, got nil")
	}
}

func TestResolveSecretRefs_NilPairs(t *testing.T) {
	got, placeholders, varsUpdated, err := ResolveSecretRefs(context.Background(), nil, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
	if placeholders != 0 || varsUpdated != 0 {
		t.Errorf("expected 0 counts, got %d / %d", placeholders, varsUpdated)
	}
}

func TestResolveSecretRefs_PairWithoutEquals(t *testing.T) {
	pairs := []EnvPair{"INVALID_PAIR"}
	got, _, _, err := ResolveSecretRefs(context.Background(), pairs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != "INVALID_PAIR" {
		t.Errorf("got[0] = %q, want %q", got[0], "INVALID_PAIR")
	}
}

func TestNormalizeRef(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1",
			"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1"},
		{"//secretmanager.googleapis.com/projects/123/secrets/foo/versions/1",
			"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1"},
		{"'secretmanager.googleapis.com/projects/123/secrets/foo/versions/1'",
			"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1"},
		{"'//secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest'",
			"secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest"},
		{`"//secretmanager.googleapis.com/projects/123/secrets/foo/versions/1"`,
			"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1"},
		{"  //secretmanager.googleapis.com/projects/123/secrets/foo/versions/1  ",
			"secretmanager.googleapis.com/projects/123/secrets/foo/versions/1"},
		{"'//secretsmanager.amazonaws.com/us-east-1/my-secret'",
			"secretsmanager.amazonaws.com/us-east-1/my-secret"},
	}
	for _, c := range cases {
		got := NormalizeRef(c.raw)
		if got != c.want {
			t.Errorf("NormalizeRef(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestResolveSecretRefs_QuotedAndDoubleSlashRef(t *testing.T) {
	provider := &mockResolver{
		prefix:  "mock://",
		secrets: map[string]string{"mock://secret": "value123"},
	}
	pairs := []EnvPair{`KEY=__SECRET_REF__('//mock://secret')`}

	got, placeholders, _, err := ResolveSecretRefs(context.Background(), pairs, []SecretResolver{provider})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if placeholders != 1 {
		t.Errorf("placeholders = %d, want 1", placeholders)
	}
	if got[0] != "KEY=value123" {
		t.Errorf("got[0] = %q, want %q", got[0], "KEY=value123")
	}
}
