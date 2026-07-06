# Tencent Cloud Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Tencent Cloud as a third provider — parameter payloads fetched from COS objects at HTTPS URLs, secrets resolved via Tencent Secrets Manager — plugged into the existing source/secret registries without touching domain, application, or `cmd/`.

**Architecture:** Two new adapters (`internal/infrastructure/source/tencent.go`, `internal/infrastructure/secrets/tencent.go`) share one credential resolver (`internal/infrastructure/tencentcred`). Registry appends, `ProviderTencent` constant, URL-based matcher. CVM/TKE metadata server with AK/SK env fallback. Tests mock the SDK clients via package-private vars (same pattern as AWS/GCP).

**Tech Stack:** Go 1.26, `github.com/tencentyun/cos-go-sdk-v5`, `github.com/tencentcloud/tencentcloud-sdk-go` (`ssm/v20190923` sub-package — Tencent's Secrets Manager SDK path).

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `internal/domain/config_source.go` | modify | Add `ProviderTencent` constant |
| `internal/infrastructure/tencentcred/tencentcred.go` | create | Shared AK/SK resolver (env → CVM metadata) |
| `internal/infrastructure/tencentcred/tencentcred_test.go` | create | Tests for credential resolution |
| `internal/infrastructure/source/tencent.go` | create | COS source adapter |
| `internal/infrastructure/source/tencent_test.go` | create | URL parse + Fetch error tests |
| `internal/infrastructure/source/registry.go` | modify | Append Tencent entry + matcher |
| `internal/infrastructure/secrets/tencent.go` | create | Secrets Manager resolver |
| `internal/infrastructure/secrets/tencent_test.go` | create | Ref parse + Resolve tests |
| `internal/infrastructure/secrets/registry.go` | modify | Append Tencent resolver entry |
| `go.mod`, `go.sum` | modify | Add `cos-go-sdk-v5`, `tencentcloud-sdk-go` |
| `README.md` | modify | Env vars + examples |
| `CLOUD_SUPPORT.md` | modify | New "Tencent Cloud" section |

---

## Task 1: Add `ProviderTencent` constant

**Files:**
- Modify: `internal/domain/config_source.go`

- [ ] **Step 1: Edit the const block**

Open `internal/domain/config_source.go` and replace the `const ( ... )` block with:

```go
const (
	ProviderGCP     ProviderKind = "gcp"
	ProviderAWS     ProviderKind = "aws"
	ProviderTencent ProviderKind = "tencent"
)
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: exit 0, no output.

- [ ] **Step 3: Run existing tests to confirm no regression**

Run: `go test ./internal/domain/...`
Expected: PASS (existing tests untouched).

- [ ] **Step 4: Commit**

```bash
git add internal/domain/config_source.go
git commit -m "feat(domain): add ProviderTencent constant"
```

---

## Task 2: Add Tencent SDK dependencies

**Files:**
- Modify: `go.mod`, `go.sum` (via `go get`)

- [ ] **Step 1: Add COS SDK**

Run: `go get github.com/tencentyun/cos-go-sdk-v5`
Expected: `go.mod` and `go.sum` updated; command exits 0.

- [ ] **Step 2: Add Secrets Manager SDK sub-package**

Run: `go get github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923`
Expected: `go.mod` and `go.sum` updated; command exits 0.

> Note: Tencent confusingly calls their Secrets Manager product "SSM" (Secure
> Secrets Manager) in the SDK. The package is `tencentcloud/ssm/v20190923`,
> not `secretsmanager`. It exposes `GetSecretValue(SecretName)` — same
> contract the plan targets.

- [ ] **Step 3: Verify modules tidy**

Run: `go mod tidy`
Expected: exit 0.

- [ ] **Step 4: Verify build still passes**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add tencent cloud sdk dependencies"
```

---

## Task 3: Shared Tencent credential resolver

**Files:**
- Create: `internal/infrastructure/tencentcred/tencentcred.go`
- Create: `internal/infrastructure/tencentcred/tencentcred_test.go`

- [ ] **Step 1: Write failing test for env-var path**

Create `internal/infrastructure/tencentcred/tencentcred_test.go`:

```go
package tencentcred

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolve_EnvVars(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRETID", "env-id")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "env-key")

	// Point metadata server at a 500-er to confirm we never hit it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("metadata server must not be called when env vars set; got %s", r.URL.Path)
	}))
	defer srv.Close()
	prevBase := metadataBase
	metadataBase = srv.URL
	defer func() { metadataBase = prevBase }()

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "env-id" || got.SecretKey != "env-key" {
		t.Fatalf("got %+v, want env-id/env-key", got)
	}
}

func TestResolve_MetadataFallback(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRETID", "")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "")

	// Role-name endpoint returns the role; credentials endpoint returns the creds.
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/meta-data/cam/security-credentials/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("my-role"))
	})
	mux.HandleFunc("/latest/meta-data/cam/security-credentials/my-role", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"TmpSecretId":"meta-id","TmpSecretKey":"meta-key"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	prevBase := metadataBase
	metadataBase = srv.URL
	defer func() { metadataBase = prevBase }()

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "meta-id" || got.SecretKey != "meta-key" {
		t.Fatalf("got %+v, want meta-id/meta-key", got)
	}
}

func TestResolve_NoCredentials(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRETID", "")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "")

	// Metadata server returns 404 → not on CVM/TKE.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	prevBase := metadataBase
	metadataBase = srv.URL
	defer func() { metadataBase = prevBase }()

	_, err := Resolve(t.Context())
	if err == nil {
		t.Fatal("expected error when no creds available")
	}
	if want := "TENCENTCLOUD_SECRETID"; !contains(err.Error(), want) {
		t.Fatalf("error %q should mention %q", err.Error(), want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run test to confirm failure**

Run: `go test ./internal/infrastructure/tencentcred/... -v`
Expected: FAIL — package does not exist yet (`no Go files in ...`).

- [ ] **Step 3: Implement the resolver**

Create `internal/infrastructure/tencentcred/tencentcred.go`:

```go
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
	body, err := getMetadata(ctx, "/latest/meta-data/cam/security-credentials/"+roleName)
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
```

- [ ] **Step 4: Run test to confirm it passes**

Run: `go test ./internal/infrastructure/tencentcred/... -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/infrastructure/tencentcred/
git commit -m "feat(tencentcred): shared env+metadata credential resolver"
```

---

## Task 4: COS source adapter — URL parsing

**Files:**
- Create: `internal/infrastructure/source/tencent.go`
- Create: `internal/infrastructure/source/tencent_test.go`

- [ ] **Step 1: Write failing tests for `parseTencentLocation`**

Append to `internal/infrastructure/source/tencent_test.go`:

```go
package source

import "testing"

func TestParseTencentLocation(t *testing.T) {
	tests := []struct {
		name        string
		location    string
		version     string
		wantBucket  string
		wantRegion  string
		wantKey     string
		wantErrSub  string
	}{
		{
			name:       "full URL with version appended",
			location:   "https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig",
			version:    "ENV-1",
			wantBucket: "cds-oms-sit-1409486316",
			wantRegion: "ap-bangkok",
			wantKey:    "projects/my-proj/parameters/appconfig/ENV-1",
		},
		{
			name:       "URL with single-segment path",
			location:   "https://bucket-123.cos.ap-shanghai.myqcloud.com/cfg",
			version:    "v1",
			wantBucket: "bucket-123",
			wantRegion: "ap-shanghai",
			wantKey:    "cfg/v1",
		},
		{
			name:       "non-https scheme rejected",
			location:   "http://bucket.cos.ap-shanghai.myqcloud.com/cfg",
			version:    "v1",
			wantErrSub: "https",
		},
		{
			name:       "host without .cos. rejected",
			location:   "https://bucket.example.com/cfg",
			version:    "v1",
			wantErrSub: ".cos.",
		},
		{
			name:       "host without .myqcloud.com rejected",
			location:   "https://bucket.cos.example.com/cfg",
			version:    "v1",
			wantErrSub: "myqcloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, region, key, err := parseTencentLocation(tt.location, tt.version)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSub)
				}
				if !contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error %q missing %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bucket != tt.wantBucket || region != tt.wantRegion || key != tt.wantKey {
				t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)",
					bucket, region, key, tt.wantBucket, tt.wantRegion, tt.wantKey)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to confirm failure**

Run: `go test ./internal/infrastructure/source/... -run TestParseTencentLocation -v`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement `parseTencentLocation`**

Create `internal/infrastructure/source/tencent.go` with:

```go
package source

import (
	"context"
	"fmt"
	"log"
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
	key = path + "/" + version
	return bucket, region, key, nil
}

// Stub types to keep the file compiling before the next task adds Fetch.
// Replaced in the next task.
type tencentSource struct{}

func (tencentSource) Kind() domain.ProviderKind { return domain.ProviderTencent }

func NewTencentSource(ctx context.Context, _ domain.FetchMode) (domain.ConfigSource, error) {
	// Will be replaced in the next task with real client construction.
	_ = tencentcred.Resolve
	_ = cos.NewClient
	return tencentSource{}, nil
}

var _ = log.Printf
```

(The stub keeps the file compileable; Task 4 step below replaces the stub with the real implementation.)

- [ ] **Step 4: Run test to confirm it passes**

Run: `go test ./internal/infrastructure/source/... -run TestParseTencentLocation -v`
Expected: PASS (5 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/infrastructure/source/tencent.go internal/infrastructure/source/tencent_test.go
git commit -m "feat(source): parse COS URL for tencent location"
```

---

## Task 5: COS source adapter — Fetch implementation

**Files:**
- Modify: `internal/infrastructure/source/tencent.go`
- Modify: `internal/infrastructure/source/tencent_test.go`

- [ ] **Step 1: Write failing test for Fetch error paths**

Append to `internal/infrastructure/source/tencent_test.go`:

```go
func TestTencentSource_Fetch_UnsupportedMode(t *testing.T) {
	s := tencentSource{}
	_, err := s.Fetch(t.Context(), domain.Reference{
		Location: "https://b.cos.ap-bangkok.myqcloud.com/cfg",
		Version:  "v1",
	}, domain.FetchRender)
	if err == nil {
		t.Fatal("expected error for render mode")
	}
	if !errors.Is(err, domain.ErrUnsupportedMode) {
		t.Fatalf("expected ErrUnsupportedMode, got %v", err)
	}
}

func TestTencentSource_Fetch_LocationError(t *testing.T) {
	s := tencentSource{}
	_, err := s.Fetch(t.Context(), domain.Reference{
		Location: "http://b.cos.ap-bangkok.myqcloud.com/cfg",
		Version:  "v1",
	}, domain.FetchGet)
	if err == nil {
		t.Fatal("expected error for http scheme")
	}
}
```

Add `"errors"` and the `domain` package import at the top:

```go
import (
	"errors"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
)
```

- [ ] **Step 2: Run test to confirm failure**

Run: `go test ./internal/infrastructure/source/... -run TestTencentSource -v`
Expected: FAIL — `tencentSource.Fetch` not defined.

- [ ] **Step 3: Implement `Fetch` and replace the stub**

Replace the stub block in `internal/infrastructure/source/tencent.go` (everything from `// Stub types...` to `var _ = log.Printf`) with:

```go
type cosObjectGetResult interface {
	Get(ctx context.Context, key string, opt *cos.ObjectGetOptions) (*cos.Response, error)
}

type tencentSource struct {
	getter  cosObjectGetResult
	bucket  string
	region  string
	key     string
}

// cosNewClient builds a real COS client wired with the supplied credentials.
// Overridden in tests.
var cosNewClient = func(bucket, region string, creds *tencentcred.Credentials) (cosObjectGetResult, error) {
	u, err := url.Parse("https://" + bucket + ".cos." + region + ".myqcloud.com")
	if err != nil {
		return nil, err
	}
	baseURL := &cos.BaseURL{BucketURL: u}
	httpClient := &http.Client{Transport: &cos.AuthorizationTransport{
		SecretID:  creds.SecretID,
		SecretKey: creds.SecretKey,
	}}
	return cos.NewClient(baseURL, httpClient), nil
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
```

Update the imports at the top of the file to:

```go
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
```

- [ ] **Step 4: Run tests to confirm they pass**

Run: `go test ./internal/infrastructure/source/... -run TestParseTencentLocation -run TestTencentSource -v`
Expected: PASS (5 + 2 = 7 tests).

- [ ] **Step 5: Run the full source package test suite**

Run: `go test ./internal/infrastructure/source/... -v`
Expected: PASS (existing tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/infrastructure/source/tencent.go internal/infrastructure/source/tencent_test.go
git commit -m "feat(source): implement COS GetObject fetch for tencent"
```

---

## Task 6: Wire ProviderTencent into source registry

**Files:**
- Modify: `internal/infrastructure/source/registry.go`

- [ ] **Step 1: Read current registry to confirm ordering**

Run: `cat internal/infrastructure/source/registry.go`
Confirm the existing `Sources` slice and the `isSSMLocation` matcher.

- [ ] **Step 2: Insert the Tencent entry between AWS and GCP**

Replace the `Sources` slice with:

```go
var Sources = []Entry{
	{
		Kind:    domain.ProviderAWS,
		Match:   isSSMLocation,
		Factory: NewAWSSource,
	},
	{
		Kind:    domain.ProviderTencent,
		Match:   isTencentLocation,
		Factory: NewTencentSource,
	},
	{
		Kind:    domain.ProviderGCP,
		Match:   func(string) bool { return true }, // GCP is the fallback for any non-AWS, non-Tencent location
		Factory: NewGCPSource,
	},
}
```

Update the "To add a new cloud" doc comment block above `Sources` to mention Tencent as an example adapter:

```go
// Sources is the global list of available config providers.
//
// To add a new cloud (e.g. Azure):
//  1. Create a new file in this package that exports a
//     `NewXxxSource(ctx, mode) (domain.ConfigSource, error)` factory.
//  2. Append one entry below.
//
// Domain, application, and cmd/ require zero changes.
```

(Only the example list updates; logic unchanged.)

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 4: Verify tests still pass**

Run: `go test ./internal/infrastructure/source/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infrastructure/source/registry.go
git commit -m "feat(source): register tencent provider"
```

---

## Task 7: Secrets Manager resolver — URI parsing

**Files:**
- Create: `internal/infrastructure/secrets/tencent.go`
- Create: `internal/infrastructure/secrets/tencent_test.go`

- [ ] **Step 1: Write failing tests for `parseTencentSecretRef`**

Create `internal/infrastructure/secrets/tencent_test.go`:

```go
package secrets

import "testing"

func TestParseTencentSecretRef(t *testing.T) {
	tests := []struct {
		name        string
		ref         string
		wantRegion  string
		wantSecret  string
		wantErrSub  string
	}{
		{
			name:       "valid legacy form",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password",
			wantRegion: "ap-bangkok",
			wantSecret: "db-password",
		},
		{
			name:       "secret with hyphens",
			ref:        "secretsmanager.tencentcloudapi.com/ap-shanghai/inner-svc-tls-key",
			wantRegion: "ap-shanghai",
			wantSecret: "inner-svc-tls-key",
		},
		{
			name:       "missing slash",
			ref:        "secretsmanager.tencentcloudapi.com/ap-bangkok",
			wantErrSub: "expected",
		},
		{
			name:       "empty region",
			ref:        "secretsmanager.tencentcloudapi.com//db-password",
			wantErrSub: "region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, secret, err := parseTencentSecretRef(tt.ref)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSub)
				}
				if !contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error %q missing %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if region != tt.wantRegion || secret != tt.wantSecret {
				t.Fatalf("got (%q,%q), want (%q,%q)", region, secret, tt.wantRegion, tt.wantSecret)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to confirm failure**

Run: `go test ./internal/infrastructure/secrets/... -run TestParseTencentSecretRef -v`
Expected: FAIL — function not defined.

- [ ] **Step 3: Implement `parseTencentSecretRef` and stub resolver**

Create `internal/infrastructure/secrets/tencent.go`:

```go
package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
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

// Stub resolver — replaced in next task.
type tencentResolver struct{}

func NewTencentResolver() domain.SecretResolver { return tencentResolver{} }

func (tencentResolver) Supports(ref string) bool {
	return strings.HasPrefix(ref, tencentSecretPrefix)
}

func (tencentResolver) Resolve(_ context.Context, _ string) (string, error) {
	return "", errors.New("tencent resolver not yet implemented")
}
```

- [ ] **Step 4: Run test to confirm it passes**

Run: `go test ./internal/infrastructure/secrets/... -run TestParseTencentSecretRef -v`
Expected: PASS (4 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/infrastructure/secrets/tencent.go internal/infrastructure/secrets/tencent_test.go
git commit -m "feat(secrets): parse tencent secret ref URI"
```

---

## Task 8: Secrets Manager resolver — Resolve implementation

**Files:**
- Modify: `internal/infrastructure/secrets/tencent.go`
- Modify: `internal/infrastructure/secrets/tencent_test.go`

- [ ] **Step 1: Write failing test for Resolve**

Append to `internal/infrastructure/secrets/tencent_test.go`:

```go
func TestTencentResolver_Supports(t *testing.T) {
	r := NewTencentResolver()
	if !r.Supports("secretsmanager.tencentcloudapi.com/ap-bangkok/db-password") {
		t.Fatal("expected support for valid prefix")
	}
	if r.Supports("secretmanager.googleapis.com/projects/p/secrets/s/versions/1") {
		t.Fatal("did not expect support for GCP prefix")
	}
}

func TestTencentResolver_Resolve_Success(t *testing.T) {
	// Replace the SDK client factory for the test.
	prev := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		if region != "ap-bangkok" {
			t.Fatalf("unexpected region %q", region)
		}
		if creds.SecretID == "" || creds.SecretKey == "" {
			t.Fatal("creds not propagated")
		}
		return fakeSecretsClient{secret: "s3cret"}, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRETID", "id")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "key")

	// Wipe the cached credentials from tencentcred to force re-resolve with env vars.
	tencentcred.ResetForTest()

	got, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/db-password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("got %q, want s3cret", got)
	}
}

func TestTencentResolver_Resolve_BinaryUnsupported(t *testing.T) {
	prev := newTencentSecretsClient
	newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
		return fakeSecretsClient{binary: "YmFzZTY0"}, nil
	}
	defer func() { newTencentSecretsClient = prev }()

	t.Setenv("TENCENTCLOUD_SECRETID", "id")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "key")
	tencentcred.ResetForTest()

	_, err := NewTencentResolver().Resolve(t.Context(), "secretsmanager.tencentcloudapi.com/ap-bangkok/blob")
	if err == nil {
		t.Fatal("expected error for binary secret")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("error %q should mention 'binary'", err.Error())
	}
}
```

Add imports at the top of `tencent_test.go`:

```go
import (
	"strings"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/tencentcred"
)
```

- [ ] **Step 2: Add `ResetForTest` to tencentcred**

Open `internal/infrastructure/tencentcred/tencentcred.go` and append at the end:

```go
// ResetForTest clears the credential cache. Test-only.
func ResetForTest() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cached = nil
	cacheErr = nil
}
```

- [ ] **Step 3: Run test to confirm failure**

Run: `go test ./internal/infrastructure/secrets/... -run TestTencentResolver -v`
Expected: FAIL — `newTencentSecretsClient`, `tencentSecretsClient`, `fakeSecretsClient` not defined.

- [ ] **Step 4: Replace the stub resolver with the real implementation**

Replace the contents of `internal/infrastructure/secrets/tencent.go` (keep the package, const, and `parseTencentSecretRef`) with the full implementation. The final file content is:

```go
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
	region string
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
	resp := &ssm.GetSecretValueResponse{}
	resp.Response = &struct {
		SecretName  *string `json:"SecretName,omitempty" name:"SecretName"`
		VersionId   *string `json:"VersionId,omitempty" name:"VersionId"`
		SecretBinary *string `json:"SecretBinary,omitempty" name:"SecretBinary"`
		SecretString *string `json:"SecretString,omitempty" name:"SecretString"`
	}{}
	if f.secret != "" {
		resp.Response.SecretString = common.StringPtr(f.secret)
	}
	if f.binary != "" {
		resp.Response.SecretBinary = common.StringPtr(f.binary)
	}
	return resp, nil
}
```

- [ ] **Step 5: Run all secrets tests**

Run: `go test ./internal/infrastructure/secrets/... -v`
Expected: PASS (4 + 3 = 7 tests).

- [ ] **Step 6: Run tencentcred tests after ResetForTest addition**

Run: `go test ./internal/infrastructure/tencentcred/... -v`
Expected: PASS (3 tests still passing).

- [ ] **Step 7: Commit**

```bash
git add internal/infrastructure/secrets/tencent.go internal/infrastructure/secrets/tencent_test.go internal/infrastructure/tencentcred/tencentcred.go
git commit -m "feat(secrets): implement tencent secretsmanager resolver"
```

---

## Task 9: Wire ProviderTencent into secrets registry

**Files:**
- Modify: `internal/infrastructure/secrets/registry.go`

- [ ] **Step 1: Append the Tencent entry**

Replace the `Resolvers` slice with:

```go
var Resolvers = []ResolverEntry{
	{Kind: domain.ProviderGCP, Resolver: NewGCPResolver()},
	{Kind: domain.ProviderAWS, Resolver: NewAWSResolver()},
	{Kind: domain.ProviderTencent, Resolver: NewTencentResolver()},
}
```

Update the "To add a new cloud" doc comment block above to mention Tencent as an example:

```go
// Resolvers is the global list of available secret resolvers. Order does
// NOT matter — dispatch is done by Supports().
//
// To add a new cloud:
//  1. Create a new file in this package exposing a
//     `NewXxxResolver() domain.SecretResolver` constructor.
//  2. Append one entry below.
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 3: Verify tests still pass**

Run: `go test ./internal/infrastructure/secrets/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/infrastructure/secrets/registry.go
git commit -m "feat(secrets): register tencent resolver"
```

---

## Task 10: README.md updates

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add Tencent env vars**

Find the env vars table in `README.md` and append:

```markdown
| `TENCENTCLOUD_SECRETID` | no | — | Tencent Cloud API AK. Optional when running on CVM/TKE with a CAM role attached. |
| `TENCENTCLOUD_SECRETKEY` | no | — | Tencent Cloud API SK. Same precedence rule. |
```

- [ ] **Step 2: Add Tencent CONFIG_LOCATION example**

Find the CONFIG_LOCATION row and append an example block:

```markdown
Tencent COS example:

```
CONFIG_LOCATION=https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig
CONFIG_VERSION=ENV-1
```

Resolves to bucket `cds-oms-sit-1409486316`, region `ap-bangkok`, key `projects/my-proj/parameters/appconfig/ENV-1`.
```

- [ ] **Step 3: Add Tencent secret ref example**

Find the secret ref shapes list and append:

```markdown
- Tencent Secrets Manager: `secretsmanager.tencentcloudapi.com/{region}/{secret-name}`
```

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: add tencent cloud examples to README"
```

---

## Task 11: CLOUD_SUPPORT.md updates

**Files:**
- Modify: `CLOUD_SUPPORT.md`

- [ ] **Step 1: Read current structure**

Run: `grep -n '^##' CLOUD_SUPPORT.md`
Identify the section after AWS and before any closing architecture notes.

- [ ] **Step 2: Insert Tencent section**

Append after the AWS section (before "Architecture" or any closing notes):

```markdown
## Tencent Cloud

### Detection

`isTencentLocation(location)` in `internal/infrastructure/source/registry.go` matches
URLs whose scheme is `https`, host contains `.cos.`, and host ends with
`.myqcloud.com`. Examples:

- `https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig`

Bucket name and region are extracted from the URL host. `CONFIG_VERSION` is
appended as the final object-key segment.

### Fetcher

`NewTencentSource(ctx, mode)` returns a `tencentSource` (`internal/infrastructure/source/tencent.go`)
backed by `github.com/tencentyun/cos-go-sdk-v5`. Only the `get` mode is
supported; `render` returns `domain.ErrUnsupportedMode` (COS stores raw
bytes, no template rendering).

### Secret resolution

`__SECRET_REF__(secretsmanager.tencentcloudapi.com/{region}/{secret-name})`
dispatches to `tencentResolver` (`internal/infrastructure/secrets/tencent.go`),
which calls `ssm.v20190923.GetSecretValue` via the official
tencentcloud-sdk-go sub-package. Binary secrets are rejected with an error
(matching the AWS limitation).

### Credentials

`internal/infrastructure/tencentcred` resolves AK/SK in this order:

1. `TENCENTCLOUD_SECRETID` + `TENCENTCLOUD_SECRETKEY` env vars.
2. CVM/TKE metadata server (`http://metadata.tencentyun.com/latest/meta-data/cam/security-credentials/<role>`).
3. Error if neither path yields a pair.

The resolved pair is cached for process lifetime so a single Fetch that
runs both source and secret resolution hits the network once.

### End-to-end flow

1. Resolve AK/SK via `tencentcred.Resolve(ctx)`.
2. Build COS client (`cos-go-sdk-v5`).
3. Build Secrets Manager client (lazy, only if a secret ref is present).
4. `tencentSource.Fetch` → `cos.Object.Get(key)` → payload bytes.
5. `ParsePayload` → `EnvPair` slice.
6. `ResolveSecretRefs` → resolved pairs (tencent resolver claims `secretsmanager.tencentcloudapi.com/...`).
7. Write `.env` or `exec` inject.
```

- [ ] **Step 3: Commit**

```bash
git add CLOUD_SUPPORT.md
git commit -m "docs: add tencent cloud section to CLOUD_SUPPORT"
```

---

## Task 12: Full integration verification

**Files:** none

- [ ] **Step 1: Run the full test suite**

Run: `go test ./... -v`
Expected: PASS — every package's tests pass, no flakes.

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: exit 0, no warnings.

- [ ] **Step 3: Build the binary**

Run: `go build -o /tmp/config-extractor-tencent .`
Expected: exit 0, binary at `/tmp/config-extractor-tencent`.

- [ ] **Step 4: Smoke-test exec mode against env-printer**

Run:

```bash
TENCENTCLOUD_SECRETID=test TENCENTCLOUD_SECRETKEY=test \
  /tmp/config-extractor-tencent --mode=exec --strict-fetch=false -- \
  ./cmd/env-printer/env-printer
```

Expected: prints env vars of the child process. (With empty payload the
child still runs and shows only its own env.)

- [ ] **Step 5: Build Docker image to verify multi-arch build path still works**

Run: `make build IMAGE=config-extractor-daemon:test-tencent`
Expected: build completes; image tagged.

- [ ] **Step 6: Final commit if any cleanup happened**

```bash
git status
```

If anything uncommitted:

```bash
git add -A
git commit -m "chore: post-integration cleanup"
```

If clean, no commit.

---

## Self-Review Notes (planning author)

Spec coverage check:
- ✅ `ProviderTencent` constant — Task 1
- ✅ COS source adapter with URL parsing — Tasks 4, 5
- ✅ Secrets Manager resolver with URI parsing — Tasks 7, 8
- ✅ Shared credential resolver — Task 3
- ✅ Registry appends (source + secrets) — Tasks 6, 9
- ✅ Tests (parse + Fetch + Resolve) — Tasks 4, 5, 7, 8
- ✅ Documentation (README + CLOUD_SUPPORT) — Tasks 10, 11
- ✅ Dependencies — Task 2
- ✅ Integration verification — Task 12

No placeholders. All code blocks complete. Type names consistent (`tencentSource`,
`tencentResolver`, `parseTencentLocation`, `parseTencentSecretRef`,
`tencentcred.Resolve`, `tencentcred.ResetForTest`, `cosNewClient`,
`newTencentSecretsClient`, `fakeSecretsClient`).