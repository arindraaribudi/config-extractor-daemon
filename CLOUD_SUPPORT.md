# Cloud Support Reference

Init-container binary: fetches a parameter store payload from one cloud, parses into env pairs, replaces `__SECRET_REF__(uri)` placeholders via registered secret resolvers, then either writes `.env` or injects into a child process.

## Layout

| Layer | Path | Notes |
|---|---|---|
| Domain | `internal/domain/` | Ports (`ConfigSource`, `SecretResolver`), pure types, no SDKs |
| Application | `internal/application/` | Use cases that orchestrate ports |
| Infrastructure | `internal/infrastructure/source/`, `internal/infrastructure/secrets/` | Adapters + registries (the only files that import SDKs) |
| Entry | `cmd/config-extractor/main.go` | Flag parse, env load, run use cases |

## Provider Dispatch

`internal/infrastructure/source/registry.go` holds the global `Sources`
slice. The first entry whose `Match(location)` returns true wins. The GCP
entry is a catch-all and must remain last.

| Location prefix | Matched by | Adapter |
|---|---|---|
| `arn:aws:ssm:` | `isSSMLocation` | `aws.go` (SSM Parameter Store) |
| `/{path}` | `isSSMLocation` | `aws.go` (bare SSM name; region from `AWS_REGION`) |
| anything else | (GCP catch-all) | `gcp.go` (Parameter Manager) |

## Support Matrix

| Cloud | Parameter Store | SDK | Fetch Modes | Secret Ref Backend |
|---|---|---|---|---|
| GCP | Parameter Manager | `cloud.google.com/go/parametermanager` | `get` (raw), `render` (template) | Secret Manager (`AccessSecretVersion`) |
| AWS | SSM Parameter Store | `github.com/aws/aws-sdk-go-v2/service/ssm` | `get` only (`render` errors out) | Secrets Manager (`GetSecretValue`) |

## Pipeline

```
CONFIG_LOCATION + CONFIG_VERSION
        │
        ▼
LoadConfigUseCase.Run
   iterates Sources registry, picks first Match
        │
        ▼
domain.ConfigSource.Fetch(ctx, Reference, mode)
   GCP: GetParameterVersion / RenderParameterVersion
   AWS: SSM.GetParameter
        │
        ▼
domain.ParsePayload(payload) → []EnvPair
        │
        ▼
ResolveSecretsUseCase.Run
   iterates secrets.Resolvers, picks first Supports
        │
        ▼
mode=env  → WriteEnvUseCase.Run → --out file (0600)
mode=exec → ExecChildUseCase.Run → child process with merged env
```

## GCP Flow

### Parameter fetch (`internal/infrastructure/source/gcp.go`)

```
CONFIG_LOCATION = projects/{project}/locations/{location}/parameters/{id}
CONFIG_VERSION  = dev-1
name            = {location}/versions/{version}
```

| Mode | RPC | Returns |
|---|---|---|
| `get` | `GetParameterVersion` | `resp.Payload.Data` (raw bytes) |
| `render` | `RenderParameterVersion` | `resp.RenderedPayload` (template vars resolved) |

### Secret ref (`internal/infrastructure/secrets/gcp.go`)

```
__SECRET_REF__(secretmanager.googleapis.com/projects/{p}/secrets/{s}/versions/{v})
                          │
                          ▼ strip prefix
   AccessSecretVersion(Name="projects/{p}/secrets/{s}/versions/{v}")
```

`domain.NormalizeRef` (runs before resolver dispatch) accepts:
- leading `//` (GCP resource-name format)
- one surrounding layer of `'...'` or `"..."`
- surrounding whitespace

Equivalent inputs:
```
__SECRET_REF__(secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest)
__SECRET_REF__(//secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest)
__SECRET_REF__('//secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest')
```

## AWS Flow

### Parameter fetch (`internal/infrastructure/source/aws.go`)

```
CONFIG_LOCATION = arn:aws:ssm:{region}:{account}:parameter/{path}
CONFIG_VERSION  = SIT-1
```

| Step | Action |
|---|---|
| Parse ARN | split into 6 fields → `{region, "/{path}"}` |
| Build name | `/{path}/versions/{version}` (leading slash auto-prepended) |
| Call | `ssm.GetParameter(Name, WithDecryption=true)` |
| Return | `*resp.Parameter.Value` |

`render` mode is rejected by the adapter (`ErrUnsupportedMode`); SSM has no template-render equivalent.

### Secret ref (`internal/infrastructure/secrets/aws.go`)

Two URI shapes:

| Format | Example |
|---|---|
| Legacy | `secretsmanager.amazonaws.com/{region}/{secret-id}` |
| Regional | `secretsmanager.{region}.amazonaws.com/projects/{account}/secrets/{secret-id}` |

Resolve path:

```
1. match prefix       → branch on legacy vs regional
2. extract region     → config.LoadDefaultConfig(WithRegion(region))
3. extract secret-id  → secretsmanager.GetSecretValue(SecretId={secret-id})
4. return SecretString (binary secrets → error)
```

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

## End-to-End Example (AWS)

Parameter URI (SSM):
```
arn:aws:ssm:ap-southeast-7:627443353872:parameter/crd-portal/frontend
```

