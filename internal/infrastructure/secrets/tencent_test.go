package secrets

import (
	"strings"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
)

func TestParseTencentSecretRef(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantRegion string
		wantSecret string
		wantErrSub string
	}{
		{
			name:       "valid legacy form",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password",
			wantRegion: "ap-bangkok",
			wantSecret: "db-password",
		},
		{
			name:       "secret with hyphens",
			ref:        "secretsmanager.tencentcloudapi.com/ap-shanghai/inner-svc-tls-key",
			wantRegion: "ap-shanghai",
			wantSecret: "inner-svc-tls-key",
		},
		{
			name:       "missing slash",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok",
			wantErrSub: "expected",
		},
		{
			name:       "empty region",
			ref:        "secretsmanager.tencentcloudapi.com//db-password",
			wantErrSub: "region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, secret, err := parseTencentSecretRef(tt.ref)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSub)
				}
				if !contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error %q missing %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if region != tt.wantRegion || secret != tt.wantSecret {
				t.Fatalf("got (%q,%q), want (%q,%q)", region, secret, tt.wantRegion, tt.wantSecret)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestTencentResolver_Supports(t *testing.T) {
	r := NewTencentResolver()
	if !r.Supports("secretsmanager.tencentcloudapi.com/ap-bangkok/db-password") {
		t.Fatal("expected support for valid prefix")
	}
	if r.Supports("secretmanager.googleapis.com/projects/p/secrets/s/versions/1") {
		t.Fatal("did not expect support for GCP prefix")
	}
}

func TestTencentResolver_Resolve_Success(t *testing.T) {
	// Replace the SDK client factory for the test.
	prev := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		if region != "ap-bangkok" {
			t.Fatalf("unexpected region %q", region)
		}
		if creds.SecretID == "" || creds.SecretKey == "" {
			t.Fatal("creds not propagated")
		}
		return fakeSecretsClient{secret: "s3cret"}, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRETID", "id")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "key")

	// Wipe the cached credentials from tencentcred to force re-resolve with env vars.
	tencentcred.ResetForTest()

	got, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("got %q, want s3cret", got)
	}
}

func TestTencentResolver_Resolve_BinaryUnsupported(t *testing.T) {
	prev := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		return fakeSecretsClient{binary: "YmFzZTY0"}, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRETID", "id")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "key")
	tencentcred.ResetForTest()

	_, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/blob")
	if err == nil {
		t.Fatal("expected error for binary secret")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("error %q should mention 'binary'", err.Error())
	}
}
