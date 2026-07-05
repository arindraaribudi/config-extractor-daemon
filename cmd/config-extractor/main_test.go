package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arindraaribudi/config-extractor-daemon/internal/application"
	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/source"
)

// --- fakes ---------------------------------------------------------------

// fakeSource is a ConfigSource that returns a fixed payload or error.
type fakeSource struct {
	kind    domain.ProviderKind
	payload domain.Payload
	err     error
}

func (f fakeSource) Kind() domain.ProviderKind { return f.kind }
func (f fakeSource) Fetch(_ context.Context, _ domain.Reference, _ domain.FetchMode) (domain.Payload, error) {
	return f.payload, f.err
}

// fakeResolver mirrors the pattern in internal/application tests.
type fakeResolver struct {
	supports func(string) bool
	resolve  func(context.Context, string) (string, error)
}

func (f fakeResolver) Supports(ref string) bool { return f.supports(ref) }
func (f fakeResolver) Resolve(ctx context.Context, ref string) (string, error) {
	return f.resolve(ctx, ref)
}

// staticEnv returns an envGetter backed by a map.
func staticEnv(m map[string]string) envGetter {
	return func(k string) string { return m[k] }
}

// successRegistry returns a registry with one entry that fetches payload
// successfully for any location.
func successRegistry(payload domain.Payload) func(context.Context, domain.FetchMode) []application.SourceEntry {
	return func(_ context.Context, _ domain.FetchMode) []application.SourceEntry {
		return []application.SourceEntry{{
			Match:  func(string) bool { return true },
			Source: fakeSource{kind: domain.ProviderGCP, payload: payload},
		}}
	}
}

// failingRegistry returns a registry whose only entry errors on Fetch.
func failingRegistry(err error) func(context.Context, domain.FetchMode) []application.SourceEntry {
	return func(_ context.Context, _ domain.FetchMode) []application.SourceEntry {
		return []application.SourceEntry{{
			Match:  func(string) bool { return true },
			Source: fakeSource{kind: domain.ProviderGCP, err: err},
		}}
	}
}

// emptyResolvers returns a no-op resolver list (no placeholder replacement).
func emptyResolvers() []domain.SecretResolver { return nil }

// identityResolvers supports all refs, returns a fixed value (rarely needed).
func identityResolvers(value string) []domain.SecretResolver {
	return []domain.SecretResolver{fakeResolver{
		supports: func(string) bool { return true },
		resolve:  func(context.Context, string) (string, error) { return value, nil },
	}}
}

// baseDeps returns a deps struct pre-populated with stubs; tests override
// individual fields.
func baseDeps() cliDeps {
	return cliDeps{
		Getenv:        staticEnv(nil),
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
		BuildRegistry: successRegistry("FOO=bar"),
		Resolvers:     emptyResolvers(),
	}
}

func mustEnv(t *testing.T, kv map[string]string) envGetter {
	t.Helper()
	return staticEnv(kv)
}

// --- existing tests (kept verbatim, lightly adapted) --------------------

func TestBuildSourceRegistry_RealRegistry(t *testing.T) {
	// With no cloud creds, factory errors are expected and entries get
	// skipped. The point is to exercise the success-or-skip branch with
	// the live registry so the helper is covered end-to-end.
	out := defaultBuildRegistry(context.Background(), domain.FetchGet)
	for _, e := range out {
		if e.Match == nil {
			t.Error("entry.Match must not be nil")
		}
		if e.Source == nil {
			t.Error("entry.Source must not be nil when kept")
		}
	}
}

func TestBuildSourceRegistry_HandlesAllFetchModes(t *testing.T) {
	for _, mode := range []domain.FetchMode{domain.FetchGet, domain.FetchRender} {
		_ = defaultBuildRegistry(context.Background(), mode)
	}
}

func TestSourceRegistryHasExpectedEntries(t *testing.T) {
	if len(source.Sources) < 2 {
		t.Fatalf("expected at least 2 sources registered, got %d", len(source.Sources))
	}
}