Env:
```
CONFIG_LOCATION=arn:aws:ssm:ap-southeast-7:627443353872:parameter/crd-portal/frontend
CONFIG_VERSION=SIT-1
CONFIG_FETCH_MODE=get
```

Payload stored at `/crd-portal/frontend/versions/SIT-1` (JSON):
```json
{
  "APP_PORT": "8080",
  "DB_PASSWORD": "__SECRET_REF__(secretsmanager.ap-southeast-7.amazonaws.com/projects/627443353872/secrets/db-pass-1)"
}
```

Pipeline:
1. `LoadConfigUseCase` matches `isSSMLocation` → AWS source
2. `parseSSMArn` → `region="ap-southeast-7"`, `paramPath="/crd-portal/frontend"`
3. SSM reads `/crd-portal/frontend/versions/SIT-1` (SecureString decrypted)
4. `ParsePayload` → `[APP_PORT=8080, DB_PASSWORD=__SECRET_REF__(...)]`
5. `ResolveSecretsUseCase` regex matches `__SECRET_REF__(...)` per pair
6. `NormalizeRef` → strips nothing (already clean)
7. `awsResolver.Supports` → `true` (regional prefix match)
8. `awsResolver.Resolve`:
   - `region = ap-southeast-7`
   - `account = 627443353872`
   - `secret-id = db-pass-1`
   - `GetSecretValue(SecretId="db-pass-1")` in `ap-southeast-7`
9. Output pair: `DB_PASSWORD=<real-secret>`

Log line: `secret refs: 1 placeholder(s) resolved, 1 var(s) updated`

Final `.env`:
```
APP_PORT=8080
DB_PASSWORD=<real-secret>
```

## End-to-End Example (GCP)

Env:
```
CONFIG_LOCATION=projects/my-proj/locations/asia-southeast1/parameters/app-config
CONFIG_VERSION=dev-1
CONFIG_FETCH_MODE=render
```

Payload template (raw, unrendered):
```json
{
  "DB_HOST": "10.0.0.5",
  "DB_PASSWORD": "__SECRET_REF__(secretmanager.googleapis.com/projects/my-proj/secrets/db-pass/versions/latest)"
}
```

Pipeline:
1. `LoadConfigUseCase` matches catch-all → GCP source
2. `gcpSource.Fetch(mode=render)` → `RenderParameterVersion(name=".../versions/dev-1")` → resolved JSON
3. `ParsePayload` → env pairs
4. `gcpResolver.Supports` → `true`
5. `gcpResolver.Resolve` → `AccessSecretVersion(name="projects/my-proj/secrets/db-pass/versions/latest")`

## Adding a New Provider

The refactor's central guarantee: adding a cloud touches only the
infrastructure layer.

**Parameter source** (`internal/infrastructure/source/`):
```go
// azure.go
func NewAzureSource(ctx context.Context, mode domain.FetchMode) (domain.ConfigSource, error) {
    return &azureSource{ /* ... */ }, nil
}

func (a *azureSource) Kind() domain.ProviderKind { return domain.ProviderKind("azure") }
func (a *azureSource) Fetch(ctx context.Context, ref domain.Reference, mode domain.FetchMode) (domain.Payload, error) {
    // call Azure App Configuration SDK
}
```
Then append one entry to `Sources` in `registry.go`.

**Secret resolver** (`internal/infrastructure/secrets/`):
```go
// azure.go
type azureResolver struct{}
func NewAzureResolver() domain.SecretResolver { return azureResolver{} }
func (azureResolver) Supports(ref string) bool { return strings.HasPrefix(ref, "vault.azure.net/") }
func (azureResolver) Resolve(ctx context.Context, ref string) (string, error) {
    // call Azure Key Vault SDK
}
```
Then append one entry to `Resolvers` in `registry.go`.

Domain, application, and `cmd/config-extractor/main.go` are unchanged.

## CLI Flags

| Flag | Default | Effect |
|---|---|---|
| `--mode` | `env` | `env`: write `.env`. `exec`: inject into child process. |
| `--out` | `.env` | Output path (env mode only). |
| `--install` | (none) | Copy binary to dir and exit. No cloud calls. |
| `--strict-fetch` | `true` | Non-zero exit if parameter fetch fails |
| `--strict-secret-refs` | `true` | Non-zero exit if any secret ref fails |

## Env Vars

| Var | Required | Notes |
|---|---|---|
| `CONFIG_LOCATION` | yes | Resource path (ARN for AWS, GCP resource name otherwise) |
| `CONFIG_VERSION` | yes | Version label, e.g. `dev-1`, `SIT-1` |
| `CONFIG_FETCH_MODE` | no | `get` (default) / `render` (GCP only) |
| `AWS_REGION` | AWS only | Required when `CONFIG_LOCATION` is a bare parameter path |

## Known Gaps

| Issue | Location | Behavior |
|---|---|---|
| Param fetch error | `LoadConfigUseCase` (non-strict) | WARNs, continues with empty payload |
| Secret-ref error | `ResolveSecretsUseCase` (non-strict) | WARNs, keeps `__SECRET_REF__(...)` literal in `.env` — token leak risk |
| AWS `render` mode | `aws.go` adapter | Hard error via `ErrUnsupportedMode` |
| AWS Secrets binary | `aws.go` resolver | Errors; only `SecretString` returned |