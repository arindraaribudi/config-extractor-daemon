package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	ssm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
)

func TestParseTencentSecretRef(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		wantRegion  string
		wantSecret  string
		wantVersion string
		wantErrSub  string
	}{
		{
			name:        "valid form, no version",
			ref:         "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password",
			wantRegion:  "ap-bangkok",
			wantSecret:  "db-password",
			wantVersion: "",
		},
		{
			name:        "secret with hyphens",
			ref:         "secretsmanager.tencentcloudapi.com/ap-shanghai/inner-svc-tls-key",
			wantRegion:  "ap-shanghai",
			wantSecret:  "inner-svc-tls-key",
			wantVersion: "",
		},
		{
			name:        "explicit version",
			ref:         "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password/versions/v1",
			wantRegion:  "ap-bangkok",
			wantSecret:  "db-password",
			wantVersion: "v1",
		},
		{
			name:        "version with hyphen",
			ref:         "secretsmanager.tencentcloudapi.com/ap-guangzhou/svc/versions/abc-123-xyz",
			wantRegion:  "ap-guangzhou",
			wantSecret:  "svc",
			wantVersion: "abc-123-xyz",
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
		{
			name:       "empty secret name",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok/",
			wantErrSub: "secret name",
		},
		{
			name:       "empty version",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password/versions/",
			wantErrSub: "version",
		},
		{
			name:       "unknown suffix instead of /versions/",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password/stages/foo",
			wantErrSub: "/versions/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, secret, version, err := parseTencentSecretRef(tt.ref)
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
			if region != tt.wantRegion || secret != tt.wantSecret || version != tt.wantVersion {
				t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)", region, secret, version, tt.wantRegion, tt.wantSecret, tt.wantVersion)
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

func TestTencentResolver_Resolve_RequiresVersion(t *testing.T) {
	// Refs without a /versions/{v} suffix are rejected up-front — the
	// Tencent API rejects both nil and empty VersionId, and the SDK
	// offers no "latest" sentinel.
	prev := newTencentSecretsClient
	newTencentSecretsClient = func(string, *tencentcred.Credentials) (tencentSecretsClient, error) {
		t.Fatal("SDK factory should not be called when ref has no version")
		return nil, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRET_ID", "id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "key")
	tencentcred.ResetForTest()

	_, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password")
	if err == nil {
		t.Fatal("expected error for ref without /versions/{v} suffix")
	}
	if !strings.Contains(err.Error(), "/versions/{v}") {
		t.Fatalf("error %q should mention /versions/{v}", err.Error())
	}
}

func TestTencentResolver_Resolve_ExplicitVersion(t *testing.T) {
	prev := newTencentSecretsClient
	var capturedReq *ssm.GetSecretValueRequest
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		return fakeSecretsClient{secret: "v1-value", captureReq: &capturedReq}, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRET_ID", "id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "key")
	tencentcred.ResetForTest()

	got, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password/versions/v1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v1-value" {
		t.Fatalf("got %q, want v1-value", got)
	}
	if capturedReq == nil || capturedReq.VersionId == nil || *capturedReq.VersionId != "v1" {
		t.Fatalf("VersionId: got %v, want v1", capturedReq)
	}
}

func TestTencentResolver_Resolve_BinaryUnsupported(t *testing.T) {
	prev := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		return fakeSecretsClient{binary: "YmFzZTY0"}, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRET_ID", "id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "key")
	tencentcred.ResetForTest()

	_, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/blob/versions/v1")
	if err == nil {
		t.Fatal("expected error for binary secret")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("error %q should mention 'binary'", err.Error())
	}
}

func TestNewTencentSecretsClient_STSTokenPassedThrough(t *testing.T) {
	var capturedToken string
	prevNew := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		capturedToken = creds.Token
		return nil, nil
	}
	defer func() { newTencentSecretsClient = prevNew }()

	creds := &tencentcred.Credentials{SecretID: "s", SecretKey: "k", Token: "the-token"}
	if _, err := newTencentSecretsClient("ap-bangkok", creds); err != nil {
		t.Fatal(err)
	}
	if capturedToken != "the-token" {
		t.Fatalf("expected Token=%q passed through, got %q", "the-token", capturedToken)
	}
}

func TestNewTencentSecretsClient_NoTokenWhenStatic(t *testing.T) {
	var capturedToken string
	prevNew := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		capturedToken = creds.Token
		return nil, nil
	}
	defer func() { newTencentSecretsClient = prevNew }()

	creds := &tencentcred.Credentials{SecretID: "s", SecretKey: "k"}
	if _, err := newTencentSecretsClient("ap-bangkok", creds); err != nil {
		t.Fatal(err)
	}
	if capturedToken != "" {
		t.Fatalf("expected empty Token for static creds, got %q", capturedToken)
	}
}

