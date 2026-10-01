package tencentcred

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetCache() {
	cacheMu.Lock()
	cached = nil
	cachedSource = ""
	cacheErr = nil
	cacheMu.Unlock()
}

// stubSources replaces the sources slice with the given order. Each stub
// runs the provided func; returning (nil, nil) means "not configured,
// try next source".
func stubSources(t *testing.T, stubs ...func(ctx context.Context) (*Credentials, error)) {
	t.Helper()
	prev := sources
	sources = stubs
	t.Cleanup(func() { sources = prev })
}

func TestResolve_FirstSourceWins(t *testing.T) {
	resetCache()
	stubSources(t,
		func(ctx context.Context) (*Credentials, error) {
			return &Credentials{SecretID: "a-id", SecretKey: "a-key", Token: "a-tok"}, nil
		},
		func(ctx context.Context) (*Credentials, error) {
			t.Fatal("env source should not be called")
			return nil, nil
		},
		func(ctx context.Context) (*Credentials, error) {
			t.Fatal("profile source should not be called")
			return nil, nil
		},
	)

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "a-id" || got.Token != "a-tok" {
		t.Fatalf("got %+v, want first source creds", got)
	}
}

func TestResolve_FallsThroughNotConfigured(t *testing.T) {
	resetCache()
	stubSources(t,
		func(ctx context.Context) (*Credentials, error) { return nil, nil }, // STS: not configured
		func(ctx context.Context) (*Credentials, error) {
			return &Credentials{SecretID: "env-id", SecretKey: "env-key"}, nil
		},
		func(ctx context.Context) (*Credentials, error) {
			t.Fatal("profile source should not be called")
			return nil, nil
		},
	)

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "env-id" || got.Token != "" {
		t.Fatalf("got %+v, want env creds with empty token", got)
	}
}

func TestResolve_ProfileIsLastResort(t *testing.T) {
	resetCache()
	stubSources(t,
		func(ctx context.Context) (*Credentials, error) { return nil, nil },
		func(ctx context.Context) (*Credentials, error) { return nil, nil },
		func(ctx context.Context) (*Credentials, error) {
			return &Credentials{SecretID: "prof-id", SecretKey: "prof-key"}, nil
		},
	)

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "prof-id" || got.Token != "" {
		t.Fatalf("got %+v, want profile creds with empty token", got)
	}
}

func TestResolve_AllMiss(t *testing.T) {
	resetCache()
	stubSources(t,
		func(ctx context.Context) (*Credentials, error) { return nil, nil },
		func(ctx context.Context) (*Credentials, error) { return nil, nil },
		func(ctx context.Context) (*Credentials, error) { return nil, nil },
	)

	_, err := Resolve(t.Context())
	if err == nil {
		t.Fatal("expected error when no creds available")
	}
	if !strings.Contains(err.Error(), "TKE pod identity") || !strings.Contains(err.Error(), "env") || !strings.Contains(err.Error(), "SSO") || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("error %q should name all four sources", err.Error())
	}
}

func TestResolve_SourceErrorIsFatal(t *testing.T) {
	resetCache()
	wantErr := ErrNoCredentials
	stubSources(t,
		func(ctx context.Context) (*Credentials, error) { return nil, wantErr },
	)

	_, err := Resolve(t.Context())
	if err == nil {
		t.Fatal("expected error when source fails")
	}
	if !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("error %q should wrap ErrNoCredentials", err.Error())
	}
}

func TestResolve_CacheHit(t *testing.T) {
	resetCache()
	calls := 0
	stubSources(t,
		func(ctx context.Context) (*Credentials, error) {
			calls++
			return &Credentials{SecretID: "x-id", SecretKey: "x-key", Token: "x-tok"}, nil
		},
	)

	first, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if first != second {
		t.Fatalf("expected cached pointer; first=%p second=%p", first, second)
	}
	if calls != 1 {
		t.Fatalf("expected source called once, got %d", calls)
	}
}

