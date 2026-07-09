IMAGE        ?= config-extractor-daemon:latest
PLATFORMS    := linux/amd64,linux/arm64
BUILDER      := multiplatform-builder
BIN          := config-extractor-daemon
ENTRY        := ./cmd/config-extractor

.PHONY: build push build-push load test test-race lint vet fmt fmt-check layer-check certs run run-exec install-bin clean setup-builder tidy

## Build multi-platform image (cache only — no push, no local load)
build: setup-builder
	docker buildx build \
		--builder $(BUILDER) \
		--platform $(PLATFORMS) \
		--tag $(IMAGE) \
		.

## Push multi-platform image to registry
push: setup-builder
	docker buildx build \
		--builder $(BUILDER) \
		--platform $(PLATFORMS) \
		--tag $(IMAGE) \
		--push \
		.

## Build and push in one step (most common CI usage)
build-push: setup-builder
	docker buildx build \
		--builder $(BUILDER) \
		--platform $(PLATFORMS) \
		--tag $(IMAGE) \
		--push \
		.

## Load single-platform image into local Docker (for local testing)
load: setup-builder
	docker buildx build \
		--builder $(BUILDER) \
		--platform linux/$(shell go env GOARCH) \
		--tag $(IMAGE) \
		--load \
		.

## Run Go unit tests (auto-generates CA bundle if missing)
test: certs
	go test ./... -v

## Run Go unit tests with -race detector (auto-generates CA bundle if missing)
test-race: certs
	go test ./... -race

## Run Go unit tests with coverage. Writes:
##   coverage.out   — raw go-test profile (consumable by `go tool cover`)
##   coverage.html  — browsable HTML report (one page per package)
##   coverage.txt   — `go tool cover -func` summary (paste-friendly)
## Total statement % lands at the end of coverage.txt for README badges.
test-coverage: certs
	go test ./... -coverprofile=coverage.out -covermode=atomic -count=1
	go tool cover -func=coverage.out > coverage.txt
	go tool cover -html=coverage.out -o coverage.html
	@echo ""
	@grep "^total" coverage.txt

## Lint: go vet + gofmt check + layer-import guard.
## Fails CI if any domain/application file leaks an SDK import.
lint: vet fmt-check layer-check

## Generate the CA bundle that //go:embed in internal/infrastructure/tls
## pulls in. Linux (Debian/Ubuntu/Alpine) + macOS sources handled.
## Required once after clone before `go test` / `go build`.
certs:
	@mkdir -p internal/infrastructure/tls/certs
	@if [ -f /etc/ssl/certs/ca-certificates.crt ]; then \
		cp /etc/ssl/certs/ca-certificates.crt internal/infrastructure/tls/certs/ ; \
	elif [ -f /etc/ssl/cert.pem ]; then \
		cp /etc/ssl/cert.pem internal/infrastructure/tls/certs/ca-certificates.crt ; \
	else \
		echo "no system CA bundle found; install ca-certificates (apt/apk/brew)"; exit 1 ; \
	fi
	@echo "certs: installed to internal/infrastructure/tls/certs/"

## Run go vet (catches suspicious constructs). Depends on `certs` because
## //go:embed in internal/infrastructure/tls fails without the bundle.
vet: certs
	go vet ./...

## Auto-format every Go file in-place
fmt:
	gofmt -w .

## Fail if any .go file is mis-formatted (CI guard)
fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt: files need formatting:"; echo "$$out"; exit 1; fi

## Layer-import guard: domain + application must not import any SDK.
## If this ever fails, an adapter is leaking into pure logic.
layer-check:
	@bad=$$(grep -rEn 'aws-sdk-go|cloud.google.com|google.golang.org/api|google.golang.org/grpc' \
		internal/domain internal/application 2>/dev/null); \
	if [ -n "$$bad" ]; then echo "layer violation (SDK in domain/application):"; echo "$$bad"; exit 1; fi; \
	echo "layer-check: clean (no SDK imports in domain or application)"

## Run locally in env mode — writes $(OUT) (defaults to .env).
## CONFIG_LOCATION and CONFIG_VERSION must be exported in the calling shell
## (e.g. `CONFIG_LOCATION=... CONFIG_VERSION=... make run`).
OUT ?= .env
run:
	@if [ -z "$$CONFIG_LOCATION" ] || [ -z "$$CONFIG_VERSION" ]; then \
		echo "make run: CONFIG_LOCATION and CONFIG_VERSION are required"; \
		echo "  example: CONFIG_LOCATION=arn:aws:ssm:ap-southeast-1:111:parameter/dev CONFIG_VERSION=dev-1 make run"; \
		exit 1; \
	fi
	go run $(ENTRY) --mode=env --out=$(OUT)

## Run locally in exec mode — injects env into a child process
CMD ?= echo "set CMD to your command"
run-exec:
	go run $(ENTRY) --mode=exec -- $(CMD)

## Copy binary to a directory (no GCP credentials needed)
DEST ?= /usr/local/bin
install-bin:
	go run $(ENTRY) --install $(DEST)

## Create the buildx builder if it does not exist
setup-builder:
	@docker buildx inspect $(BUILDER) > /dev/null 2>&1 || \
		docker buildx create --name $(BUILDER) --driver docker-container --bootstrap

## Remove the buildx builder
clean:
	docker buildx rm $(BUILDER) 2>/dev/null || true