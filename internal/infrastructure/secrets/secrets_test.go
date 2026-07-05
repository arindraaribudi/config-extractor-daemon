package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	gax "github.com/googleapis/gax-go/v2"
)

// fakeSecretClient implements secretAccessClient for tests.
type fakeSecretClient struct {
	resp *secretmanagerpb.AccessSecretVersionResponse
	err  error
}

func (f *fakeSecretClient) AccessSecretVersion(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	return f.resp, f.err
}

func TestParseAWSSecretRef_LegacyErrors(t *testing.T) {
	cases := []string{
		"secretsmanager.amazonaws.com/",                    // empty region+secret
		"secretsmanager.us-east-1.amazonaws.com/x",         // missing /secrets/
		"secretsmanager.us-east-1.amazonaws.com/secrets/x", // wrong segment name
	}
	for _, ref := range cases {
		_, _, err := parseAWSSecretRef(ref)
		if err == nil {
			t.Errorf("parseAWSSecretRef(%q): expected error, got nil", ref)
		}
	}
}

func TestParseAWSSecretRef_LegacySuccess(t *testing.T) {
	region, sid, err := parseAWSSecretRef("secretsmanager.amazonaws.com/eu-west-2/db-pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if region != "eu-west-2" || sid != "db-pass" {
		t.Errorf("got (%q,%q)", region, sid)
	}
}

func TestParseAWSSecretRef_RegionalSuccess(t *testing.T) {
	region, sid, err := parseAWSSecretRef("secretsmanager.us-east-1.amazonaws.com/projects/123456789012/secrets/pat-xxx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if region != "us-east-1" || sid != "pat-xxx" {
		t.Errorf("got (%q,%q)", region, sid)
	}
}

func TestParseAWSSecretRef_RegionalErrors(t *testing.T) {
	cases := []string{
		"secretsmanager.us-east-1.amazonaws.com/projects/123/no-secrets",
		"secretsmanager.us-east-1.amazonaws.com/no-projects-prefix",
		"bogus",
	}
	for _, ref := range cases {
		_, _, err := parseAWSSecretRef(ref)
		if err == nil {
			t.Errorf("parseAWSSecretRef(%q): expected error, got nil", ref)
		}
	}
}

func TestGCPSecretRefPrefix(t *testing.T) {
	if !strings.HasPrefix(gcpSecretManagerPrefix, "secretmanager") {
		t.Error("prefix should mention secretmanager")
	}
	r := NewGCPResolver()
	if r.Supports("not-gcp-prefix") {
		t.Error("non-gcp ref should not be supported")
	}
	if !r.Supports(gcpSecretManagerPrefix + "x") {
		t.Error("gcp-prefixed ref should be supported")
	}
}

func TestRegistryDispatch(t *testing.T) {
	if len(Resolvers) < 2 {
		t.Fatalf("expected ≥2 resolvers, got %d", len(Resolvers))
	}
	cases := []struct {
		ref      string
		provider string
	}{
		{"secretmanager.googleapis.com/projects/p/secrets/s/versions/1", "gcp"},
		{"secretsmanager.amazonaws.com/us-east-1/x", "aws"},
	}
	for _, c := range cases {
		var matched string
		for _, r := range Resolvers {
			if r.Resolver.Supports(c.ref) {
				matched = string(r.Kind)
				break
			}
		}
		if matched != c.provider {
			t.Errorf("ref %q matched %q, want %q", c.ref, matched, c.provider)
		}
	}
}

func TestGCPResolver_Resolve_NoCredentials(t *testing.T) {
	r := NewGCPResolver()
	_, err := r.Resolve(context.Background(), gcpSecretManagerPrefix+"projects/p/secrets/s/versions/1")
	if err == nil {
		t.Fatal("expected error from GCP client init without creds")
	}
}

