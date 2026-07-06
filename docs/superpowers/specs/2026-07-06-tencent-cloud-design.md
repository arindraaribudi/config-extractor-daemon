# Tencent Cloud Support

**Date:** 2026-07-06
**Status:** Approved (brainstorming)
**Scope:** Add Tencent Cloud as a third provider for both parameter fetch and secret resolution.

## Motivation

The daemon currently supports AWS (SSM Parameter Store + Secrets Manager) and GCP (Parameter Manager + Secret Manager). User teams run on Tencent Cloud (CAM/CVM/COS/Secrets Manager) and need the daemon to source configuration from their existing Tencent infra without standing up a separate AWS or GCP project.

Tencent has no direct equivalent of AWS SSM/GCP Parameter Manager that this team uses in production. They store parameter payloads as objects in COS (Cloud Object Storage) at a path that encodes project, app, and version, and pull secrets from Tencent Secrets Manager. This is the contract we integrate against.

## Goals

1. Fetch parameter payload from a COS object addressed by a full HTTPS URL.
2. Resolve `__SECRET_REF__(uri)` tokens pointing at Tencent Secrets Manager.
3. Reuse the existing plug-in architecture: new adapters in `internal/infrastructure/source/` and `internal/infrastructure/secrets/`, single-line additions to the registries. Zero changes to `domain/`, `application/`, or `cmd/`.
4. Match existing strictness semantics (`--strict-fetch`, `--strict-secret-refs`) and lazy-credential patterns.
5. Authenticate via CVM/TKE metadata server with AK/SK env-var fallback.

## Non-Goals

- TencentCloud Parameter Store (T-Sec-SSM) integration — user explicitly stores payloads in COS.
- Multi-region failover within a single Fetch call.
- Caching of fetched payloads.
- Binary secret payloads — string secrets only (mirrors AWS limitation).
- COS multipart downloads or presigned URL generation — payloads are small config files.

## Configuration

### Environment variables

| Var | Required | Default | Notes |
|---|---|---|---|
| `TENCENTCLOUD_SECRETID` | no | — | Tencent Cloud API AK. Optional when running on CVM/TKE with a CAM role attached. |
| `TENCENTCLOUD_SECRETKEY` | no | — | Tencent Cloud API SK. Same precedence rule. |

If neither env vars nor metadata are reachable at Fetch time, exit non-zero with an error naming the missing variables.

### CONFIG_LOCATION

Full HTTPS URL of the COS object. URL host encodes the bucket (including `-{appid}` suffix) and region. Path is the object key prefix; `CONFIG_VERSION` is appended as the final path segment.

Example:

```
CONFIG_LOCATION=https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig
CONFIG_VERSION=ENV-1
```

Resolves to:

- bucket: `cds-oms-sit-1409486316`
- region: `ap-bangkok`
- key: `projects/my-proj/parameters/appconfig/ENV-1`

### Secret ref URI

```
__SECRET_REF__(secretsmanager.tencentcloudapi.com/{region}/{secret-name})
```

Example: `__SECRET_REF__(secretsmanager.tencentcloudapi.com/ap-bangkok/db-password)` → `db-password` in region `ap-bangkok`.

Normalisation rules (whitespace, surrounding quotes, leading `//`) inherit unchanged from `domain.NormalizeRef`.

## Architecture

Existing plug-in layout is preserved. Three changes in three packages, plus docs and tests.

### `internal/domain/config_source.go`

Add `ProviderTencent` to the `ProviderKind` const block:

```go
const (
    ProviderGCP     ProviderKind = "gcp"
    ProviderAWS     ProviderKind = "aws"
    ProviderTencent ProviderKind = "tencent"
)
```

No other domain changes. `Reference{Location, Version}` already carries everything Tencent needs.

### `internal/infrastructure/source/tencent.go` (new)

Defines:

