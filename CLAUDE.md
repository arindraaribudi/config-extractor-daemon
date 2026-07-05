# config-extractor-daemon

Init-container binary: fetches a parameter store payload (GCP Parameter Manager or AWS SSM), parses it, resolves `__SECRET_REF__(uri)` placeholders via registered cloud providers, then writes `.env` or injects vars into a child process.

Usage + samples → [README.md](./README.md).
Architecture / provider wiring / end-to-end flows → [CLOUD_SUPPORT.md](./CLOUD_SUPPORT.md).

## Key files
- `main.go` — entry point, `parseEnvString`, `flattenToEnv`, `parsePayload`, `writeEnvFile`, `runWithEnv`, `resolveFetcher`, `detectProvider`, `installBinary`
- `main_test.go` — unit tests for core functions (no cloud credentials required)
- `secret_ref.go` — `SecretProvider` interface, `resolveSecretRefs`, `replaceAllRefs`, `normalizeRef`
- `secret_ref_gcp.go` — `GCPSecretProvider` for `secretmanager.googleapis.com/...` refs
- `secret_ref_aws.go` — `AWSSecretProvider` for AWS Secrets Manager refs (legacy + regional)
- `config_fetcher_aws.go` — AWS SSM Parameter Store fetcher (`fetchSSMParameterGet`, `parseSSMArn`)
- `secret_ref_test.go` — unit tests for ref resolution (mock provider, all edge cases)
- `cmd/env-printer/main.go` — helper binary that prints child env (verify exec mode)
- `Dockerfile` — multi-stage scratch image; embedded CA bundle for scratch/distroless
- `Makefile` — multi-platform build/push via `docker buildx`; accepts `IMAGE=` parameter
- `CLOUD_SUPPORT.md` — architecture reference (provider detection, fetcher wiring, flows)

## Env vars

| Var | Required | Default | Notes |
|---|---|---|---|
| `CONFIG_LOCATION` | yes | — | GCP: `projects/{p}/locations/{l}/parameters/{id}`. AWS: `arn:aws:ssm:{region}:{account}:parameter/{path}` or bare `/{path}` (region from `AWS_REGION`). |
| `CONFIG_VERSION` | yes | — | Version label, e.g. `dev-1`, `SIT-1`. |
| `CONFIG_FETCH_MODE` | no | `get` | `get` (raw payload) or `render` (GCP only — resolves template vars). AWS rejects `render`. |
| `AWS_REGION` | AWS only | — | Required when `CONFIG_LOCATION` is a bare parameter path; ignored if the ARN encodes the region. |

## CLI flags

| Flag | Default | Effect |
|---|---|---|
| `--mode` | `env` | `env`: write `.env`. `exec`: inject into child process. |
| `--out` | `.env` | Output path (env mode only). |
| `--install` | — | Copy binary to dir, exit. No cloud calls. |
| `--strict-fetch` | `true` | Exit non-zero if parameter fetch fails. `false` → warn + continue with empty payload. |
| `--strict-secret-refs` | `true` | Exit non-zero if any `__SECRET_REF__()` fails to resolve. `false` → warn + leave placeholder literal. |

## Secret refs

Values may embed `__SECRET_REF__(uri)`. Resolved by first matching `SecretProvider.Supports`. Normalisation (whitespace / quotes / leading `//`) runs before dispatch.

Supported URI shapes:
- GCP Secret Manager: `secretmanager.googleapis.com/projects/{p}/secrets/{s}/versions/{v}`
- AWS Secrets Manager (legacy): `secretsmanager.amazonaws.com/{region}/{secret-id}`
- AWS Secrets Manager (regional): `secretsmanager.{region}.amazonaws.com/projects/{account}/secrets/{secret-id}`

## Provider detection

`detectProvider(location)` (`main.go:175`):
- `arn:aws:ssm:` prefix or bare `/{path}` → `aws`
- anything else → `gcp`

## Commands

```bash
go test ./... -v                                                   # run tests
make build IMAGE=gcr.io/project/config-extractor-daemon:v1    # build (cache)
make push  IMAGE=gcr.io/project/config-extractor-daemon:v1    # push to registry
make load  IMAGE=config-extractor-daemon:v1                    # load into local Docker
go run . --mode=exec -- ./cmd/env-printer/env-printer              # verify exec mode
go run . --install /usr/local/bin                                 # install binary to a directory
```