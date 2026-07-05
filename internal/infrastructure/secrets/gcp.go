package secrets

import (
	"context"
	"fmt"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tls"
)

const gcpSecretManagerPrefix = "secretmanager.googleapis.com/"

// newSecretManagerClient builds a real *secretmanager.Client. Overridable
// from tests to force the error branch in ensureClient.
var newSecretManagerClient = func(ctx context.Context) (secretAccessClient, error) {
	c, err := secretmanager.NewClient(ctx, tls.GRPCOption())
	if err != nil {
		return nil, err
	}
	return c, nil
}

// secretAccessClient is the subset of *secretmanager.Client that gcpResolver
// uses. Defined here so tests can inject a fake without real GCP credentials.
type secretAccessClient interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
}

// gcpResolver resolves __SECRET_REF__() tokens pointing at GCP Secret Manager.
//
// Ref format:
//
//	secretmanager.googleapis.com/projects/{project}/secrets/{secret}/versions/{version}
type gcpResolver struct {
	client secretAccessClient
}

func NewGCPResolver() domain.SecretResolver {
	// Client is constructed lazily inside Resolve so that constructing the
	// resolver (which happens at startup, in main) does not require GCP
	// credentials to be present yet.
	return &gcpResolver{}
}

func (g *gcpResolver) Supports(ref string) bool {
	return strings.HasPrefix(ref, gcpSecretManagerPrefix)
}

func (g *gcpResolver) Resolve(ctx context.Context, ref string) (string, error) {
	name := strings.TrimPrefix(ref, gcpSecretManagerPrefix)

	client, err := g.ensureClient(ctx)
	if err != nil {
		return "", err
	}

	resp, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: name})
	if err != nil {
		return "", fmt.Errorf("access secret version %q: %w", name, err)
	}
	return string(resp.GetPayload().GetData()), nil
}

func (g *gcpResolver) ensureClient(ctx context.Context) (secretAccessClient, error) {
	if g.client != nil {
		return g.client, nil
	}
	c, err := newSecretManagerClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcp secret manager client: %w", err)
	}
	g.client = c
	return c, nil
}
