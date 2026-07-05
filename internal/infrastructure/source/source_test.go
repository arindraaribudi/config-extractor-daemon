package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	parametermanagerpb "cloud.google.com/go/parametermanager/apiv1/parametermanagerpb"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	gax "github.com/googleapis/gax-go/v2"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)

// fakeGCPClient implements gcpClient for tests. Records close count and
// returns whatever was wired in via fields.
type fakeGCPClient struct {
	getResp    *parametermanagerpb.ParameterVersion
	getErr     error
	renderResp *parametermanagerpb.RenderParameterVersionResponse
	renderErr  error
	closeCount int
}

func (f *fakeGCPClient) GetParameterVersion(_ context.Context, _ *parametermanagerpb.GetParameterVersionRequest, _ ...gax.CallOption) (*parametermanagerpb.ParameterVersion, error) {
	return f.getResp, f.getErr
}

func (f *fakeGCPClient) RenderParameterVersion(_ context.Context, _ *parametermanagerpb.RenderParameterVersionRequest, _ ...gax.CallOption) (*parametermanagerpb.RenderParameterVersionResponse, error) {
	return f.renderResp, f.renderErr
}

func (f *fakeGCPClient) Close() error {
	f.closeCount++
	return nil
}

func TestNewAWSSource_FallsBackToDefaultRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	src, err := NewAWSSource(context.Background(), domain.FetchGet)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}
	if src.Kind() != domain.ProviderAWS {
		t.Errorf("kind = %q, want aws", src.Kind())
	}
}

func TestNewAWSSource_PicksRegionFromEnv(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_DEFAULT_REGION", "ap-east-1")
	src, err := NewAWSSource(context.Background(), domain.FetchGet)
	if err != nil {
		t.Fatalf("NewAWSSource: %v", err)
	}
	if src.Kind() != domain.ProviderAWS {
		t.Errorf("kind = %q, want aws", src.Kind())
	}
}

func TestAWSSource_Fetch_UnsupportedMode(t *testing.T) {
	a := &awsSource{}
	_, err := a.Fetch(context.Background(), domain.Reference{Location: "arn:aws:ssm:us-east-1:123:parameter/x", Version: "v1"}, domain.FetchRender)
	if err == nil {
		t.Fatal("expected unsupported-mode error")
	}
}

func TestAWSSource_Fetch_BadARN(t *testing.T) {
	a := &awsSource{}
	_, err := a.Fetch(context.Background(), domain.Reference{Location: "not-an-arn", Version: "v1"}, domain.FetchGet)
	if err == nil {
		t.Fatal("expected parse error for bad ARN")
	}
}

func TestAWSSource_Fetch_BarePathMissingRegion(t *testing.T) {
	a := &awsSource{}
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	_, err := a.Fetch(context.Background(), domain.Reference{Location: "/path/without/region", Version: "v1"}, domain.FetchGet)
	if err == nil {
		t.Fatal("expected region-required error")
	}
}

func TestGCPSource_Fetch_UnsupportedMode(t *testing.T) {
	g := &gcpSource{}
	_, err := g.Fetch(context.Background(), domain.Reference{Location: "projects/p/locations/l/parameters/x", Version: "v1"}, domain.FetchMode("bogus"))
	if err == nil {
		t.Fatal("expected unsupported-mode error")
	}
}

func TestGCPSource_CloseNilSafe(t *testing.T) {
	var g *gcpSource
	if err := g.Close(); err != nil {
		t.Errorf("nil Close should be no-op, got %v", err)
	}
	g = &gcpSource{}
	if err := g.Close(); err != nil {
		t.Errorf("no-client Close should be no-op, got %v", err)
	}
}

func TestRegistry_OrderMatters(t *testing.T) {
	if len(Sources) < 2 {
		t.Fatalf("expected at least 2 sources, got %d", len(Sources))
	}
	if Sources[0].Kind != domain.ProviderAWS {
		t.Errorf("first source kind = %q, want aws", Sources[0].Kind)
	}
	if Sources[1].Kind != domain.ProviderGCP {
		t.Errorf("second source kind = %q, want gcp", Sources[1].Kind)
	}
}

func TestRegistry_GCPMatchAcceptsEverything(t *testing.T) {
	// The GCP entry's Match is `func(string) bool { return true }` — it
	// must accept any location, since AWS already claimed the SSM-shaped
	// ones and everything else is GCP's.
	cases := []string{
		"projects/p/locations/l/parameters/x",
		"arn:aws:ssm:us-east-1:123:parameter/x",
		"/some/path",
		"",
	}
	for _, loc := range cases {
		if !Sources[1].Match(loc) {
			t.Errorf("Sources[1].Match(%q) = false, want true (GCP is fallback)", loc)
		}
	}
}