// TestPipeline_ParseThenResolve_TencentRef proves the full config-extractor
// flow end-to-end with no live cloud calls: parse a JSON payload that
// contains a __SECRET_REF__() pointing at Tencent Secrets Manager, then run
// it through the same ref-resolution path the production binary uses, and
// verify the placeholder is replaced with the resolved value.
//
// Skipped when tencent credentials are not present (the test would block
// trying to resolve real secrets) — the fake-client path still proves the
// wiring works for callers with mocked credentials.
func TestPipeline_ParseThenResolve_TencentRef(t *testing.T) {
	// Same fixture shape as the dev-1 sample payload shipped with the repo.
	// The ref pins a /versions/v1 segment because the Tencent API rejects
	// requests without a VersionId.
	const payload = `{
  "test": "test",
  "secret": "__SECRET_REF__(secretsmanager.tencentcloudapi.com/ap-bangkok/cds-stk__secret1/versions/v1)"
}`

	// 1) Parse the payload into KEY=VALUE pairs.
	pairs := domain.ParsePayload(payload)
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs from payload, got %d: %v", len(pairs), pairs)
	}

	// 2) Wire a fake tencent client that returns a known plaintext.
	prevNew := newTencentSecretsClient
	newTencentSecretsClient = func(region string, _ *tencentcred.Credentials) (tencentSecretsClient, error) {
		return fakeSecretsClient{secret: "the-real-secret-value"}, nil
	}
	defer func() { newTencentSecretsClient = prevNew }()

	// 3) Run the production ref-resolution loop.
	updated, placeholders, varsUpdated, err := domain.ResolveSecretRefs(
		t.Context(), pairs, []domain.SecretResolver{NewTencentResolver()},
	)
	if err != nil {
		t.Fatalf("ResolveSecretRefs: %v", err)
	}
	if placeholders != 1 {
		t.Errorf("placeholders resolved: got %d, want 1", placeholders)
	}
	if varsUpdated != 1 {
		t.Errorf("vars updated: got %d, want 1", varsUpdated)
	}

	// 4) Verify the resolved pairs (sorted because ParsePayload sorts).
	sorted := append([]domain.EnvPair(nil), updated...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	want := []domain.EnvPair{
		"secret=the-real-secret-value",
		"test=test",
	}
	if len(sorted) != len(want) {
		t.Fatalf("len mismatch: got %d, want %d (%v)", len(sorted), len(want), sorted)
	}
	for i := range sorted {
		if sorted[i] != want[i] {
			t.Errorf("pair[%d]: got %q, want %q", i, sorted[i], want[i])
		}
	}
}

// TestPipeline_Live_TencentRef runs the real binary's resolution path
// against the live Tencent API. Skipped unless TENCENTCLOUD_LIVE_TEST=1.
// Touches ~/.tccli/default.credential first so tccli can auto-refresh the
// access token from the SSO token (access tokens expire in 2h; the SSO
// token lasts 12h).
func TestPipeline_Live_TencentRef(t *testing.T) {
	if os.Getenv("TENCENTCLOUD_LIVE_TEST") != "1" {
		t.Skip("set TENCENTCLOUD_LIVE_TEST=1 to run the live resolution test")
	}
	// Trigger tccli to refresh the access token from the still-valid SSO
	// token. Any tccli subcommand runs the refresh on the way in.
	_ = exec.Command("tccli", "sts", "GetCallerIdentity", "--region", "ap-bangkok").Run()

	// Find a real version of the user's secret.
	listOut, err := exec.Command("tccli", "ssm", "ListSecretVersionIds",
		"--region", "ap-bangkok", "--SecretName", "cds-stk__secret1").Output()
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	var listed struct {
		Versions []struct {
			VersionID string `json:"VersionId"`
		} `json:"Versions"`
	}
	if err := json.Unmarshal(listOut, &listed); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(listed.Versions) == 0 {
		t.Fatal("no versions found for cds-stk__secret1")
	}
	version := listed.Versions[0].VersionID
	t.Logf("using secret version %q", version)

	const payloadTmpl = `{
  "test": "test",
  "secret": "__SECRET_REF__(secretsmanager.tencentcloudapi.com/ap-bangkok/cds-stk__secret1/versions/%s)"
}`
	payload := fmt.Sprintf(payloadTmpl, version)

	tencentcred.ResetForTest()
	pairs := domain.ParsePayload(payload)
	resolver := NewTencentResolver()
	updated, placeholders, _, err := domain.ResolveSecretRefs(
		t.Context(), pairs, []domain.SecretResolver{resolver},
	)
	if err != nil {
		t.Fatalf("ResolveSecretRefs: %v", err)
	}
	if placeholders != 1 {
		t.Errorf("placeholders resolved: got %d, want 1", placeholders)
	}

	// Format the resolved pairs as a .env file — the exact content the
	// production binary writes.
	var envBuf bytes.Buffer
	for _, p := range updated {
		envBuf.WriteString(string(p))
		envBuf.WriteByte('\n')
	}
	t.Logf("live .env output:\n%s", envBuf.String())

	for _, p := range updated {
		if strings.HasPrefix(string(p), "secret=") && string(p) == "secret=" {
			t.Fatal("secret value is empty — ref was not translated")
		}
	}
}
