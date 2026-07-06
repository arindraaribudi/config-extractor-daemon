// Package tencentcred resolves Tencent Cloud AK/SK credentials for adapters
// in this module. The chain mirrors Tencent's SDK defaults: env vars first,
// CVM/TKE metadata server as fallback. The first successful lookup wins;
// the result is cached for the process lifetime so a single Fetch that
// runs both a source and a secret resolver hits the network once.
package tencentcred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// Credentials holds a Tencent Cloud AK/SK pair.
type Credentials struct {
	SecretID  string
	SecretKey string
}

// ErrNoCredentials is returned when neither env vars nor the metadata server
// yielded a pair. Its message names the env vars so users see the fix path.
var ErrNoCredentials = errors.New("tencent creds: no TENCENTCLOUD_SECRETID/TENCENTCLOUD_SECRETKEY in env and metadata server unreachable")

// metadataBase is the CVM/TKE metadata server base URL. Overridden in tests.
var metadataBase = "http://metadata.tencentyun.com"

var (
	httpClient = &http.Client{Timeout: 2 * time.Second}

	cacheMu  sync.Mutex
	cached   *Credentials
	cacheErr error
)

// Resolve returns a Credentials value, trying env vars then the metadata server.
// Cached after first success.
func Resolve(ctx context.Context) (*Credentials, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cached != nil {
		return cached, nil
	}
	if cacheErr != nil {
		return nil, cacheErr
	}

	if id, key := os.Getenv("TENCENTCLOUD_SECRETID"), os.Getenv("TENCENTCLOUD_SECRETKEY"); id != "" && key != "" {
		cached = &Credentials{SecretID: id, SecretKey: key}
		return cached, nil
	}

	creds, err := fetchFromMetadata(ctx)
	if err != nil {
		cacheErr = fmt.Errorf("%w: %v", ErrNoCredentials, err)
		return nil, cacheErr
	}
	cached = creds
	return cached, nil
}

func fetchFromMetadata(ctx context.Context) (*Credentials, error) {
	roleName, err := getMetadata(ctx, "/latest/meta-data/cam/security-credentials/")
	if err != nil {
		return nil, fmt.Errorf("metadata role lookup: %w", err)
	}
	body, err := getMetadata(ctx, "/latest/meta-data/cam/security-credentials/"+string(roleName))
	if err != nil {
		return nil, fmt.Errorf("metadata credentials lookup: %w", err)
	}
	var resp struct {
		TmpSecretID  string `json:"TmpSecretId"`
		TmpSecretKey string `json:"TmpSecretKey"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse metadata response: %w", err)
	}
	if resp.TmpSecretID == "" || resp.TmpSecretKey == "" {
		return nil, fmt.Errorf("metadata returned empty TmpSecretId/TmpSecretKey")
	}
	return &Credentials{SecretID: resp.TmpSecretID, SecretKey: resp.TmpSecretKey}, nil
}

func getMetadata(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataBase+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not on CVM/TKE (404 from %s)", path)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("metadata %s: status %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ResetForTest clears the credential cache. Test-only.
func ResetForTest() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cached = nil
	cacheErr = nil
}