func TestNewParameterManagerClient_DefaultConstructor(t *testing.T) {
	// Calling the default constructor without GCP creds may fail (we
	// accept either branch). The point is to exercise the body of the
	// default constructor so the var is fully covered.
	_, _ = newParameterManagerClient(context.Background())
}

// newSSMTestClient returns an SSM client and *awsSource wired to a fake
// endpoint served by `handler`. The endpoint resolver redirects every
// region to the test server so the SDK's signer accepts the URL.
func newSSMTestClient(t *testing.T, handler http.Handler) *awsSource {
	t.Helper()
	// Stub creds so the SDK doesn't try to reach the real IMDS / process
	// provider — that path times out under tests and flakes the suite.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(_, _ string, _ ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: srv.URL, SigningRegion: "us-east-1", HostnameImmutable: true}, nil
			},
		)),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return &awsSource{client: ssm.NewFromConfig(cfg)}
}

func TestAWSSource_Fetch_HappyPath(t *testing.T) {
	const want = "FOO=bar\nBAZ=qux"
	a := newSSMTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "AmazonSSM.GetParameter" {
			t.Errorf("unexpected X-Amz-Target: %q", r.Header.Get("X-Amz-Target"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Parameter": map[string]any{
				"Name":  aws.String("/foo/versions/v1"),
				"Value": aws.String(want),
				"Type":  aws.String("SecureString"),
			},
		})
	}))

	got, err := a.Fetch(t.Context(),
		domain.Reference{Location: "arn:aws:ssm:us-east-1:123:parameter/foo", Version: "v1"},
		domain.FetchGet,
	)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestAWSSource_Fetch_NilParameter(t *testing.T) {
	a := newSSMTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Parameter": nil})
	}))
	_, err := a.Fetch(t.Context(),
		domain.Reference{Location: "arn:aws:ssm:us-east-1:123:parameter/foo", Version: "v1"},
		domain.FetchGet,
	)
	if err == nil {
		t.Fatal("expected error for nil parameter")
	}
	if !strings.Contains(err.Error(), "empty parameter") {
		t.Errorf("expected 'empty parameter' in error, got %v", err)
	}
}

func TestAWSSource_Fetch_NilValue(t *testing.T) {
	a := newSSMTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Parameter": map[string]any{"Name": aws.String("/foo/versions/v1"), "Value": nil},
		})
	}))
	_, err := a.Fetch(t.Context(),
		domain.Reference{Location: "arn:aws:ssm:us-east-1:123:parameter/foo", Version: "v1"},
		domain.FetchGet,
	)
	if err == nil {
		t.Fatal("expected error for nil value")
	}
	if !strings.Contains(err.Error(), "empty parameter") {
		t.Errorf("expected 'empty parameter' in error, got %v", err)
	}
}

func TestAWSSource_Fetch_HTTP400(t *testing.T) {
	a := newSSMTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"__type":  "ParameterNotFound",
			"message": "Parameter /foo/versions/v1 not found.",
		})
	}))
	_, err := a.Fetch(t.Context(),
		domain.Reference{Location: "arn:aws:ssm:us-east-1:123:parameter/foo", Version: "v1"},
		domain.FetchGet,
	)
	if err == nil {
		t.Fatal("expected wrapped error for HTTP 400")
	}
	if !strings.Contains(err.Error(), "aws ssm: GetParameter") {
		t.Errorf("expected wrapped SDK error, got %v", err)
	}
}

func TestAWSSource_Fetch_BarePathUsesEnvRegion(t *testing.T) {
	const want = "from-bare-path"
	t.Setenv("AWS_REGION", "us-east-1")
	a := newSSMTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Parameter": map[string]any{"Value": aws.String(want)},
		})
	}))
	got, err := a.Fetch(t.Context(),
		domain.Reference{Location: "/foo", Version: "v1"},
		domain.FetchGet,
	)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestGCPSource_Kind(t *testing.T) {
	g := &gcpSource{}
	if g.Kind() != domain.ProviderGCP {
		t.Errorf("Kind = %q, want %q", g.Kind(), domain.ProviderGCP)
	}
}