func TestTccliSSOSource_ReadsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.credential")
	body := `{"secretId":"sso-id","secretKey":"sso-key","token":"sso-token"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	prev := tccliSSOCredsPath
	tccliSSOCredsPath = func() string { return path }
	t.Cleanup(func() { tccliSSOCredsPath = prev })

	got, err := tccliSSOSource(t.Context())
	if err != nil {
		t.Fatalf("tccliSSOSource: %v", err)
	}
	if got == nil || got.SecretID != "sso-id" || got.SecretKey != "sso-key" || got.Token != "sso-token" {
		t.Fatalf("got %+v, want sso creds", got)
	}
}

func TestTccliSSOSource_FileMissing(t *testing.T) {
	prev := tccliSSOCredsPath
	tccliSSOCredsPath = func() string { return "/nonexistent/.tccli/default.credential" }
	t.Cleanup(func() { tccliSSOCredsPath = prev })

	got, err := tccliSSOSource(t.Context())
	if err != nil {
		t.Fatalf("missing file must not error; got %v", err)
	}
	if got != nil {
		t.Fatalf("missing file must return nil creds; got %+v", got)
	}
}

func TestTccliSSOSource_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.credential")
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	prev := tccliSSOCredsPath
	tccliSSOCredsPath = func() string { return path }
	t.Cleanup(func() { tccliSSOCredsPath = prev })

	got, err := tccliSSOSource(t.Context())
	if err != nil {
		t.Fatalf("malformed JSON must not error; got %v", err)
	}
	if got != nil {
		t.Fatalf("malformed JSON must return nil creds; got %+v", got)
	}
}

func TestTccliSSOSource_MissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default.credential")
	body := `{"token":"only-token"}` // no secretId, no secretKey
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	prev := tccliSSOCredsPath
	tccliSSOCredsPath = func() string { return path }
	t.Cleanup(func() { tccliSSOCredsPath = prev })

	got, err := tccliSSOSource(t.Context())
	if err != nil {
		t.Fatalf("missing fields must not error; got %v", err)
	}
	if got != nil {
		t.Fatalf("missing fields must return nil creds; got %+v", got)
	}
}

func TestParseTKEIssuer_Valid(t *testing.T) {
	// JWT with iss = https://ap-bangkok-oidc.tke.tencentcs.com/id/abc123
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://ap-bangkok-oidc.tke.tencentcs.com/id/abc123"}`))
	tok := header + "." + payload + ".signature"

	region, providerID, err := parseTKEIssuer(tok)
	if err != nil {
		t.Fatalf("parseTKEIssuer: %v", err)
	}
	if region != "ap-bangkok" || providerID != "abc123" {
		t.Fatalf("got (%q, %q), want (ap-bangkok, abc123)", region, providerID)
	}
}

func TestParseTKEIssuer_NotTKEHost(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://accounts.google.com"}`))
	tok := "h." + payload + ".s"

	if _, _, err := parseTKEIssuer(tok); err == nil {
		t.Fatal("expected error for non-TKE issuer")
	}
}

func TestParseTKEIssuer_NotAJWT(t *testing.T) {
	if _, _, err := parseTKEIssuer("garbage"); err == nil {
		t.Fatal("expected error for non-JWT input")
	}
}

func TestTKEAutoSource_NoRoleArnReturnsNil(t *testing.T) {
	t.Setenv("TKE_ROLE_ARN", "")
	t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "/nonexistent")
	if got, err := tkeAutoSource(t.Context()); err != nil || got != nil {
		t.Fatalf("got (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestTKEAutoSource_NonTKEIssuerFallsThrough(t *testing.T) {
	// Write a JWT with a non-TKE iss to the default token path. With
	// TKE_ROLE_ARN set, parseTKEIssuer should reject and tkeAutoSource
	// must return (nil, nil) instead of erroring.
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://accounts.google.com"}`))
	if err := os.WriteFile(path, []byte("h."+payload+".s"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TKE_ROLE_ARN", "qcs::cam::uin/0:roleName/test")
	t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", path)

	got, err := tkeAutoSource(t.Context())
	if err != nil {
		t.Fatalf("non-TKE issuer must fall through; got %v", err)
	}
	if got != nil {
		t.Fatalf("non-TKE issuer must return nil creds; got %+v", got)
	}
}
