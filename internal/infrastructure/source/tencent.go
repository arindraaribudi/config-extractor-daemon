package source

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
	"github.com/tencentyun/cos-go-sdk-v5"
)

func isTencentLocation(location string) bool {
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	host := u.Host
	return strings.HasSuffix(host, ".myqcloud.com") && strings.Contains(host, ".cos.")
}

// parseTencentLocation extracts bucket, region, and object key from a COS
// HTTPS URL. CONFIG_VERSION is appended as the final path segment.
func parseTencentLocation(location, version string) (bucket, region, key string, err error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", "", "", fmt.Errorf("tencent cos: parse URL %q: %w", location, err)
	}
	if u.Scheme != "https" {
		return "", "", "", fmt.Errorf("tencent cos: location %q must use https scheme", location)
	}
	host := u.Host
	if !strings.HasSuffix(host, ".myqcloud.com") || !strings.Contains(host, ".cos.") {
		return "", "", "", fmt.Errorf("tencent cos: host %q is not a COS endpoint (expected *.cos.{region}.myqcloud.com)", host)
	}

	beforeCos, afterCos, ok := strings.Cut(host, ".cos.")
	if !ok {
		return "", "", "", fmt.Errorf("tencent cos: host %q missing .cos. segment", host)
	}
	bucket = beforeCos
	region, _, ok = strings.Cut(afterCos, ".")
	if !ok || region == "" {
		return "", "", "", fmt.Errorf("tencent cos: host %q missing region", host)
	}

	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return "", "", "", fmt.Errorf("tencent cos: location %q has empty path", location)
	}
	key = path + "/versions/" + version
	return bucket, region, key, nil
}

// Stub types to keep the file compiling before the next task adds Fetch.
// Replaced in the next task.
type cosObjectGetResult interface {
	Get(ctx context.Context, key string, opt *cos.ObjectGetOptions, id ...string) (*cos.Response, error)
}

type tencentSource struct {
	getter cosObjectGetResult
}

// cosNewClient builds a real COS client wired with the supplied credentials.
// Overridden in tests.
var cosNewClient = func(bucket, region string, creds *tencentcred.Credentials) (cosObjectGetResult, error) {
	u, err := url.Parse("https://" + bucket + ".cos." + region + ".myqcloud.com")
	if err != nil {
		return nil, err
	}
	baseURL := &cos.BaseURL{BucketURL: u}
	auth := &cos.AuthorizationTransport{
		SecretID:  creds.SecretID,
		SecretKey: creds.SecretKey,
	}
	if creds.Token != "" {
		auth.SessionToken = creds.Token
	}
	httpClient := &http.Client{Transport: auth}
	client := cos.NewClient(baseURL, httpClient)
	return client.Object, nil
}

func NewTencentSource(ctx context.Context, mode domain.FetchMode) (domain.ConfigSource, error) {
	// Build a placeholder source that defers client construction to Fetch —
	// matches the AWS pattern where credentials are resolved lazily.
	// Location is unknown at construction time (mode is the only arg).
	_ = ctx
	_ = mode
	return &tencentSource{}, nil
}

func (s *tencentSource) Kind() domain.ProviderKind { return domain.ProviderTencent }

func (s *tencentSource) Fetch(ctx context.Context, ref domain.Reference, mode domain.FetchMode) (domain.Payload, error) {
	if mode != domain.FetchGet {
		return "", fmt.Errorf("%w: tencent cos supports only %q (got %q)", domain.ErrUnsupportedMode, domain.FetchGet, mode)
	}

	bucket, region, key, err := parseTencentLocation(ref.Location, ref.Version)
	if err != nil {
		return "", err
	}

	getter := s.getter
	if getter == nil {
		creds, err := tencentcred.Resolve(ctx)
		if err != nil {
			return "", fmt.Errorf("tencent cos: resolve credentials: %w", err)
		}
		g, err := cosNewClient(bucket, region, creds)
		if err != nil {
			return "", fmt.Errorf("tencent cos: build client: %w", err)
		}
		getter = g
	}

	log.Printf("tencent cos: fetching bucket=%q region=%q key=%q", bucket, region, key)
	resp, err := getter.Get(ctx, key, nil)
	if err != nil {
		return "", fmt.Errorf("tencent cos: GetObject %q (region=%s): %w", key, region, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("tencent cos: read body for %q: %w", key, err)
	}
	log.Printf("tencent cos: object %q fetched successfully (%d byte(s))", key, len(body))
	return domain.Payload(body), nil
}
