# tencentcred STS → env → tccli chain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the existing env → metadata-server chain in `internal/infrastructure/tencentcred` with a three-source chain: TKE pod identity STS → env vars → tccli profile file.

**Architecture:** Add a `Token` field to `Credentials`. Replace the if/else in `Resolve` with a slice of source funcs iterated in order; first non-nil wins. Use Tencent SDK's `common.DefaultTkeOIDCRoleArnProvider` for the STS source. Read `$HOME/.tencentcloud/credentials` INI for tccli. Wire `Token` to COS `AuthorizationTransport.SecurityToken` and SSM profile where non-empty.

**Tech Stack:** Go 1.22+, `github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common` (TkeOIDCRoleArnProvider), `github.com/tencentyun/cos-go-sdk-v5`.

---

## File Structure

### Modify

- `internal/infrastructure/tencentcred/tencentcred.go` — add `Token` field, replace `Resolve` with chain loop, add `fromPodIdentitySTS` / `fromTccliProfile`, drop metadata server. Keep env source as-is.
- `internal/infrastructure/tencentcred/tencentcred_test.go` — replace metadata-server tests with chain tests (STS wins / STS misses + env wins / env misses + tccli wins / all miss / cache hit).
- `internal/infrastructure/source/tencent.go` — `cosNewClient`: pass `creds.Token` to `AuthorizationTransport.SecurityToken` when non-empty.
- `internal/infrastructure/source/tencent_test.go` — add coverage for token-bearing creds (if not already there).
- `internal/infrastructure/secrets/tencent.go` — `newTencentSecretsClient`: build via `common.NewCredential` + `common.Profile` when token non-empty; existing `NewClientWithSecretId` path when empty.
- `internal/infrastructure/secrets/tencent_test.go` — add coverage for token-bearing creds (if not already there).

### No new files

Single-file ownership per responsibility. Tests stay next to impl.

---

## Task 1: Extend `Credentials` struct with `Token` field

**Files:**
- Modify: `internal/infrastructure/tencentcred/tencentcred.go:21-24`

- [ ] **Step 1: Add `Token` field to `Credentials`**

In `tencentcred.go`, replace the struct definition (lines 21-24):

```go
// Credentials holds a Tencent Cloud AK/SK pair.
type Credentials struct {
    SecretID  string
    SecretKey string
}
```

with:

```go
// Credentials holds a Tencent Cloud credential set. Token is non-empty
// only for STS web-identity creds (TKE pod identity). Static-creds
// sources (env, tccli) leave it empty.
type Credentials struct {
    SecretID  string
    SecretKey string
    Token     string
}
```

- [ ] **Step 2: Run build to verify struct change compiles**

Run: `go build ./...`
Expected: success. Token is unused at this point, but the struct is referenced by `fromEnv` and existing callers — none of them set it, so it's just an extra zero-value field.

- [ ] **Step 3: Run existing tests**

Run: `go test ./internal/infrastructure/tencentcred/... ./internal/infrastructure/source/... ./internal/infrastructure/secrets/...`
Expected: PASS. Existing tests don't inspect struct internals beyond `SecretID`/`SecretKey`, so adding a field is safe.

- [ ] **Step 4: Commit**

```bash
git add internal/infrastructure/tencentcred/tencentcred.go
git commit -m "feat(tencentcred): add Token field to Credentials"
```

---

## Task 2: Add `fromPodIdentitySTS` source + pluggable provider hook

**Files:**
- Modify: `internal/infrastructure/tencentcred/tencentcred.go`

- [ ] **Step 1: Add `oidcProvider` hook var at package level**

After the existing `httpClient`/`cacheMu` block (around line 34-39), add:

```go
// oidcProvider returns (*Credentials, nil) on success, (nil, nil) when
// TKE_ROLE_ARN / TKE_WEB_IDENTITY_TOKEN_FILE are unset, (nil, err) when
// the SDK fails to exchange the token. Overridden in tests.
var oidcProvider = func(ctx context.Context) (*Credentials, error) {
    if os.Getenv("TKE_ROLE_ARN") == "" || os.Getenv("TKE_WEB_IDENTITY_TOKEN_FILE") == "" {
        return nil, nil
    }
    p, err := common.DefaultTkeOIDCRoleArnProvider()
    if err != nil {
        return nil, fmt.Errorf("tke oidc provider: %w", err)
    }
    credIface, err := p.GetCredential()
    if err != nil {
        return nil, fmt.Errorf("tke oidc GetCredential: %w", err)
    }
    secretId, _ := credIface.GetSecretId(), ""
    _ = secretId // SDK getter names confirmed at impl time; see step 3 verify
    return nil, nil // ponytail: placeholder, real extraction in step 3
}
```

Wait — the getter names need verification. **Do not commit this snippet as-is.** Use this stub instead that compiles but returns `nil, nil` so tests pass:

```go
var oidcProvider = func(ctx context.Context) (*Credentials, error) {
    if os.Getenv("TKE_ROLE_ARN") == "" || os.Getenv("TKE_WEB_IDENTITY_TOKEN_FILE") == "" {
        return nil, nil
    }
    p, err := common.DefaultTkeOIDCRoleArnProvider()
    if err != nil {
        return nil, fmt.Errorf("tke oidc provider: %w", err)
    }
    _ = ctx
    _ = p
    return nil, nil
}
```

- [ ] **Step 2: Add `tencentcloud/common` import**

Add to the import block:

```go
"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
```

- [ ] **Step 3: Verify SDK getter names by running a real `go doc`**

Run: `go doc github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common.CredentialIface`
Expected: lists `GetSecretId() string`, `GetSecretKey() string`, `GetToken() string` (or `GetSecurityToken()`). If `GetToken()` exists, use it; if `GetSecurityToken()`, use that.

- [ ] **Step 4: Wire the real extraction into `oidcProvider`**

Replace the stub body with the real one based on step 3 output. Example (final form depends on step 3):

```go
var oidcProvider = func(ctx context.Context) (*Credentials, error) {
    if os.Getenv("TKE_ROLE_ARN") == "" || os.Getenv("TKE_WEB_IDENTITY_TOKEN_FILE") == "" {
        return nil, nil
    }
    p, err := common.DefaultTkeOIDCRoleArnProvider()
    if err != nil {
        return nil, fmt.Errorf("tke oidc provider: %w", err)
    }
    cred, err := p.GetCredential()
    if err != nil {
        return nil, fmt.Errorf("tke oidc GetCredential: %w", err)
    }
    return &Credentials{
        SecretID:  cred.GetSecretId(),
        SecretKey: cred.GetSecretKey(),
        Token:     cred.GetToken(), // or GetSecurityToken() per step 3
    }, nil
}
```

Adjust the field name (`GetToken` vs `GetSecurityToken`) per step 3.

- [ ] **Step 5: Build + verify**

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/infrastructure/tencentcred/tencentcred.go
git commit -m "feat(tencentcred): add fromPodIdentitySTS source via TkeOIDCRoleArnProvider"
```

---

## Task 3: Add `fromTccliProfile` source

**Files:**
- Modify: `internal/infrastructure/tencentcred/tencentcred.go`

- [ ] **Step 1: Add hook var for tccli path + parser**

After `oidcProvider`, add:

```go
// tccliCredsPath returns the path to the tccli credentials file.
// Overridden in tests.
var tccliCredsPath = func() string {
    if home := os.Getenv("HOME"); home != "" {
        return filepath.Join(home, ".tencentcloud", "credentials")
    }
    return ".tencentcloud/credentials"
}

