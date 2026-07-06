# config-extractor-daemon

Init-container / sidecar binary that pulls a parameter payload from **GCP Parameter Manager** or **AWS SSM Parameter Store**, parses it (JSON / YAML / `.env`), resolves `__SECRET_REF__(uri)` placeholders against a registered cloud secrets backend, then either writes a `.env` file or injects vars into a child process.

[![coverage](https://img.shields.io/badge/coverage-97%25-brightgreen)](./README.md#coverage)

---

## Quick start

### GCP

```bash
export CONFIG_LOCATION="projects/my-proj/locations/global/parameters/app-config"
export CONFIG_VERSION="dev-1"
gcloud auth application-default login

go run ./cmd/config-extractor                              # writes ./env
go run ./cmd/config-extractor --mode=exec -- /app/server   # injects into child process
```

### AWS

```bash
export CONFIG_LOCATION="arn:aws:ssm:ap-southeast-7:111122223333:parameter/crd-portal/frontend"
export CONFIG_VERSION="SIT-1"
# AWS_REGION also respected; default credential chain (IAM role, env, ~/.aws)

go run ./cmd/config-extractor                              # writes ./env
```

### Tencent Cloud

```bash
export CONFIG_LOCATION="https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig"
export CONFIG_VERSION="ENV-1"
# TENCENTCLOUD_SECRETID + TENCENTCLOUD_SECRETKEY env vars
# OR a CAM role attached to the CVM/TKE instance

go run ./cmd/config-extractor                              # writes ./env
```

### Install-only (no cloud credentials needed)

```bash
go run ./cmd/config-extractor --install /usr/local/bin      # copy binary into dir, exit
```

---

## How it works

```
CONFIG_LOCATION + CONFIG_VERSION
        │
        ▼
LoadConfigUseCase
   iterates internal/infrastructure/source/registry.Sources
   picks first entry whose Match(location) returns true
        │
        ▼
domain.ConfigSource.Fetch(ref, mode)  →  raw payload
        │
        ▼
domain.ParsePayload(payload)          →  []EnvPair  (KEY=VALUE)
        │
        ▼
ResolveSecretsUseCase
   iterates internal/infrastructure/secrets/registry.Resolvers
   picks first entry whose Supports(ref) returns true
        │
        ▼
--mode=env   → WriteEnvUseCase → --out file (0600)
--mode=exec  → ExecChildUseCase → child process with merged env
```

Detection / format / provider dispatch is automatic — only `CONFIG_LOCATION` shape changes between clouds. See `CLOUD_SUPPORT.md` for the full pipeline, provider matrix, and instructions for adding a new cloud.

---

## Environment variables

| Var | Required | Default | Notes |
|---|---|---|---|
| `CONFIG_LOCATION` | yes | — | GCP: `projects/{p}/locations/{l}/parameters/{id}`. AWS: `arn:aws:ssm:{region}:{account}:parameter/{path}` or bare `/{path}` (region from `AWS_REGION`). Tencent: `https://{bucket}-{appid}.cos.{region}.myqcloud.com/{key-prefix}` (region + bucket derived from URL host; `CONFIG_VERSION` appended as final key segment). |
| `CONFIG_VERSION` | yes | — | Version label, e.g. `dev-1`, `SIT-1`, `prod-2025-11-12`. |
| `CONFIG_FETCH_MODE` | no | `get` | `get` (raw payload) or `render` (GCP only — resolves template vars). AWS and Tencent reject `render`. |
| `AWS_REGION` | AWS only | — | Required when `CONFIG_LOCATION` is a bare parameter path; ignored if the ARN already encodes the region. |
| `TENCENTCLOUD_SECRETID` | no | — | Tencent Cloud API AK. Optional when running on CVM/TKE with a CAM role attached. |
| `TENCENTCLOUD_SECRETKEY` | no | — | Tencent Cloud API SK. Same precedence rule. |

---

## CLI flags

| Flag | Default | Effect |
|---|---|---|
| `--mode` | `env` | `env`: write `.env` file. `exec`: inject into child process. |
| `--out` | `.env` | Output path for the `.env` file (env mode only). |
| `--install` | — | Copy binary to dir, exit. No cloud calls. |
| `--strict-fetch` | `true` | Exit non-zero if parameter fetch fails. Set `false` to warn + continue. |
| `--strict-secret-refs` | `true` | Exit non-zero if any `__SECRET_REF__()` fails to resolve. Set `false` to warn + leave the placeholder literal in the output. |

---

## Modes

### `env` (default) — write `.env` file

```bash
# default location
go run .

# custom output path
go run . --mode=env --out=/etc/app/.env

# GCP — resolve template vars in the payload
CONFIG_FETCH_MODE=render go run .
```

### `exec` — inject into child process

```bash
# minimal
go run . --mode=exec -- /app/server --port 8080

# verify with the bundled helper
go build -o ./env-printer ./cmd/env-printer
go run . --mode=exec -- ./env-printer
```

The child process inherits stdin/stdout/stderr and the host environment, plus the injected `KEY=VALUE` pairs. Non-zero child exits are propagated.

---

## Payload format

Auto-detected in order: **JSON → YAML → plain `.env`**.

```json
{
  "DB": { "HOST": "localhost", "PORT": "5432" }
}
```

becomes:

```
DB_HOST=localhost
DB_PORT=5432
```

Nested keys are joined with `_`. YAML mappings and plain `KEY=VALUE` lines work the same way.

---

## Secret refs (`__SECRET_REF__()`)

Any value in the payload may embed `__SECRET_REF__(uri)`. The binary resolves each placeholder by fetching from the registered provider that matches the URI prefix.

```
DSN=postgres://__SECRET_REF__(secretsmanager.amazonaws.com/us-east-1/db-user):__SECRET_REF__(secretsmanager.amazonaws.com/us-east-1/db-pass)@localhost/mydb
```

After resolution: `secret refs: 2 placeholder(s) resolved, 1 var(s) updated`.

### Supported providers

| Provider | URI |
|---|---|
| GCP Secret Manager | `secretmanager.googleapis.com/projects/{p}/secrets/{s}/versions/{v}` |
| AWS Secrets Manager (legacy) | `secretsmanager.amazonaws.com/{region}/{secret-id}` |
| AWS Secrets Manager (regional) | `secretsmanager.{region}.amazonaws.com/projects/{account}/secrets/{secret-id}` |
| Tencent Secrets Manager | `secretsmanager.tencentcloudapi.com/{region}/{secret-name}` |

### URI normalisation

Before any provider sees the ref, `normalizeRef` cleans the raw token:

1. Trim surrounding whitespace
2. Strip one layer of `'...'` or `"..."`
3. Strip a leading `//` (GCP resource-name format)

These are all equivalent:

```
__SECRET_REF__(secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest)
__SECRET_REF__(//secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest)
__SECRET_REF__('//secretmanager.googleapis.com/projects/123/secrets/foo/versions/latest')
```

### IAM

| Backend | Required permission |
|---|---|
| GCP Secret Manager | `secretmanager.versions.access` |
| AWS Secrets Manager | `secretsmanager:GetSecretValue` on the secret |
| Tencent Secrets Manager | `cam:QueryCAMRole` + the policy attached to your CAM role granting `GetSecretValue` on the secret |

---

## Sample — AWS end-to-end

Parameter at `/crd-portal/frontend/versions/SIT-1` (SecureString, decrypted automatically):

```json
{
  "APP_PORT": "8080",
  "DB_PASSWORD": "__SECRET_REF__(secretsmanager.ap-southeast-7.amazonaws.com/projects/111122223333/secrets/db-pass-1)"
}
```

Run:

```bash
export CONFIG_LOCATION="arn:aws:ssm:ap-southeast-7:111122223333:parameter/crd-portal/frontend"
export CONFIG_VERSION="SIT-1"
go run . --mode=env --out=/tmp/.env
```

Resulting `/tmp/.env`:

```
APP_PORT=8080
DB_PASSWORD=<real-secret>
```

`render` mode is rejected for AWS — SSM has no template-render equivalent.

---

## Sample — GCP end-to-end

Parameter at `projects/my-proj/locations/global/parameters/app-config/versions/dev-1`:

```json
{
  "DB_HOST": "10.0.0.5",
  "DB_PASSWORD": "__SECRET_REF__(secretmanager.googleapis.com/projects/my-proj/secrets/db-pass/versions/latest)"
}
```

Run:

```bash
export CONFIG_LOCATION="projects/my-proj/locations/global/parameters/app-config"
export CONFIG_VERSION="dev-1"
gcloud auth application-default login
go run . --mode=env --out=/tmp/.env
```

To resolve template variables stored in the payload (e.g. `__REF__(other-param)`) use:

```bash
CONFIG_FETCH_MODE=render go run .
```

---

## Sample — Tencent end-to-end

Object at `https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig/ENV-1` (COS object key, JSON payload):

```json
{
  "APP_PORT": "8080",
  "DB_PASSWORD": "__SECRET_REF__(secretsmanager.tencentcloudapi.com/ap-bangkok/db-pass-1)"
}
```

Run:

```bash
export CONFIG_LOCATION="https://cds-oms-sit-1409486316.cos.ap-bangkok.myqcloud.com/projects/my-proj/parameters/appconfig"
export CONFIG_VERSION="ENV-1"
export TENCENTCLOUD_SECRETID="AKIDxxxxxxxxxxxxxxxxxxxx"
export TENCENTCLOUD_SECRETKEY="xxxxxxxxxxxxxxxxxxxxxxxx"
go run . --mode=env --out=/tmp/.env
```

Resulting `/tmp/.env`:

```
APP_PORT=8080
DB_PASSWORD=<real-secret>
```

`render` mode is rejected for Tencent — COS stores raw bytes, no template rendering.

---

## Kubernetes init-container pattern

Copy the binary onto a shared volume so the main container can call it:

```yaml
initContainers:
  - name: install-config-extractor
    image: gcr.io/my-project/config-extractor-daemon:v1
    args: ["--install", "/shared/bin"]
    volumeMounts:
      - name: shared-bin
        mountPath: /shared/bin

containers:
  - name: app
    image: my-app:v1
    command: ["/shared/bin/config-extractor-daemon", "--mode=exec", "--", "/app/server"]
    env:
      - { name: CONFIG_LOCATION, value: "projects/my-proj/locations/global/parameters/app-config" }
      - { name: CONFIG_VERSION,  value: "prod-1" }
    volumeMounts:
      - name: shared-bin
        mountPath: /shared/bin
```

For `--mode=env` workloads, mount the same volume as an output dir and run without `--mode`:

```yaml
containers:
  - name: app
    image: my-app:v1
    command: ["/shared/bin/config-extractor-daemon", "--out", "/shared/env/.env"]
    volumeMounts:
      - { name: shared-bin,  mountPath: /shared/bin }
      - { name: shared-env,  mountPath: /shared/env }
```

The container needs `parametermanager.parameterVersions.get` (GCP) or `ssm:GetParameter` + `kms:Decrypt` for SecureString (AWS), plus the secret-ref permissions.

---

## Build & ship

```bash
# local single-arch (loads into Docker)
make load IMAGE=config-extractor-daemon:v1

# multi-arch build (cache only)
make build IMAGE=gcr.io/my-project/config-extractor-daemon:v1

# multi-arch build + push
make push IMAGE=gcr.io/my-project/config-extractor-daemon:v1

# tests
make test           # = go test ./... -v
```

The Dockerfile is a multi-stage scratch build. The CA bundle is embedded at build time so the binary verifies TLS in scratch/distroless images.

---

## Adding a new secrets backend

1. Create `secret_ref_<cloud>.go`:

   ```go
   type XSecretProvider struct{}

   func (XSecretProvider) Supports(ref string) bool {
       return strings.HasPrefix(ref, "your-prefix/")
   }

   func (XSecretProvider) Resolve(ctx context.Context, ref string) (string, error) {
       // fetch + return plaintext
   }
   ```

2. Append to the `providers` slice in `main.go`:

   ```go
   providers := []SecretProvider{GCPSecretProvider{}, AWSSecretProvider{}, XSecretProvider{}}
   ```

3. Add unit tests using the existing `mockProvider` pattern in `secret_ref_test.go`.

The first provider whose `Supports(ref)` returns `true` wins.

---

## Coverage

The `ci` workflow runs `make test-coverage` on every push and PR, and uploads the result as a workflow artifact named **`coverage`** (retained 30 days).

Artifact contents:
- `coverage.out` — raw `go test -coverprofile` profile. Open in your editor or feed back into Go: `go tool cover -html=coverage.out`.
- `coverage.html` — single-page HTML report. Open it in a browser to drill into which lines are uncovered, per package.
- `coverage.txt` — `go tool cover -func` summary. The last line (`total: (statements)  XX.X%`) is the headline number.

To refresh the badge locally and re-verify the number:

```bash
make test-coverage
# ... see coverage.txt for the new statement %
```

The badge at the top of this README reflects the latest `main` build; bump the number in the badge URL when it changes.

---

## Project layout

```
.
├── main.go                     # Entry point + parsePayload, writeEnvFile, runWithEnv, installBinary, resolveFetcher, detectProvider
├── main_test.go                # Unit tests (no cloud credentials required)
├── secret_ref.go               # SecretProvider interface + resolveSecretRefs + normalizeRef
├── secret_ref_gcp.go           # GCPSecretProvider
├── secret_ref_aws.go           # AWSSecretProvider (Secrets Manager — for refs)
├── config_fetcher_aws.go       # AWS SSM Parameter Store fetcher (for CONFIG_LOCATION)
├── secret_ref_test.go          # Tests for ref resolution + mock provider
├── Dockerfile                  # Multi-stage scratch image, embedded CA bundle
├── Makefile                    # buildx multi-platform targets
├── go.mod / go.sum
└── cmd/
    └── env-printer/
        └── main.go             # Helper: prints injected env (verify exec mode)
```

Architecture reference for provider detection, fetcher wiring, and end-to-end flows: see [CLOUD_SUPPORT.md](./CLOUD_SUPPORT.md).