// --- run() tests --------------------------------------------------------

func TestRun_Help(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	code := run([]string{"-h"}, d)
	if code != 0 {
		t.Errorf("help: code = %d, want 0", code)
	}
}

func TestRun_HelpLong(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	if code := run([]string{"--help"}, d); code != 0 {
		t.Errorf("--help: code = %d, want 0", code)
	}
}

func TestRun_Install(t *testing.T) {
	dir := t.TempDir()
	d := baseDeps()
	d.BuildRegistry = successRegistry("ignored") // shouldn't run
	// No env vars needed; install short-circuits before any are read.
	d.Getenv = mustEnv(t, nil)
	code := run([]string{"--install", dir}, d)
	if code != 0 {
		t.Fatalf("install: code = %d, want 0; stderr=%s", code, d.Stderr.(*bytes.Buffer).String())
	}
	// File should exist (copy of the running test binary).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("install dir is empty; expected the binary")
	}
	stdout := d.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(stdout, "installed to "+dir) {
		t.Errorf("stdout missing confirmation: %q", stdout)
	}
}

func TestRun_InstallFailureReturnsOne(t *testing.T) {
	// /dev/full exists on Linux/macOS; writing to it should fail.
	if runtime.GOOS == "windows" {
		t.Skip("unix-only test")
	}
	d := baseDeps()
	d.Getenv = mustEnv(t, nil)
	// Use a directory we can't actually write to as DestDir; pass through
	// the symlink dance by giving a path that MkdirAll will reject.
	// Simplest reliable failure: a file path that already exists as a
	// regular file → os.MkdirAll on the same path errors.
	conflict := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(conflict, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	code := run([]string{"--install", conflict}, d)
	if code == 0 {
		t.Errorf("install into blocking file should fail; code=%d", code)
	}
}

func TestRun_MissingConfigLocation(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_VERSION": "v1",
	})
	if code := run([]string{}, d); code != 1 {
		t.Errorf("missing CONFIG_LOCATION: code = %d, want 1", code)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "CONFIG_LOCATION is required") {
		t.Errorf("expected CONFIG_LOCATION error in stderr")
	}
}

func TestRun_MissingConfigVersion(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
	})
	if code := run([]string{}, d); code != 1 {
		t.Errorf("missing CONFIG_VERSION: code = %d, want 1", code)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "CONFIG_VERSION is required") {
		t.Errorf("expected CONFIG_VERSION error in stderr")
	}
}

func TestRun_EnvMode_Success(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env")
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar\nBAZ=qux")

	code := run([]string{"--out", out}, d)
	if code != 0 {
		t.Fatalf("env success: code = %d, want 0; stderr=%s", code, d.Stderr.(*bytes.Buffer).String())
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "FOO=bar") || !strings.Contains(got, "BAZ=qux") {
		t.Errorf("file missing pairs: %q", got)
	}
	stdout := d.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(stdout, "wrote 2 var(s)") {
		t.Errorf("stdout missing summary: %q", stdout)
	}
}

func TestRun_EnvMode_Empty(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env")
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("") // empty payload

	code := run([]string{"--out", out}, d)
	if code != 0 {
		t.Fatalf("env empty: code = %d, want 0", code)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if string(body) != "" {
		t.Errorf("empty payload should produce empty file, got %q", string(body))
	}
	stdout := d.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(stdout, "wrote empty file") {
		t.Errorf("stdout missing empty-file message: %q", stdout)
	}
}

func TestRun_EnvMode_StrictFetchError(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = failingRegistry(errors.New("boom"))
	// strict-fetch defaults to true; explicit for clarity:
	code := run([]string{"--strict-fetch=true"}, d)
	if code != 1 {
		t.Errorf("strict fetch error: code = %d, want 1", code)
	}
}

func TestRun_EnvMode_NonStrictFetchError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env")
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = failingRegistry(errors.New("boom"))

	code := run([]string{"--strict-fetch=false", "--out", out}, d)
	if code != 0 {
		t.Errorf("non-strict fetch error: code = %d, want 0; stderr=%s", code, d.Stderr.(*bytes.Buffer).String())
	}
	// Should still write an empty .env (non-strict continues with empty payload).
	if _, err := os.Stat(out); err != nil {
		t.Errorf("expected .env to be written in non-strict mode: %v", err)
	}
}