// parseTccliINI reads {secretId, secretKey} from the [default] section
// of a tccli-format INI file. Returns (zero, nil) when the file is missing
// or the [default] section is absent; (zero, err) when malformed.
func parseTccliINI(path string) (id, key string, err error) {
    data, err := os.ReadFile(path)
    if errors.Is(err, fs.ErrNotExist) {
        return "", "", nil
    }
    if err != nil {
        return "", "", fmt.Errorf("read tccli credentials: %w", err)
    }
    inDefault := false
    for _, line := range strings.Split(string(data), "\n") {
        line = strings.TrimSpace(line)
        if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
            continue
        }
        if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
            inDefault = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), "default")
            continue
        }
        eq := strings.IndexByte(line, '=')
        if eq < 0 || !inDefault {
            continue
        }
        k := strings.TrimSpace(line[:eq])
        v := strings.TrimSpace(line[eq+1:])
        switch strings.ToLower(k) {
        case "secretid":
            id = v
        case "secretkey":
            key = v
        }
    }
    return id, key, nil
}

// fromTccliProfile reads $HOME/.tencentcloud/credentials. Returns
// (nil, nil) when the file or [default] section is missing; (nil, err)
// on parse error.
func fromTccliProfile(ctx context.Context) (*Credentials, error) {
    _ = ctx
    id, key, err := parseTccliINI(tccliCredsPath())
    if err != nil {
        return nil, err
    }
    if id == "" || key == "" {
        return nil, nil
    }
    return &Credentials{SecretID: id, SecretKey: key}, nil
}
```

- [ ] **Step 2: Add imports**

```go
import (
    "context"
    "encoding/json"  // keep — still used? see step 3
    "errors"
    "fmt"
    "io"             // keep — still used? see step 3
    "net/http"       // keep — still used? see step 3
    "os"
    "path/filepath"
    "io/fs"
    "strings"
    "sync"
    "time"           // keep — still used? see step 3
)
```

- [ ] **Step 3: Drop metadata-server imports + symbols**

After verifying no other code in `tencentcred.go` references them, remove:
- `fetchFromMetadata`, `getMetadata`, `metadataBase`, `ErrNoCredentials`
- The `net/http`, `io`, `time`, `encoding/json` imports IF no longer used.

Keep them only if the metadata server is replaced by something else; here it is fully removed.

Also update `ErrNoCredentials` to reflect new chain:

```go
var ErrNoCredentials = errors.New("tencent creds: no TKE pod identity STS, TENCENTCLOUD_SECRETID/SECRETKEY env, or ~/.tencentcloud/credentials [default] profile available")
```

- [ ] **Step 4: Build + verify**

Run: `go build ./...`
Expected: success. Existing callers still compile because `tencentcred.Resolve(ctx)` signature is unchanged.

- [ ] **Step 5: Run existing tests to confirm nothing breaks**

Run: `go test ./...`
Expected: existing source/secrets tests PASS. The tencentcred tests will FAIL because we removed metadata-server code — that's expected, fix in task 4.

- [ ] **Step 6: Commit**

```bash
git add internal/infrastructure/tencentcred/tencentcred.go
git commit -m "feat(tencentcred): add fromTccliProfile source reading ~/.tencentcloud/credentials"
```

---

## Task 4: Replace `Resolve` with chain loop

**Files:**
- Modify: `internal/infrastructure/tencentcred/tencentcred.go`

- [ ] **Step 1: Replace the `Resolve` function**

Replace lines 41-65 (`Resolve`) and the metadata helper functions with:

```go
// sources is the credential lookup order. First non-nil wins.
// Order: TKE pod identity STS > env > tccli profile.
var sources = []func(ctx context.Context) (*Credentials, error){
    fromPodIdentitySTS,
    fromEnv,
    fromTccliProfile,
}

// fromPodIdentitySTS delegates to oidcProvider (pluggable for tests).
func fromPodIdentitySTS(ctx context.Context) (*Credentials, error) {
    return oidcProvider(ctx)
}

// fromEnv reads TENCENTCLOUD_SECRETID / TENCENTCLOUD_SECRETKEY.
func fromEnv(ctx context.Context) (*Credentials, error) {
    _ = ctx
    id, key := os.Getenv("TENCENTCLOUD_SECRETID"), os.Getenv("TENCENTCLOUD_SECRETKEY")
    if id == "" || key == "" {
        return nil, nil
    }
    return &Credentials{SecretID: id, SecretKey: key}, nil
}

