# tencentcred: TKE pod identity STS → env → tccli chain

## Goal

Replace the existing `tencentcred` two-step chain (env → CVM/TKE metadata server)
with a three-step chain that targets TKE Pod Identity as the primary credential
source, with static credentials (env, then tccli profile) as fallback.

## Context

`internal/infrastructure/tencentcred/tencentcred.go` is consumed by:

- `internal/infrastructure/source/tencent.go` (COS object fetcher)
- `internal/infrastructure/secrets/tencent.go` (Secrets Manager adapter)

Both call `tencentcred.Resolve(ctx)` to get a `*Credentials` they then wire into
their respective SDK clients. Today the chain is:

1. `TENCENTCLOUD_SECRETID` / `TENCENTCLOUD_SECRETKEY` env vars
2. CVM/TKE metadata server (`http://metadata.tencentyun.com/.../cam/security-credentials/...`)

The metadata-server path returns STS-style temp creds
(`TmpSecretId` / `TmpSecretKey`) but does NOT include a `Token`. It also assumes
the binary runs inside a CVM or TKE node with the metadata service reachable,
which is not true for all pod-identity configurations.

## Design

### Credentials struct

Add a `Token` field. Empty for static creds; non-empty for STS web-identity creds.

```go
type Credentials struct {
    SecretID  string
    SecretKey string
    Token     string // SecurityToken; empty for static creds
}
```

### Chain

Order is fixed: **sts → env → tccli**. Each source is a function that returns
`(*Credentials, nil)` on success, `(nil, nil)` when "not configured" (so the
chain falls through), or `(nil, error)` when configured but broken.

```go
var sources = []func(ctx context.Context) (*Credentials, error){
    fromPodIdentitySTS, // TKE_ROLE_ARN + TKE_WEB_IDENTITY_TOKEN_FILE
    fromEnv,            // TENCENTCLOUD_SECRETID + TENCENTCLOUD_SECRETKEY
    fromTccliProfile,   // ~/.tencentcloud/credentials [default]
}

func Resolve(ctx context.Context) (*Credentials, error)
```

Iterate in order; first non-nil wins. Same process-lifetime cache as today
(mutex-guarded).

### Source 1: TKE Pod Identity STS

Activation: `TKE_ROLE_ARN` non-empty AND `TKE_WEB_IDENTITY_TOKEN_FILE`
points to a readable file. Otherwise source returns `(nil, nil)` and chain
falls through.

Implementation: use the Tencent Go SDK's built-in
`common.TkeRoleArnCredential` provider, which handles the
`AssumeRoleWithWebIdentity` exchange against the STS service. Read the JWT
from the token file, pass `RoleArn` from `TKE_ROLE_ARN`, optional
`ProviderId` from `TKE_PROVIDER_ID` (empty if unset — SDK default), optional
region from `TKE_REGION` (empty if unset).

The provider exposes `GetCredentials()` returning a struct with `SecretID`,
`SecretKey`, `Token`. Copy into `tencentcred.Credentials`.

If `TkeRoleArnCredential` is not present in the vendored SDK version, fall
back to a hand-rolled `AssumeRoleWithWebIdentity` call via the STS client
wrapped with the SDK's anonymous-credential mode (or raw HTTPS POST with
TC3-HMAC-SHA256 if neither ships). This fallback path is a `ponytail:`-marked
shortcut — upgrade by switching to the SDK helper when its API is confirmed.

### Source 2: Env

Activation: both `TENCENTCLOUD_SECRETID` and `TENCENTCLOUD_SECRETKEY` non-empty.
Otherwise `(nil, nil)`. Token left empty.

### Source 3: tccli profile

Read `$HOME/.tencentcloud/credentials` (INI format). Fall back to
`./.tencentcloud/credentials` if `HOME` is unset. Parse `[default]` section:
keys `secretId` and `secretKey`. Missing file or missing section/keys →
`(nil, nil)`. Malformed INI → `(nil, error)`.

Token always empty for this source.

### Error semantics

Single `ErrNoCredentials` with a message listing all three sources (sts, env,
tccli) so users see the fix path. No per-source detail (per design decision).

### Cache

Existing mutex-guarded `cached` / `cacheErr` retained. No TTL — process
lifetime, matching the current behavior.

## Caller updates

Both callers pass the new `Token` field when non-empty.

### `source/tencent.go` (COS)

The `cos.AuthorizationTransport` already takes `SecretID` / `SecretKey`. Wire
`Token` to its `SecurityToken` field (COS SDK v5 has it) when non-empty. When
Token is empty, leave `SecurityToken` unset (matches static-creds behavior).

### `secrets/tencent.go` (Secrets Manager)

The SDK's `ssm.NewClientWithSecretId` returns a client bound to static
creds. The Tencent SSM SDK accepts a `common.Profile` with `SecretId`,
`SecretKey`, `SecurityToken`. Build the profile via `common.NewCredential` or
equivalent, passing Token when non-empty. When Token is empty, use the
existing constructor.

## Files changed

- `internal/infrastructure/tencentcred/tencentcred.go` — chain impl
- `internal/infrastructure/tencentcred/tencentcred_test.go` — tests
- `internal/infrastructure/source/tencent.go` — wire Token to COS client
- `internal/infrastructure/source/tencent_test.go` — adjust tests
- `internal/infrastructure/secrets/tencent.go` — wire Token to SSM client
- `internal/infrastructure/secrets/tencent_test.go` — adjust tests

## Testing

`tencentcred_test.go` covers one test per source:

1. **STS active** — set `TKE_ROLE_ARN` + a fake token file; stub the SDK
   provider to return known creds; assert `Resolve` returns them and chain
   does not consult env.
2. **STS not configured** — leave STS env vars unset; set
   `TENCENTCLOUD_SECRETID/SECRETKEY`; assert env wins; tccli not consulted.
3. **Env not configured** — clear both env vars; write a fake tccli
   credentials file; assert tccli wins.
4. **All sources miss** — clear env, set STS env vars to empty file path,
   point tccli file at missing path; assert single `ErrNoCredentials` whose
   message mentions all three sources.
5. **Cache hit** — first call resolves via STS; second call must not re-read
   the token file (assert via fake file counter).

For each non-STS path we use the existing pattern: monkey-patch the source's
hook var to point at a stub function so tests don't touch the filesystem or
network.

## Out of scope

- TTL / refresh on STS creds (process-lifetime cache matches current
  behavior; refresh belongs to a separate task).
- Support for non-`default` tccli profiles (single profile matches the
  default tccli config; multiple profiles can be added later).
- Removing or deprecating the CVM metadata-server path (drop it; if anything
  outside `tencentcred` depends on it, this task adds the dependency, but
  no current caller does).