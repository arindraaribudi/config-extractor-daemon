package secrets

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/source"
)

const awsLegacyPrefix = "secretsmanager.amazonaws.com/"

// awsResolver resolves __SECRET_REF__() tokens pointing at AWS Secrets Manager.
//
// Supported ref formats:
//
// Legacy:
//
//	secretsmanager.amazonaws.com/{region}/{secret-id}
//
// Regional:
//
//	secretsmanager.{region}.amazonaws.com/projects/{account_id}/secrets/{secret-id}
//
// In both cases the region is used to configure the AWS client and the
// secret-id is passed to GetSecretValue (name or full ARN).
type awsResolver struct{}

var awsCredLogOnce sync.Once

func NewAWSResolver() domain.SecretResolver { return awsResolver{} }

func (awsResolver) Supports(ref string) bool {
	if strings.HasPrefix(ref, awsLegacyPrefix) {
		return true
	}
	return strings.HasPrefix(ref, "secretsmanager.") && strings.Contains(ref, ".amazonaws.com/projects/")
}

func (awsResolver) Resolve(ctx context.Context, ref string) (string, error) {
	region, secretID, err := parseAWSSecretRef(ref)
	if err != nil {
		return "", err
	}

	awsCredLogOnce.Do(func() {
		log.Printf("aws creds: source=%s region=%s", source.DetectAWSCredSource(os.Getenv), region)
	})

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("aws config for region %q: %w", region, err)
	}

	client := secretsmanager.NewFromConfig(cfg)
	resp, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretID),
	})
	if err != nil {
		return "", fmt.Errorf("aws get secret %q: %w", secretID, err)
	}

	if resp.SecretString != nil {
		return *resp.SecretString, nil
	}
	return "", fmt.Errorf("aws secret %q: binary secrets are not supported", secretID)
}

// parseAWSSecretRef extracts the region and secret-id from a supported
// ref format. Kept package-private so the use case can call it for tests.
func parseAWSSecretRef(ref string) (region, secretID string, err error) {
	if path, ok := strings.CutPrefix(ref, awsLegacyPrefix); ok {
		region, secretID, ok = strings.Cut(path, "/")
		if !ok {
			return "", "", fmt.Errorf("aws secret ref %q: expected {region}/{secret-id} after prefix", ref)
		}
		log.Printf("aws secret ref: arn:aws:secretsmanager:%s::secret:%s", region, secretID)
		return region, secretID, nil
	}

	after := strings.TrimPrefix(ref, "secretsmanager.")
	region, path, ok := strings.Cut(after, ".amazonaws.com/")
	if !ok {
		return "", "", fmt.Errorf("aws secret ref %q: expected secretsmanager.{region}.amazonaws.com/...", ref)
	}
	projectsPart, secretID, ok := strings.Cut(path, "/secrets/")
	if !ok {
		return "", "", fmt.Errorf("aws secret ref %q: expected /secrets/{secret-id} in path", ref)
	}
	accountID := strings.TrimPrefix(projectsPart, "projects/")
	log.Printf("aws secret ref: arn:aws:secretsmanager:%s:%s:secret:%s", region, accountID, secretID)
	return region, secretID, nil
}