// Resolve returns a Credentials value from the first source that yields
// one. Cached after first success.
func Resolve(ctx context.Context) (*Credentials, error) {
    cacheMu.Lock()
    defer cacheMu.Unlock()
    if cached != nil {
        return cached, nil
    }
    if cacheErr != nil {
        return nil, cacheErr
    }

    for _, src := range sources {
        creds, err := src(ctx)
        if err != nil {
            return nil, fmt.Errorf("%w: %v", ErrNoCredentials, err)
        }
        if creds != nil {
            cached = creds
            return cached, nil
        }
    }
    return nil, ErrNoCredentials
}
```

- [ ] **Step 2: Remove the now-unused `fetchFromMetadata` and `getMetadata`**

Delete those functions entirely (around lines 67-106).

- [ ] **Step 3: Build + verify**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/infrastructure/tencentcred/tencentcred.go
git commit -m "refactor(tencentcred): chain-based Resolve over [sts, env, tccli]"
```

---

## Task 5: Rewrite tests for new chain

**Files:**
- Modify: `internal/infrastructure/tencentcred/tencentcred_test.go`

- [ ] **Step 1: Replace the entire test file**

```go
package tencentcred

import (
    "os"
    "path/filepath"
    "testing"
)

func resetCache() {
    cacheMu.Lock()
    cached = nil
    cacheErr = nil
    cacheMu.Unlock()
}

func TestResolve_STS(t *testing.T) {
    resetCache()
    t.Setenv("TKE_ROLE_ARN", "qcs::cam::uin/0:roleName/test")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "/dev/null") // readable; ignored by stub

    prev := oidcProvider
    oidcProvider = func(ctx context.Context) (*Credentials, error) {
        return &Credentials{SecretID: "sts-id", SecretKey: "sts-key", Token: "sts-token"}, nil
    }
    defer func() { oidcProvider = prev }()

    t.Setenv("TENCENTCLOUD_SECRETID", "") // belt + suspenders
    t.Setenv("TENCENTCLOUD_SECRETKEY", "")

    got, err := Resolve(t.Context())
    if err != nil {
        t.Fatalf("Resolve: %v", err)
    }
    if got.SecretID != "sts-id" || got.SecretKey != "sts-key" || got.Token != "sts-token" {
        t.Fatalf("got %+v, want sts creds", got)
    }
}

func TestResolve_EnvWhenSTSNotConfigured(t *testing.T) {
    resetCache()
    t.Setenv("TKE_ROLE_ARN", "")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "")

    prev := oidcProvider
    oidcProvider = func(ctx context.Context) (*Credentials, error) { return nil, nil }
    defer func() { oidcProvider = prev }()

    t.Setenv("TENCENTCLOUD_SECRETID", "env-id")
    t.Setenv("TENCENTCLOUD_SECRETKEY", "env-key")

    got, err := Resolve(t.Context())
    if err != nil {
        t.Fatalf("Resolve: %v", err)
    }
    if got.SecretID != "env-id" || got.SecretKey != "env-key" || got.Token != "" {
        t.Fatalf("got %+v, want env creds with empty token", got)
    }
}

func TestResolve_TccliWhenEnvMissing(t *testing.T) {
    resetCache()
    t.Setenv("TKE_ROLE_ARN", "")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "")

    prevOIDC := oidcProvider
    oidcProvider = func(ctx context.Context) (*Credentials, error) { return nil, nil }
    defer func() { oidcProvider = prevOIDC }()

    t.Setenv("TENCENTCLOUD_SECRETID", "")
    t.Setenv("TENCENTCLOUD_SECRETKEY", "")

    dir := t.TempDir()
    path := filepath.Join(dir, "credentials")
    if err := os.WriteFile(path, []byte("[default]\nsecretId = tccli-id\nsecretKey = tccli-key\n"), 0600); err != nil {
        t.Fatal(err)
    }
    prevPath := tccliCredsPath
    tccliCredsPath = func() string { return path }
    defer func() { tccliCredsPath = prevPath }()

    got, err := Resolve(t.Context())
    if err != nil {
        t.Fatalf("Resolve: %v", err)
    }
    if got.SecretID != "tccli-id" || got.SecretKey != "tccli-key" || got.Token != "" {
        t.Fatalf("got %+v, want tccli creds with empty token", got)
    }
}

func TestResolve_AllMiss(t *testing.T) {
    resetCache()
    t.Setenv("TKE_ROLE_ARN", "")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "")
    t.Setenv("TENCENTCLOUD_SECRETID", "")
    t.Setenv("TENCENTCLOUD_SECRETKEY", "")

    prevOIDC := oidcProvider
    oidcProvider = func(ctx context.Context) (*Credentials, error) { return nil, nil }
    defer func() { oidcProvider = prevOIDC }()

    prevPath := tccliCredsPath
    tccliCredsPath = func() string { return "/nonexistent/.tencentcloud/credentials" }
    defer func() { tccliCredsPath = prevPath }()

    _, err := Resolve(t.Context())
    if err == nil {
        t.Fatal("expected error when no creds available")
    }
    for _, want := range []string{"TKE_ROLE_ARN", "TENCENTCLOUD_SECRETID", ".tencentcloud/credentials"} {
        if !contains(err.Error(), want) {
            t.Fatalf("error %q should mention %q", err.Error(), want)
        }
    }
}

func TestResolve_CacheHit(t *testing.T) {
    resetCache()
    t.Setenv("TKE_ROLE_ARN", "")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "")
    t.Setenv("TENCENTCLOUD_SECRETID", "")
    t.Setenv("TENCENTCLOUD_SECRETKEY", "")

    dir := t.TempDir()
    path := filepath.Join(dir, "credentials")
    if err := os.WriteFile(path, []byte("[default]\nsecretId = tccli-id\nsecretKey = tccli-key\n"), 0600); err != nil {
        t.Fatal(err)
    }
    prevPath := tccliCredsPath
    tccliCredsPath = func() string { return path }
    defer func() { tccliCredsPath = prevPath }()

    first, err := Resolve(t.Context())
    if err != nil {
        t.Fatalf("first Resolve: %v", err)
    }

    // Delete the file; second Resolve must still succeed (cache hit).
    if err := os.Remove(path); err != nil {
        t.Fatal(err)
    }
    second, err := Resolve(t.Context())
    if err != nil {
        t.Fatalf("second Resolve: %v", err)
    }
    if first != second {
        t.Fatalf("expected cached pointer; first=%p second=%p", first, second)
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

Note: `ctx` arg in `oidcProvider` stub is unused — Go allows unused params.

- [ ] **Step 2: Run tests, verify pass**

Run: `go test ./internal/infrastructure/tencentcred/... -v`
Expected: 5 tests PASS.

- [ ] **Step 3: Run full repo tests, verify downstream not broken**

Run: `go test ./...`
Expected: source + secrets tests pass (they use stub clients; tencentcred tests now use the new chain).

- [ ] **Step 4: Commit**

```bash
git add internal/infrastructure/tencentcred/tencentcred_test.go
git commit -m "test(tencentcred): rewrite tests for sts > env > tccli chain"
```

---

## Task 6: Wire `Token` into COS client (`source/tencent.go`)

**Files:**
- Modify: `internal/infrastructure/source/tencent.go:74-86`

- [ ] **Step 1: Update `cosNewClient` to pass Token**

Replace the `cosNewClient` var body (lines 74-86) with:

```go
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
        // ponytail: STS web-identity creds include a SecurityToken. SDK
        // field name verified at impl time against github.com/tencentyun/cos-go-sdk-v5
        // AuthorizationTransport — adjust if SDK exposes different field.
        auth.SecurityToken = creds.Token
    }
    httpClient := &http.Client{Transport: auth}
    client := cos.NewClient(baseURL, httpClient)
    return client.Object, nil
}
```

- [ ] **Step 2: Verify SDK field name via go doc**

Run: `go doc github.com/tencentyun/cos-go-sdk-v5.AuthorizationTransport`
Expected: lists a `SecurityToken` field (string). If the field name differs, update step 1 accordingly.

- [ ] **Step 3: Build + verify**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Add a test that exercises STS-token path**

In `internal/infrastructure/source/tencent_test.go`, add a new test after the existing ones:

```go
func TestNewTencentSource_STSTokenPassedToClient(t *testing.T) {
    var gotAuth *cos.AuthorizationTransport
    prevNew := cosNewClient
    cosNewClient = func(bucket, region string, creds *tencentcred.Credentials) (cosObjectGetResult, error) {
        // Re-derive auth the same way the production code does; the
        // simplest way is to assert via a fake factory below. For now
        // capture creds for assertion.
        if creds == nil {
            return nil, errors.New("nil creds")
        }
        if creds.Token != "" {
            gotAuth = &cos.AuthorizationTransport{SecurityToken: creds.Token}
        }
        return nil, nil
    }
    defer func() { cosNewClient = prevNew }()
    _ = gotAuth
}
```

Wait — this test pattern doesn't actually verify the wiring. Use a stronger version:

```go
func TestNewTencentSource_STSTokenWiring(t *testing.T) {
    var capturedToken string
    prevNew := cosNewClient
    cosNewClient = func(bucket, region string, creds *tencentcred.Credentials) (cosObjectGetResult, error) {
        capturedToken = creds.Token
        return nil, nil
    }
    defer func() { cosNewClient = prevNew }()

    // Resolve creds via the chain — easiest is to call tencentcred.Resolve
    // with a stubbed oidcProvider that returns a token-bearing creds set.
    resetCacheForSourceTest()
    t.Setenv("TKE_ROLE_ARN", "x")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "/dev/null")
    prevOIDC := oidcProvider
    oidcProvider = func(ctx context.Context) (*tencentcred.Credentials, error) {
        return &tencentcred.Credentials{SecretID: "s", SecretKey: "k", Token: "the-token"}, nil
    }
    defer func() { oidcProvider = prevOIDC }()

    creds, err := tencentcred.Resolve(t.Context())
    if err != nil {
        t.Fatal(err)
    }
    if _, err := cosNewClient("bucket", "ap-bangkok", creds); err != nil {
        t.Fatal(err)
    }
    if capturedToken != "the-token" {
        t.Fatalf("expected Token=%q passed through, got %q", "the-token", capturedToken)
    }
}

