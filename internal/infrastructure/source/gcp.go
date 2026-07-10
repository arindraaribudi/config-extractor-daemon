package source

import (
	"context"
	"fmt"
	"log"
	"os"

	parametermanager "cloud.google.com/go/parametermanager/apiv1"
	parametermanagerpb "cloud.google.com/go/parametermanager/apiv1/parametermanagerpb"
	gax "github.com/googleapis/gax-go/v2"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tls"
)

// gcpClient is the subset of *parametermanager.Client that gcpSource uses.
// Defined here so tests can inject a fake without real GCP credentials.
type gcpClient interface {
	GetParameterVersion(ctx context.Context, req *parametermanagerpb.GetParameterVersionRequest, opts ...gax.CallOption) (*parametermanagerpb.ParameterVersion, error)
	RenderParameterVersion(ctx context.Context, req *parametermanagerpb.RenderParameterVersionRequest, opts ...gax.CallOption) (*parametermanagerpb.RenderParameterVersionResponse, error)
	Close() error
}

// newParameterManagerClient builds a real *parametermanager.Client. Overridable
// from tests to force the error branch in NewGCPSource.
var newParameterManagerClient = func(ctx context.Context) (gcpClient, error) {
	c, err := parametermanager.NewClient(ctx, tls.GRPCOption())
	if err != nil {
		return nil, err
	}
	return c, nil
}

// DetectGCPCredSource reports the most likely GCP Application Default
// Credentials (ADC) source in use. Heuristic — the GCP SDK resolves ADC
// transparently and does not expose which provider yielded creds, so we
// inspect the env vars ADC looks at. Priority (first match wins):
//
//	GOOGLE_APPLICATION_CREDENTIALS → service-account-key-file
//	KUBERNETES_SERVICE_HOST (no SA key) → gke-workload-identity
//	else → adc-default (GCE metadata server, gcloud user creds, etc.)
func DetectGCPCredSource(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		return "service-account-key-file"
	}
	if getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "gke-workload-identity"
	}
	return "adc-default"
}

// gcpSource fetches payloads from GCP Parameter Manager.
//
// Supports both `get` (raw stored payload) and `render` (template-rendered
// payload) modes. The client is created on first Fetch and reused.
type gcpSource struct {
	client gcpClient
}

func NewGCPSource(ctx context.Context, _ domain.FetchMode) (domain.ConfigSource, error) {
	client, err := newParameterManagerClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcp parametermanager client: %w", err)
	}
	log.Printf("gcp creds: source=%s", DetectGCPCredSource(os.Getenv))
	return &gcpSource{client: client}, nil
}

func (g *gcpSource) Kind() domain.ProviderKind { return domain.ProviderGCP }

func (g *gcpSource) Fetch(ctx context.Context, ref domain.Reference, mode domain.FetchMode) (domain.Payload, error) {
	name := ref.Location + "/versions/" + ref.Version

	switch mode {
	case domain.FetchGet:
		resp, err := g.client.GetParameterVersion(ctx, &parametermanagerpb.GetParameterVersionRequest{Name: name})
		if err != nil {
			return "", fmt.Errorf("get parameter version %q: %w", name, err)
		}
		return domain.Payload(resp.GetPayload().GetData()), nil

	case domain.FetchRender:
		resp, err := g.client.RenderParameterVersion(ctx, &parametermanagerpb.RenderParameterVersionRequest{Name: name})
		if err != nil {
			return "", fmt.Errorf("render parameter version %q: %w", name, err)
		}
		return domain.Payload(resp.GetRenderedPayload()), nil

	default:
		return "", fmt.Errorf("%w: %q (use %q or %q)", domain.ErrUnsupportedMode, mode, domain.FetchGet, domain.FetchRender)
	}
}

// Close releases the underlying gRPC connection. Safe to call on nil.
func (g *gcpSource) Close() error {
	if g == nil || g.client == nil {
		return nil
	}
	return g.client.Close()
}
