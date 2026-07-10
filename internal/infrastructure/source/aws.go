package source

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tls"
)

// awsSource fetches parameters from AWS SSM Parameter Store. Only the
// "get" mode is supported (SSM has no template-rendering equivalent).
type awsSource struct {
	client *ssm.Client
}

// loadAWSConfig builds the *aws.Config used by SSM. Overridable in tests
// so the LoadDefaultConfig error branch can be exercised without a real
// broken AWS environment.
var loadAWSConfig = func(ctx context.Context, region string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithHTTPClient(tls.HTTPClient()),
	)
}

// DetectAWSCredSource reports the most likely AWS credential source in
// use, based on environment variables. Heuristic — the SDK does not
// expose which provider in the default chain actually resolved, so this
// inspects the same env vars AWS SDK providers look at and returns the
// first match. Priority (first match wins):
//
//	AWS_WEB_IDENTITY_TOKEN_FILE + AWS_ROLE_ARN → irsa-web-identity (EKS pod identity)
//	AWS_CONTAINER_CREDENTIALS_RELATIVE_URI/FULL_URI → ecs-container-role
//	AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY → env-vars
//	AWS_PROFILE → shared-config (~/.aws/credentials)
//	else → imds-v2 (EC2 instance metadata, default chain tail)
func DetectAWSCredSource(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" && getenv("AWS_ROLE_ARN") != "" {
		return "irsa-web-identity"
	}
	if getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return "ecs-container-role"
	}
	if getenv("AWS_ACCESS_KEY_ID") != "" && getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return "env-vars"
	}
	if getenv("AWS_PROFILE") != "" {
		return "shared-config"
	}
	return "imds-v2"
}

func NewAWSSource(ctx context.Context, _ domain.FetchMode) (domain.ConfigSource, error) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		// Fall back to the default config chain; AWS_REGION is preferred
		// because bare parameter paths encode no region.
		region = os.Getenv("AWS_DEFAULT_REGION")
	}

	cfg, err := loadAWSConfig(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("aws ssm: load default config: %w", err)
	}

	log.Printf("aws creds: source=%s region=%s", DetectAWSCredSource(os.Getenv), region)
	return &awsSource{client: ssm.NewFromConfig(cfg)}, nil
}

func (a *awsSource) Kind() domain.ProviderKind { return domain.ProviderAWS }

func (a *awsSource) Fetch(ctx context.Context, ref domain.Reference, mode domain.FetchMode) (domain.Payload, error) {
	if mode != domain.FetchGet {
		return "", fmt.Errorf("%w: AWS SSM supports only %q (got %q)", domain.ErrUnsupportedMode, domain.FetchGet, mode)
	}

	region, paramPath, err := parseSSMArn(ref.Location)
	if err != nil {
		return "", fmt.Errorf("parse SSM ARN: %w", err)
	}

	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		return "", fmt.Errorf("parse SSM ARN: location %q has no region; set AWS_REGION or use an arn:aws:ssm:... ARN", ref.Location)
	}

	name := paramPath + "/versions/" + ref.Version
	log.Printf("aws ssm: fetching parameter %q in region %q", name, region)

	resp, err := a.client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("aws ssm: GetParameter %q (region=%s): %w", name, region, err)
	}

	if resp.Parameter == nil || resp.Parameter.Value == nil {
		return "", fmt.Errorf("aws ssm: GetParameter %q returned an empty parameter", name)
	}

	log.Printf("aws ssm: parameter %q fetched successfully (%d byte(s))", name, len(*resp.Parameter.Value))
	return domain.Payload(*resp.Parameter.Value), nil
}

// parseSSMArn parses an AWS SSM Parameter Store ARN or bare parameter name
// and returns the region and parameter path (with a leading slash).
//
// ARN format: arn:aws:ssm:{region}:{account}:parameter/{path}
// Bare format: /{path} (region supplied via AWS_REGION by the caller).
func parseSSMArn(arn string) (region, paramPath string, err error) {
	if strings.HasPrefix(arn, "/") {
		if arn == "/" {
			return "", "", fmt.Errorf("invalid SSM parameter name %q: path is empty", arn)
		}
		return "", arn, nil
	}

	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 {
		return "", "", fmt.Errorf("invalid SSM ARN %q: expected 6 colon-delimited fields (got %d)", arn, len(parts))
	}
	if parts[0] != "arn" || parts[1] != "aws" || parts[2] != "ssm" {
		return "", "", fmt.Errorf("invalid SSM ARN %q: must begin with arn:aws:ssm:", arn)
	}

	region = parts[3]
	if region == "" {
		return "", "", fmt.Errorf("invalid SSM ARN %q: region field is empty", arn)
	}

	resource := parts[5]
	if !strings.HasPrefix(resource, "parameter/") {
		return "", "", fmt.Errorf("invalid SSM ARN %q: resource %q does not start with 'parameter/'", arn, resource)
	}

	paramPath = "/" + strings.TrimPrefix(resource, "parameter/")
	return region, paramPath, nil
}