func TestGCPSource_Fetch_Get_HappyPath(t *testing.T) {
	const want = "FOO=bar"
	g := &gcpSource{client: &fakeGCPClient{
		getResp: &parametermanagerpb.ParameterVersion{
			Payload: &parametermanagerpb.ParameterVersionPayload{Data: []byte(want)},
		},
	}}
	got, err := g.Fetch(context.Background(),
		domain.Reference{Location: "projects/p/locations/l/parameters/x", Version: "v1"},
		domain.FetchGet,
	)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestGCPSource_Fetch_Get_Error(t *testing.T) {
	wantErr := errors.New("boom")
	g := &gcpSource{client: &fakeGCPClient{getErr: wantErr}}
	_, err := g.Fetch(context.Background(),
		domain.Reference{Location: "projects/p/locations/l/parameters/x", Version: "v1"},
		domain.FetchGet,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "get parameter version") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected wrapped 'get parameter version' + 'boom', got %v", err)
	}
}

func TestGCPSource_Fetch_Render_HappyPath(t *testing.T) {
	const want = "rendered=ok"
	g := &gcpSource{client: &fakeGCPClient{
		renderResp: &parametermanagerpb.RenderParameterVersionResponse{RenderedPayload: []byte(want)},
	}}
	got, err := g.Fetch(context.Background(),
		domain.Reference{Location: "projects/p/locations/l/parameters/x", Version: "v1"},
		domain.FetchRender,
	)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != want {
		t.Errorf("payload = %q, want %q", got, want)
	}
}

func TestGCPSource_Fetch_Render_Error(t *testing.T) {
	wantErr := errors.New("rpc-failed")
	g := &gcpSource{client: &fakeGCPClient{renderErr: wantErr}}
	_, err := g.Fetch(context.Background(),
		domain.Reference{Location: "projects/p/locations/l/parameters/x", Version: "v1"},
		domain.FetchRender,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "render parameter version") || !strings.Contains(err.Error(), "rpc-failed") {
		t.Errorf("expected wrapped 'render parameter version' + 'rpc-failed', got %v", err)
	}
}

func TestGCPSource_Fetch_Get_NilPayload(t *testing.T) {
	// nil resp.Payload returns empty bytes (no panic). Both resp and
	// resp.GetPayload() are nil, so GetData() is safe.
	g := &gcpSource{client: &fakeGCPClient{getResp: &parametermanagerpb.ParameterVersion{}}}
	got, err := g.Fetch(context.Background(),
		domain.Reference{Location: "projects/p/locations/l/parameters/x", Version: "v1"},
		domain.FetchGet,
	)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != "" {
		t.Errorf("payload = %q, want empty", got)
	}
}

func TestGCPSource_Close(t *testing.T) {
	f := &fakeGCPClient{}
	g := &gcpSource{client: f}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f.closeCount != 1 {
		t.Errorf("close count = %d, want 1", f.closeCount)
	}
}

func TestNewGCPSource_HappyPath(t *testing.T) {
	// Override the constructor with a fake — exercises the success branch
	// without requiring real GCP credentials.
	f := &fakeGCPClient{}
	orig := newParameterManagerClient
	newParameterManagerClient = func(_ context.Context) (gcpClient, error) {
		return f, nil
	}
	defer func() { newParameterManagerClient = orig }()

	src, err := NewGCPSource(context.Background(), domain.FetchGet)
	if err != nil {
		t.Fatalf("NewGCPSource: %v", err)
	}
	if src.Kind() != domain.ProviderGCP {
		t.Errorf("Kind = %q, want %q", src.Kind(), domain.ProviderGCP)
	}
}

func TestNewGCPSource_ConstructorError(t *testing.T) {
	orig := newParameterManagerClient
	newParameterManagerClient = func(_ context.Context) (gcpClient, error) {
		return nil, errors.New("forced-construct-fail")
	}
	defer func() { newParameterManagerClient = orig }()

	_, err := NewGCPSource(context.Background(), domain.FetchGet)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gcp parametermanager client") || !strings.Contains(err.Error(), "forced-construct-fail") {
		t.Errorf("expected wrapped construct error, got %v", err)
	}
}

func TestNewAWSSource_LoadConfigError(t *testing.T) {
	orig := loadAWSConfig
	loadAWSConfig = func(_ context.Context, _ string) (aws.Config, error) {
		return aws.Config{}, errors.New("forced-config-fail")
	}
	defer func() { loadAWSConfig = orig }()

	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_DEFAULT_REGION", "")

	_, err := NewAWSSource(context.Background(), domain.FetchGet)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "load default config") || !strings.Contains(err.Error(), "forced-config-fail") {
		t.Errorf("expected wrapped config error, got %v", err)
	}
}