- `parseTencentLocation(location, version string) (bucket, region, key string, err error)` — pure function, exported only to the package's test file via package-private access.
- `tencentSource` struct holding a `*cos.Client` from `github.com/tencentyun/cos-go-sdk-v5`.
- `NewTencentSource(ctx context.Context, _ domain.FetchMode) (domain.ConfigSource, error)` — builds the client lazily via `tencentcred.Resolve(ctx)`.
- `(*tencentSource).Kind() domain.ProviderKind` — returns `ProviderTencent`.
- `(*tencentSource).Fetch(ctx, ref, mode) (domain.Payload, error)`:
  - `mode != FetchGet` → returns `domain.ErrUnsupportedMode` (only `get`; COS has no template rendering).
  - Calls `parseTencentLocation`.
  - Logs `tencent cos: fetching bucket=<bucket> region=<region> key=<key>`.
  - `client.Object.Get(ctx, key, nil)` → `io.ReadAll` → returns `domain.Payload`.

The client constructor is wrapped in a package-private `var cosNewClient = func(...)` so tests can inject a fake without real credentials (mirrors `loadAWSConfig` and `newSecretManagerClient`).

### `internal/infrastructure/source/registry.go`

Append one entry to `Sources`:

```go
{
    Kind:    domain.ProviderTencent,
    Match:   isTencentLocation,
    Factory: NewTencentSource,
},
```

`isTencentLocation` must come before the GCP fallback in match order. The current AWS entry already comes first; the new Tencent entry sits between AWS and GCP:

```go
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
```

Use `url.Parse` rather than substring slicing — buckets with hyphens and AppId suffixes make prefix matching brittle.

### `internal/infrastructure/secrets/tencent.go` (new)

Defines:

- `const tencentSecretPrefix = "secretsmanager.tencentcloudapi.com/"`
- `parseTencentSecretRef(ref string) (region, secretName string, err error)` — `Cut` on prefix, `Cut` on `/`.
- `tencentResolver` struct (mirrors `awsResolver`/`gcpResolver`).
- `NewTencentResolver() domain.SecretResolver` — returns zero-value resolver; client built lazily.
- `(*tencentResolver).Supports(ref string) bool` — prefix match.
- `(*tencentResolver).Resolve(ctx, ref) (string, error)`:
  - Parse ref.
  - Lazy-build client via `tencentcred.Resolve(ctx)` and `secretmanager.NewClient` from `github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/secretsmanager/v20220123`.
  - Call `GetSecretValue(SecretName=secretName)`.
  - Return `resp.Response.SecretString` or error if binary.

Client construction is wrapped in a package-private `var newTencentSecretsClient = func(...)` to enable tests.

### `internal/infrastructure/secrets/registry.go`

Append:

```go
{Kind: domain.ProviderTencent, Resolver: NewTencentResolver()},
```

Dispatch remains `Supports`-driven — order does not matter.

### `internal/infrastructure/tencentcred/tencentcred.go` (new, shared)

Tiny package to avoid credential drift between source and secrets adapters:

```go
package tencentcred

type Credentials struct{ SecretID, SecretKey string }

// Resolve returns AK/SK by trying, in order:
//  1. TENCENTCLOUD_SECRETID + TENCENTCLOUD_SECRETKEY env vars.
//  2. CVM/TKE metadata server at http://metadata.tencentyun.com/latest/meta-data/cam/security-credentials/<role-name>.
//     If the role-name endpoint returns 404, the caller is not on CVM/TKE — return an error listing env vars.
// Returns ErrNoCredentials when neither path yields a pair.
```

Cache the resolved credentials for the process lifetime (single Fetch call per process; caching avoids two metadata hits if both adapters run).

## Error handling

- Invalid `CONFIG_LOCATION` (not a URL, missing host, non-COS host) → returned from `parseTencentLocation` with a precise message. Surface wrapped to the use case.
- `mode == "render"` → `domain.ErrUnsupportedMode` (matches AWS pattern).
- COS `Object.Get` 404 → error from SDK wrapped as `tencent cos: GetObject %q: %w`.
- COS auth failure → wrapped as `tencent cos: GetObject %q (region=%s): %w`.
- Secret ref with missing region or empty secret name → `tencent secret ref %q: expected {region}/{secret-name}`.
- Secrets Manager auth failure → wrapped as `tencent secretsmanager get secret %q: %w`.
- Binary secret payload → `tencent secretsmanager %q: binary secrets are not supported`.
- `--strict-fetch=false` and Fetch fails → log warn, return empty payload (existing use-case behaviour).
- `--strict-secret-refs=false` and Resolve fails → log warn, leave `__SECRET_REF__(...)` literal in value (existing behaviour).