func TestAWSResolver_Resolve_BadRef(t *testing.T) {
	r := NewAWSResolver()
	if _, err := r.Resolve(context.Background(), "bogus"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestAWSResolver_Resolve_NoRegion(t *testing.T) {
	r := NewAWSResolver()
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	// Regional format parses OK; config.LoadDefaultConfig should still error
	// because there is no region to load.
	if _, err := r.Resolve(context.Background(), "secretsmanager.us-east-1.amazonaws.com/projects/123/secrets/s"); err == nil {
		t.Fatal("expected region/config error")
	}
}

// newSMTestServer starts an httptest server that fakes the Secrets Manager
// GetSecretValue endpoint. The caller registers handlers by mutating the
// returned closure before calling Resolve. AWS_ENDPOINT_URL redirects the
// SDK client to this server.
func newSMTestServer(t *testing.T, handle func(action string, body []byte) (int, any)) string {
	t.Helper()
	// Stub creds so the SDK doesn't try real IMDS/process providers
	// (which time out under tests and flake the suite).
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			tmp := make([]byte, 256)
			for {
				n, err := r.Body.Read(tmp)
				body = append(body, tmp[:n]...)
				if err != nil {
					break
				}
			}
		}
		status, payload := handle(r.Header.Get("X-Amz-Target"), body)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAWSResolver_Resolve_HappyPath(t *testing.T) {
	url := newSMTestServer(t, func(action string, _ []byte) (int, any) {
		if action != "secretsmanager.GetSecretValue" {
			t.Errorf("unexpected action: %q", action)
		}
		return http.StatusOK, map[string]any{
			"ARN":          "arn:aws:secretsmanager:us-east-1:123:secret:db-pass-AbCdEf",
			"Name":         "db-pass",
			"VersionId":    "v1",
			"SecretString": "hunter2",
		}
	})
	t.Setenv("AWS_ENDPOINT_URL", url)
	t.Setenv("AWS_REGION", "us-east-1")

	got, err := NewAWSResolver().Resolve(context.Background(),
		"secretsmanager.amazonaws.com/us-east-1/db-pass")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestAWSResolver_Resolve_BinarySecret(t *testing.T) {
	url := newSMTestServer(t, func(_ string, _ []byte) (int, any) {
		return http.StatusOK, map[string]any{
			"ARN":          "arn:aws:secretsmanager:us-east-1:123:secret:binary-secret-AbCdEf",
			"Name":         "binary-secret",
			"VersionId":    "v1",
			"SecretBinary": []byte{0x00, 0x01, 0x02},
		}
	})
	t.Setenv("AWS_ENDPOINT_URL", url)
	t.Setenv("AWS_REGION", "us-east-1")

	_, err := NewAWSResolver().Resolve(context.Background(),
		"secretsmanager.amazonaws.com/us-east-1/binary-secret")
	if err == nil {
		t.Fatal("expected binary-not-supported error")
	}
	if !strings.Contains(err.Error(), "binary secrets are not supported") {
		t.Errorf("expected binary error, got %v", err)
	}
}

func TestAWSResolver_Resolve_HTTP500(t *testing.T) {
	url := newSMTestServer(t, func(_ string, _ []byte) (int, any) {
		return http.StatusInternalServerError, map[string]any{
			"__type":  "InternalServiceError",
			"message": "service unavailable",
		}
	})
	t.Setenv("AWS_ENDPOINT_URL", url)
	t.Setenv("AWS_REGION", "us-east-1")

	_, err := NewAWSResolver().Resolve(context.Background(),
		"secretsmanager.amazonaws.com/us-east-1/db-pass")
	if err == nil {
		t.Fatal("expected wrapped error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "aws get secret") {
		t.Errorf("expected wrapped SDK error, got %v", err)
	}
}

// Verify the AWS resolver hands off cleanly via the full SDK client
// construction path with a custom endpoint resolver. This exercises
// LoadDefaultConfig + NewFromConfig directly.
func TestAWSResolver_ConstructClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"SecretString": "x"})
	}))
	t.Cleanup(srv.Close)

	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test", Source: "test"}, nil
		})),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(_, _ string, _ ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: srv.URL, SigningRegion: "us-east-1", HostnameImmutable: true}, nil
			},
		)),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	client := secretsmanager.NewFromConfig(cfg)
	out, err := client.GetSecretValue(t.Context(), &secretsmanager.GetSecretValueInput{
		SecretId: aws.String("db-pass"),
	})
	if err != nil {
		t.Fatalf("GetSecretValue: %v", err)
	}
	if out.SecretString == nil || *out.SecretString != "x" {
		t.Errorf("got %v", out.SecretString)
	}
}

func TestGCPResolver_Resolve_HappyPath(t *testing.T) {
	r := NewGCPResolver().(*gcpResolver)
	r.client = &fakeSecretClient{
		resp: &secretmanagerpb.AccessSecretVersionResponse{
			Payload: &secretmanagerpb.SecretPayload{Data: []byte("hunter2")},
		},
	}
	got, err := r.Resolve(context.Background(),
		gcpSecretManagerPrefix+"projects/p/secrets/db-pass/versions/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestGCPResolver_Resolve_Error(t *testing.T) {
	r := NewGCPResolver().(*gcpResolver)
	r.client = &fakeSecretClient{err: errors.New("rpc-deny")}
	_, err := r.Resolve(context.Background(),
		gcpSecretManagerPrefix+"projects/p/secrets/db-pass/versions/1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "access secret version") || !strings.Contains(err.Error(), "rpc-deny") {
		t.Errorf("expected wrapped 'access secret version' + 'rpc-deny', got %v", err)
	}
}

func TestGCPResolver_Resolve_NilPayload(t *testing.T) {
	// nil Payload: GetData() is safe on nil → returns "" (no panic).
	r := NewGCPResolver().(*gcpResolver)
	r.client = &fakeSecretClient{resp: &secretmanagerpb.AccessSecretVersionResponse{}}
	got, err := r.Resolve(context.Background(),
		gcpSecretManagerPrefix+"projects/p/secrets/db-pass/versions/1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestGCPResolver_ensureClient_ReusesExisting(t *testing.T) {
	r := NewGCPResolver().(*gcpResolver)
	r.client = &fakeSecretClient{}
	first, err := r.ensureClient(context.Background())
	if err != nil {
		t.Fatalf("ensureClient: %v", err)
	}
	second, err := r.ensureClient(context.Background())
	if err != nil {
		t.Fatalf("ensureClient (2nd): %v", err)
	}
	if first != second {
		t.Errorf("ensureClient returned different clients on reuse")
	}
}

func TestGCPResolver_ensureClient_ConstructorError(t *testing.T) {
	// Override the package-level client constructor to force the error branch.
	orig := newSecretManagerClient
	newSecretManagerClient = func(_ context.Context) (secretAccessClient, error) {
		return nil, errors.New("forced-construct-fail")
	}
	defer func() { newSecretManagerClient = orig }()

	r := NewGCPResolver().(*gcpResolver)
	_, err := r.Resolve(context.Background(),
		gcpSecretManagerPrefix+"projects/p/secrets/db-pass/versions/1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gcp secret manager client") || !strings.Contains(err.Error(), "forced-construct-fail") {
		t.Errorf("expected wrapped construct error, got %v", err)
	}
}