func TestRun_ExecMode_NoArgs(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	code := run([]string{"--mode=exec"}, d)
	if code != 1 {
		t.Errorf("exec no-args: code = %d, want 1", code)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "--mode=exec requires a command") {
		t.Errorf("expected exec requires-a-command error in stderr")
	}
}

func TestRun_ExecMode_Success(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	// `true` is a portable POSIX no-op that exits 0.
	code := run([]string{"--mode=exec", "--", "true"}, d)
	if code != 0 {
		t.Errorf("exec success: code = %d, want 0; stderr=%s", code, d.Stderr.(*bytes.Buffer).String())
	}
}

func TestRun_ExecMode_PropagatesChildExitCode(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	// `sh -c 'exit 7'` should produce exit code 7.
	code := run([]string{"--mode=exec", "--", "sh", "-c", "exit 7"}, d)
	if code != 7 {
		t.Errorf("exec exit propagation: code = %d, want 7", code)
	}
}

func TestRun_ExecMode_EmptyPayloadStillRuns(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("") // empty payload
	code := run([]string{"--mode=exec", "--", "true"}, d)
	if code != 0 {
		t.Errorf("exec empty payload: code = %d, want 0", code)
	}
}

func TestRun_UnknownMode(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	code := run([]string{"--mode=foo"}, d)
	if code != 1 {
		t.Errorf("unknown mode: code = %d, want 1", code)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), `unknown mode "foo"`) {
		t.Errorf("expected unknown mode error in stderr")
	}
}

func TestRun_BadFlag(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, nil)
	code := run([]string{"--unknown-flag"}, d)
	if code != 2 {
		t.Errorf("bad flag: code = %d, want 2", code)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "flag provided but not defined") {
		t.Errorf("expected flag error in stderr")
	}
}

func TestRun_FetchModeDefaultsToGet(t *testing.T) {
	// No CONFIG_FETCH_MODE env → code should still pass through registry
	// with default FetchGet. Verifies the defaulting branch.
	dir := t.TempDir()
	out := filepath.Join(dir, ".env")
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	if code := run([]string{"--out", out}, d); code != 0 {
		t.Errorf("default fetch mode: code = %d, want 0", code)
	}
}

func TestRun_RegistryBuildFailure_Strict(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = failingRegistry(errors.New("auth failed"))
	code := run([]string{"--strict-fetch=true"}, d)
	if code != 1 {
		t.Errorf("strict registry failure: code = %d, want 1", code)
	}
}

func TestRun_RegistryBuildFailure_NonStrict(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env")
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = failingRegistry(errors.New("auth failed"))
	code := run([]string{"--strict-fetch=false", "--out", out}, d)
	if code != 0 {
		t.Errorf("non-strict registry failure: code = %d, want 0", code)
	}
}

func TestRun_NoMatchingSource(t *testing.T) {
	// Registry has an entry whose Match never returns true → use case
	// returns ErrNoSource, which is treated as a fatal error (strict-mode
	// rule, but actually ErrNoSource is unconditional in the use case).
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = func(_ context.Context, _ domain.FetchMode) []application.SourceEntry {
		return []application.SourceEntry{{
			Match:  func(string) bool { return false },
			Source: fakeSource{kind: domain.ProviderGCP, payload: "FOO=bar"},
		}}
	}
	code := run([]string{}, d)
	if code != 1 {
		t.Errorf("no matching source: code = %d, want 1", code)
	}
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "no config source matches location") {
		t.Errorf("expected ErrNoSource message in stderr")
	}
}