## Testing strategy

No live cloud credentials needed. Tests mirror the existing GCP/AWS patterns.

### `internal/infrastructure/source/tencent_test.go`

- `TestParseTencentLocation`: table-driven — full URL + version appended, URL missing version path, non-https scheme (reject), host without `.cos.` (reject), host without `.myqcloud.com` (reject), unparseable URL.
- `TestTencentSource_Fetch_UnsupportedMode`: `mode=render` returns `ErrUnsupportedMode`.
- `TestTencentSource_Fetch_HTTPError`: inject a fake COS client returning an error; assert wrapping.

### `internal/infrastructure/secrets/tencent_test.go`

- `TestParseTencentSecretRef`: valid, missing slash, empty region, empty secret name.
- `TestTencentResolver_Supports`: prefix match and reject.
- `TestTencentResolver_Resolve_Success` / `_HTTPError`: inject a fake `secretmanagerClient` interface (mirror GCP's `secretAccessClient`).

### `internal/infrastructure/tencentcred/tencentcred_test.go`

- `TestResolve_EnvVars`: both env vars set → returns env values, no metadata hit.
- `TestResolve_MetadataFallback`: env unset, metadata server returns role creds → returns metadata values. Use `httptest.Server` to fake the metadata endpoint.
- `TestResolve_NoCredentials`: env unset, metadata unreachable → returns `ErrNoCredentials` with the env-var names in the message.

### Coverage targets

Maintain the existing project coverage (mirrored from `main_test.go` and `secret_ref_test.go` style). No new code without a test.

## Documentation updates

### `README.md`

- Add `TENCENTCLOUD_SECRETID`, `TENCENTCLOUD_SECRETKEY` rows to the env-vars table.
- Add a Tencent row to the `CONFIG_LOCATION` examples showing the COS URL form.
- Add a Tencent row to the secret-refs examples.

### `CLOUD_SUPPORT.md`

- New section "Tencent Cloud" placed after AWS, before the closing architecture notes.
- Cover: provider detection (URL host check), fetcher wiring (registry append), end-to-end flow (Fetch → ParsePayload → ResolveSecretRefs → write), credential resolution chain, COS URL form, secret ref form, supported/unsupported modes.

### `CHANGELOG` (if maintained)

- Entry under next version: "Add Tencent Cloud provider (COS parameter store + Secrets Manager)."

## Dependencies

Add to `go.mod`:

- `github.com/tencentyun/cos-go-sdk-v5` — COS client (slim, dedicated).
- `github.com/tencentcloud/tencentcloud-sdk-go` — Secrets Manager client (only the `tencentcloud/secretsmanager/v20220123` sub-package is imported at call sites; Go modules pulls the whole module but only the imported sub-package compiles in).

No indirect dependencies beyond what those two already require.

## Out of scope (deferred)

- TencentCloud Parameter Store (T-Sec-SSM) — user uses COS, not needed.
- COS bucket-as-static-website fallback — irrelevant.
- TencentCloud TKE ConfigMap source — out of scope.
- Caching layer for either COS objects or secrets — not requested; revisit if cold-start latency becomes a problem.
- Metrics / tracing — daemon doesn't emit either today.

## Rollout

- Feature ships behind no flag — provider detection is location-driven, so it activates only when `CONFIG_LOCATION` matches `isTencentLocation`.
- No migration needed for existing AWS/GCP users.
- New deployment must populate `TENCENTCLOUD_SECRETID`/`SECRETKEY` or run on a CVM/TKE instance with the right CAM role bound.