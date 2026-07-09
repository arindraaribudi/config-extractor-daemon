package secrets

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ssm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
)

const tencentSecretPrefix = "secretsmanager.tencentcloudapi.com/"

// parseTencentSecretRef extracts region, secret name, and optional version
// from a Tencent Secrets Manager URI. The version segment is optional — when
// absent, Resolve uses tencentLatestVersion to satisfy the SDK's required
// VersionId parameter.
//
// Forms:
//
//	secretsmanager.tencentcloudapi.com/{region}/{name}
//	secretsmanager.tencentcloudapi.com/{region}/{name}/versions/{version}
func parseTencentSecretRef(ref string) (region, secretName, version string, err error) {
	path, ok := strings.CutPrefix(ref, tencentSecretPrefix)
	if !ok {
		return "", "", "", fmt.Errorf("tencent secret ref %q: missing %q prefix", ref, tencentSecretPrefix)
	}
	region, rest, ok := strings.Cut(path, "/")
	if !ok {
		return "", "", "", fmt.Errorf("tencent secret ref %q: expected {region}/{secret-name}[/versions/{v}] after prefix", ref)
	}
	if region == "" {
		return "", "", "", fmt.Errorf("tencent secret ref %q: region is empty", ref)
	}
	secretName, versionTail, _ := strings.Cut(rest, "/")
	if secretName == "" {
		return "", "", "", fmt.Errorf("tencent secret ref %q: secret name is empty", ref)
	}
	if versionTail != "" {
		ver, ok := strings.CutPrefix(versionTail, "versions/")
		if !ok {
			return "", "", "", fmt.Errorf("tencent secret ref %q: expected /versions/{v} suffix, got %q", ref, versionTail)
		}
		if ver == "" {
			return "", "", "", fmt.Errorf("tencent secret ref %q: version is empty", ref)
		}
		version = ver
	}
	return region, secretName, version, nil
}

// tencentSecretsClient is the subset of *ssm.Client we use. Defined here so
// tests can inject a fake without real Tencent credentials.
type tencentSecretsClient interface {
	GetSecretValue(request *ssm.GetSecretValueRequest) (*ssm.GetSecretValueResponse, error)
}

// newTencentSecretsClient builds a real Tencent Secrets Manager client.
// Overridden in tests.
var newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
	if creds.Token != "" {
		client, err := ssm.NewClient(common.NewTokenCredential(creds.SecretID, creds.SecretKey, creds.Token), region, profile.NewClientProfile())
		if err != nil {
			return nil, err
		}
		return client, nil
	}
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
	region, secretName, version, err := parseTencentSecretRef(ref)
	if err != nil {
		return "", err
	}

	if version == "" {
		// The Tencent API requires VersionId. Without it, both nil and
		// empty string are rejected (nil → "MissingParameter", "" →
		// "InvalidVersionId"). The SDK does not provide a "latest" sentinel.
		// Reject up-front so the user gets a clear, actionable error
		// instead of a 4xx from the API.
		return "", fmt.Errorf("tencent secretsmanager: ref for %q is missing /versions/{v} — pin a version (e.g. /versions/v1); the API does not accept a default-current sentinel", secretName)
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
		VersionId:  common.StringPtr(version),
	}

	log.Printf("tencent secretsmanager: fetching secret %q in region %q (version=%q)", secretName, region, version)
	resp, err := client.GetSecretValue(request)
	if err != nil {
		return "", fmt.Errorf("tencent secretsmanager: get secret %q (region=%s): %w", secretName, region, err)
	}
	return decodeSecretValue(resp, secretName)
}

// decodeSecretValue extracts the plaintext SecretString from a Tencent
// GetSecretValue response. Binary secrets are rejected — the extractor
// only writes KEY=VALUE pairs, so a base64-encoded blob would silently
// corrupt the output.
func decodeSecretValue(resp *ssm.GetSecretValueResponse, secretName string) (string, error) {
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
	secret     string
	binary     string
	err        error
	captureReq **ssm.GetSecretValueRequest
}

func (f fakeSecretsClient) GetSecretValue(request *ssm.GetSecretValueRequest) (*ssm.GetSecretValueResponse, error) {
	if f.captureReq != nil {
		*f.captureReq = request
	}
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