func TestRun_ResolverStrictError(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=__SECRET_REF__(s)")
	d.Resolvers = []domain.SecretResolver{fakeResolver{
		supports: func(string) bool { return true },
		resolve:  func(context.Context, string) (string, error) { return "", errors.New("nope") },
	}}
	code := run([]string{"--strict-secret-refs"}, d)
	if code != 1 {
		t.Errorf("strict secret ref error: code = %d, want 1", code)
	}
}

func TestRun_ResolverNonStrictError(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".env")
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=__SECRET_REF__(s)")
	d.Resolvers = []domain.SecretResolver{fakeResolver{
		supports: func(string) bool { return true },
		resolve:  func(context.Context, string) (string, error) { return "", errors.New("nope") },
	}}
	code := run([]string{"--strict-secret-refs=false", "--out", out}, d)
	if code != 0 {
		t.Errorf("non-strict secret ref error: code = %d, want 0; stderr=%s", code, d.Stderr.(*bytes.Buffer).String())
	}
}

func TestRun_LoggerGoesToInjectedStderr(t *testing.T) {
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = failingRegistry(errors.New("boom"))
	_ = run([]string{}, d)
	if !strings.Contains(d.Stderr.(*bytes.Buffer).String(), "platform=") {
		t.Errorf("expected platform log line in injected stderr")
	}
}

func TestDefaultResolvers_NotEmpty(t *testing.T) {
	// Covers the global-registry flattening branch.
	got := defaultResolvers()
	// Live package: in a unit test environment without GCP/AWS creds
	// the resolvers still construct (they're not credential-bound at
	// construction), so we should see both registered resolvers.
	if len(got) < 1 {
		t.Errorf("defaultResolvers returned empty slice")
	}
}

func TestRun_DefaultsWhenDepsNil(t *testing.T) {
	// BuildRegistry and Resolvers nil → fallback to defaults. The
	// default registry will likely error on Fetch in CI (no creds),
	// but the path is still exercised; we just assert non-zero code.
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = nil
	d.Resolvers = nil
	code := run([]string{"--strict-fetch=false"}, d)
	if code != 0 {
		t.Errorf("nil deps with non-strict: code = %d, want 0", code)
	}
}

func TestRun_EnvMode_WriteFailure(t *testing.T) {
	// /dev/full reliably fails os.WriteFile with ENOSPC on unix.
	if runtime.GOOS == "windows" {
		t.Skip("unix-only test")
	}
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	code := run([]string{"--out", "/dev/full/sub/file"}, d)
	if code != 1 {
		t.Errorf("write failure: code = %d, want 1", code)
	}
}

func TestRun_ExecMode_BinaryNotFound(t *testing.T) {
	// Non-ExitError path: a command that doesn't exist → exec.LookPath
	// inside cmd.Run returns an *exec.Error, not *exec.ExitError.
	d := baseDeps()
	d.Getenv = mustEnv(t, map[string]string{
		"CONFIG_LOCATION": "projects/p/locations/l/parameters/x",
		"CONFIG_VERSION":  "v1",
	})
	d.BuildRegistry = successRegistry("FOO=bar")
	code := run([]string{"--mode=exec", "--", "definitely-not-a-binary-xyz"}, d)
	if code != 1 {
		t.Errorf("exec binary-not-found: code = %d, want 1", code)
	}
}

func TestRun_NilEnvStdoutStderrFallbacks(t *testing.T) {
	// All three nil → fallbacks to os.Getenv / os.Stdout / os.Stderr.
	// We can't capture those from the OS, but we can verify the code
	// path runs end-to-end. Help short-circuits before any I/O.
	d := cliDeps{
		Getenv:        nil,
		Stdout:        nil,
		Stderr:        nil,
		BuildRegistry: successRegistry("FOO=bar"),
		Resolvers:     emptyResolvers(),
	}
	if code := run([]string{"-h"}, d); code != 0 {
		t.Errorf("nil-fallback help: code = %d, want 0", code)
	}
}
