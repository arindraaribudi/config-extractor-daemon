package secrets

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	ssm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
)

const tencentSecretPrefix = "secretsmanager.tencentcloudapi.com/"

// parseTencentSecretRef extracts region and secret name from a Tencent
// Secrets Manager URI of the form secretsmanager.tencentcloudapi.com/{region}/{name}.
func parseTencentSecretRef(ref string) (region, secretName string, err error) {
	path, ok := strings.CutPrefix(ref, tencentSecretPrefix)
	if !ok {
		return "", "", fmt.Errorf("tencent secret ref %q: missing %q prefix", ref, tencentSecretPrefix)
	}
	region, secretName, ok = strings.Cut(path, "/")
	if !ok {
		return "", "", fmt.Errorf("tencent secret ref %q: expected {region}/{secret-name} after prefix", ref)
	}
	if region == "" {
		return "", "", fmt.Errorf("tencent secret ref %q: region is empty", ref)
	}
	if secretName == "" {
		return "", "", fmt.Errorf("tencent secret ref %q: secret name is empty", ref)
	}
	return region, secretName, nil
}

// tencentSecretsClient is the subset of *ssm.Client we use. Defined here so
// tests can inject a fake without real Tencent credentials.
type tencentSecretsClient interface {
	GetSecretValue(request *ssm.GetSecretValueRequest) (*ssm.GetSecretValueResponse, error)
}

// newTencentSecretsClient builds a real Tencent Secrets Manager client.
// Overridden in tests.
var newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
	client, err := ssm.NewClientWithSecretId(creds.SecretID, creds.SecretKey, region)
	if err != nil {
		return nil, err
	}
	return client, nil
}

type tencentResolver struct {
	client tencentSecretsClient
}

func NewTencentResolver() domain.SecretResolver {
	return &tencentResolver{}
}

func (r *tencentResolver) Supports(ref string) bool {
	return strings.HasPrefix(ref, tencentSecretPrefix)
}

func (r *tencentResolver) Resolve(ctx context.Context, ref string) (string, error) {
	region, secretName, err := parseTencentSecretRef(ref)
	if err != nil {
		return "", err
	}

	client := r.client
	if client == nil {
		creds, err := tencentcred.Resolve(ctx)
		if err != nil {
			return "", fmt.Errorf("tencent secretsmanager: resolve credentials: %w", err)
		}
		c, err := newTencentSecretsClient(region, creds)
		if err != nil {
			return "", fmt.Errorf("tencent secretsmanager: build client: %w", err)
		}
		client = c
	}

	request := &ssm.GetSecretValueRequest{
		SecretName: common.StringPtr(secretName),
	}

	log.Printf("tencent secretsmanager: fetching secret %q in region %q", secretName, region)
	resp, err := client.GetSecretValue(request)
	if err != nil {
		return "", fmt.Errorf("tencent secretsmanager: get secret %q (region=%s): %w", secretName, region, err)
	}
	if resp == nil || resp.Response == nil {
		return "", fmt.Errorf("tencent secretsmanager: empty response for %q", secretName)
	}
	// Tencent returns SecretBinary as base64-encoded *string (not []byte).
	if resp.Response.SecretBinary != nil && *resp.Response.SecretBinary != "" {
		return "", fmt.Errorf("tencent secretsmanager: secret %q is binary; binary secrets are not supported", secretName)
	}
	if resp.Response.SecretString == nil {
		return "", fmt.Errorf("tencent secretsmanager: secret %q has no SecretString", secretName)
	}
	return *resp.Response.SecretString, nil
}

// Test fakes — defined here because the test file imports these names.
type fakeSecretsClient struct {
	secret string
	binary string
	err    error
}

func (f fakeSecretsClient) GetSecretValue(request *ssm.GetSecretValueRequest) (*ssm.GetSecretValueResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	resp := &ssm.GetSecretValueResponse{Response: &ssm.GetSecretValueResponseParams{}}
	if f.secret != "" {
		resp.Response.SecretString = common.StringPtr(f.secret)
	}
	if f.binary != "" {
		resp.Response.SecretBinary = common.StringPtr(f.binary)
	}
	return resp, nil
}