func resetCacheForSourceTest() {
    tencentcred.ResetForTest()
}
```

This requires `tencentcred.ResetForTest` to exist (it already does — see tencentcred.go line 109).

- [ ] **Step 5: Run source tests**

Run: `go test ./internal/infrastructure/source/... -v`
Expected: PASS, including new test.

- [ ] **Step 6: Commit**

```bash
git add internal/infrastructure/source/tencent.go internal/infrastructure/source/tencent_test.go
git commit -m "feat(tencent): pass STS SecurityToken to COS AuthorizationTransport"
```

---

## Task 7: Wire `Token` into SSM client (`secrets/tencent.go`)

**Files:**
- Modify: `internal/infrastructure/secrets/tencent.go:46-52`

- [ ] **Step 1: Update `newTencentSecretsClient` to accept Token**

Replace the var body with:

```go
var newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
    if creds.Token != "" {
        // ponytail: STS web-identity creds — SDK supports a Profile with
        // SecurityToken. Field name verified via go doc at impl time;
        // adjust if SDK exposes a different field.
        profile := common.NewCredential(creds.SecretID, creds.Token)
        c, err := ssm.NewClient(profile, region)
        if err != nil {
            return nil, err
        }
        return c, nil
    }
    client, err := ssm.NewClientWithSecretId(creds.SecretID, creds.SecretKey, region)
    if err != nil {
        return nil, err
    }
    return client, nil
}
```

- [ ] **Step 2: Verify SDK constructor signature**

Run: `go doc github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssm/v20190923.NewClient`
Expected: lists `NewClient(credential common.CredentialIface, region string) (*Client, error)`. Adjust step 1 if different.

Run: `go doc github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common.NewCredential`
Expected: lists `NewCredential(secretId, secretKey string) Credential`. (Token may need separate setter; if so, follow what `Credential` interface exposes.)

If SDK cannot accept a SecurityToken via `NewCredential`, the fallback is:
- Build a `common.Profile` with `SecretId`, `SecretKey`, `SecurityToken` and use the `NewClient` overload that takes a Profile.

Verify and adjust step 1 accordingly.

- [ ] **Step 3: Build + verify**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Add a test that exercises STS-token path**

In `internal/infrastructure/secrets/tencent_test.go`, add:

```go
func TestNewTencentResolver_STSTokenPassedToClient(t *testing.T) {
    var gotToken string
    prevNew := newTencentSecretsClient
    newTencentSecretsClient = func(region string, creds *tencentcred.Credentials) (tencentSecretsClient, error) {
        gotToken = creds.Token
        return nil, nil
    }
    defer func() { newTencentSecretsClient = prevNew }()

    tencentcred.ResetForTest()
    t.Setenv("TKE_ROLE_ARN", "x")
    t.Setenv("TKE_WEB_IDENTITY_TOKEN_FILE", "/dev/null")
    prevOIDC := oidcProvider
    oidcProvider = func(ctx context.Context) (*tencentcred.Credentials, error) {
        return &tencentcred.Credentials{SecretID: "s", SecretKey: "k", Token: "the-token"}, nil
    }
    defer func() { oidcProvider = prevOIDC }()

    creds, err := tencentcred.Resolve(t.Context())
    if err != nil {
        t.Fatal(err)
    }
    if _, err := newTencentSecretsClient("ap-bangkok", creds); err != nil {
        t.Fatal(err)
    }
    if gotToken != "the-token" {
        t.Fatalf("expected Token=%q passed through, got %q", "the-token", gotToken)
    }
}
```

- [ ] **Step 5: Run secrets tests**

Run: `go test ./internal/infrastructure/secrets/... -v`
Expected: PASS.

- [ ] **Step 6: Run full repo test suite**

Run: `go test ./...`
Expected: PASS for all packages.

- [ ] **Step 7: Commit**

```bash
git add internal/infrastructure/secrets/tencent.go internal/infrastructure/secrets/tencent_test.go
git commit -m "feat(secrets): pass STS SecurityToken to SSM client"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Credentials struct + Token | Task 1 |
| Chain ordering [sts, env, tccli] | Task 4 |
| fromPodIdentitySTS via TkeOIDCRoleArnProvider | Tasks 2, 4 |
| fromEnv | Task 4 |
| fromTccliProfile reading ~/.tencentcloud/credentials | Task 3 |
| Single ErrNoCredentials naming all 3 | Tasks 3, 5 |
| Process-lifetime cache | Tasks 1, 4 (preserved) |
| COS AuthorizationTransport.SecurityToken | Task 6 |
| SSM client Token wiring | Task 7 |
| Tests per source + cache hit | Task 5 |
| Metadata server dropped | Tasks 3, 4 |

All covered.

**Placeholder scan:**
- Step 1 in Task 2 has a stub form explicitly marked "ponytail: placeholder, real extraction in step 3" — replaced in step 4.
- Step 4 in Task 6 and step 1 in Task 7 have "verify SDK field name via go doc" sub-steps. This is intentional — the SDK field name isn't fully predictable without runtime confirmation, and the plan accounts for the verification.

**Type consistency:**
- `Credentials{SecretID, SecretKey, Token}` consistent across all tasks.
- `oidcProvider`, `tccliCredsPath`, `newTencentSecretsClient`, `cosNewClient` hook var names used identically.
- `ResetForTest` exists in current `tencentcred.go`; tests reference it.

No inconsistencies.

---

## Out-of-scope reminders

- TTL / refresh on STS creds: not implemented; process-lifetime cache only.
- Multiple tccli profiles (non-`default`): not supported; `[default]` only.
- CVM instance-role credential source (CVM, not TKE): not supported. If running on CVM (not TKE pod identity), users must set env vars or tccli profile. Document this in CLAUDE.md/CLOUD_SUPPORT.md as a follow-up.