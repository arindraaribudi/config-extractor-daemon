// Command config-extractor fetches a config payload from a cloud provider
// (GCP Parameter Manager or AWS SSM), resolves any __SECRET_REF__() tokens
// in the payload against the matching secrets backend, then either writes
// the result to a .env file or injects it into a child process.
//
// See CLAUDE.md for full env/flag reference.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"

	"github.com/arindraaribudi/config-extractor-daemon/internal/application"
	"github.com/arindraaribudi/config-extractor-daemon/internal/domain"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/secrets"
	"github.com/arindraaribudi/config-extractor-daemon/internal/infrastructure/source"
)

func main() {
	deps := cliDeps{
		Getenv:        os.Getenv,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		BuildRegistry: defaultBuildRegistry,
		Resolvers:     defaultResolvers(),
	}
	os.Exit(run(os.Args[1:], deps))
}

// envGetter abstracts os.Getenv so tests can inject a static env.
type envGetter func(string) string

// cliDeps carries everything run() needs that would otherwise come from
// process-global state (env, stdout/stderr) or from a side-effecting
// constructor (the source registry, the secrets resolver list). Tests
// override BuildRegistry/Resolvers to inject deterministic fakes.
type cliDeps struct {
	Getenv        envGetter
	Stdout        io.Writer
	Stderr        io.Writer
	BuildRegistry func(ctx context.Context, mode domain.FetchMode) []application.SourceEntry
	Resolvers     []domain.SecretResolver
}

// fatalf prints to the logger and returns the requested exit code.
// We avoid log.Fatal / log.Fatalf because those call os.Exit(1) and
// would bypass the testable return path of run().
func fatalf(logger *log.Logger, code int, format string, args ...any) int {
	logger.Printf(format, args...)
	return code
}

// run is the testable core of the CLI. Returns a process exit code.
func run(args []string, deps cliDeps) int {
	getenv := deps.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := deps.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	// Send log output to the injected stderr so tests can assert on it
	// without leaking into the global logger.
	logger := log.New(stderr, "", log.LstdFlags)
	log.SetOutput(stderr)

	fs := flag.NewFlagSet("config-extractor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "env", "env: write .env file | exec: inject into child process")
	envOut := fs.String("out", ".env", "output path for .env file (mode=env only)")
	installTo := fs.String("install", "", "copy this binary to the given directory and exit")
	strictFetch := fs.Bool("strict-fetch", true, "exit non-zero if parameter fetch fails (default true)")
	strictSecretRefs := fs.Bool("strict-secret-refs", true, "exit non-zero if any __SECRET_REF__() fails to resolve (default true)")

	if err := fs.Parse(args); err != nil {
		// ErrHelp (-h/-help) prints usage and returns 0; flag package
		// returns it from Parse. Anything else is a usage error → 2.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	logger.Printf("platform=%s/%s", runtime.GOOS, runtime.GOARCH)

	if *installTo != "" {
		if err := (application.InstallBinaryUseCase{DestDir: *installTo}).Run(); err != nil {
			return fatalf(logger, 1, "install: %v", err)
		}
		fmt.Fprintf(stdout, "installed to %s\n", *installTo)
		return 0
	}

	location := getenv("CONFIG_LOCATION")
	version := getenv("CONFIG_VERSION")
	fetchMode := domain.FetchMode(getenv("CONFIG_FETCH_MODE"))
	if fetchMode == "" {
		fetchMode = domain.FetchGet
	}

	if location == "" {
		return fatalf(logger, 1, "CONFIG_LOCATION is required")
	}
	if version == "" {
		return fatalf(logger, 1, "CONFIG_VERSION is required")
	}

	// Resolve a config source for the configured location. Strict-mode
	// errors here are fatal regardless of strictFetch because no source
	// means we cannot fetch at all.
	buildRegistry := deps.BuildRegistry
	if buildRegistry == nil {
		buildRegistry = defaultBuildRegistry
	}
	registry := buildRegistry(context.Background(), fetchMode)
	loadUC := application.LoadConfigUseCase{Registry: registry, Strict: *strictFetch}

	loaded, err := loadUC.Run(context.Background(), location, version, fetchMode)
	if err != nil {
		return fatalf(logger, 1, "%v", err)
	}

	// 3) Parse payload → resolve secret refs → write or exec.
	pairs := domain.ParsePayload(string(loaded.Payload))

	resolvers := deps.Resolvers
	if resolvers == nil {
		resolvers = defaultResolvers()
	}
	resolveUC := application.ResolveSecretsUseCase{Resolvers: resolvers, Strict: *strictSecretRefs}
	resolved, err := resolveUC.Run(context.Background(), pairs)
	if err != nil {
		return fatalf(logger, 1, "%v", err)
	}

	switch *mode {
	case "env":
		if err := (application.WriteEnvUseCase{Path: *envOut}).Run(resolved.Pairs); err != nil {
			return fatalf(logger, 1, "%v", err)
		}
		if len(resolved.Pairs) > 0 {
			fmt.Fprintf(stdout, "wrote %d var(s) to %s\n", len(resolved.Pairs), *envOut)
		} else {
			fmt.Fprintf(stdout, "wrote empty file to %s (no config fetched)\n", *envOut)
		}
		return 0

	case "exec":
		cmdArgs := fs.Args()
		if len(cmdArgs) == 0 {
			return fatalf(logger, 1, "--mode=exec requires a command, e.g.: --mode=exec -- /app/server")
		}
		if len(resolved.Pairs) > 0 {
			logger.Printf("exec: injecting %d var(s) from %s@%s into %s", len(resolved.Pairs), location, version, cmdArgs[0])
		} else {
			logger.Printf("exec: no config loaded from %s@%s — running %s without injected vars", location, version, cmdArgs[0])
		}
		if err := (application.ExecChildUseCase{Args: cmdArgs}).Run(resolved.Pairs); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			return fatalf(logger, 1, "exec: %v", err)
		}
		return 0

	default:
		return fatalf(logger, 1, "unknown mode %q: use 'env' or 'exec'", *mode)
	}
}

// defaultBuildRegistry materialises a use-case-compatible slice from the
// infrastructure registry. We construct each source eagerly so any auth
// errors surface up-front (rather than on the first Fetch call).
func defaultBuildRegistry(ctx context.Context, mode domain.FetchMode) []application.SourceEntry {
	out := make([]application.SourceEntry, 0, len(source.Sources))
	for _, s := range source.Sources {
		src, err := s.Factory(ctx, mode)
		if err != nil {
			log.Printf("WARNING: skipping source %s: %v", s.Kind, err)
			continue
		}
		out = append(out, application.SourceEntry{
			Match:  s.Match,
			Source: src,
		})
	}
	return out
}

// defaultResolvers flattens the global secrets registry into the slice
// shape the use case expects.
func defaultResolvers() []domain.SecretResolver {
	resolvers := make([]domain.SecretResolver, 0, len(secrets.Resolvers))
	for _, r := range secrets.Resolvers {
		resolvers = append(resolvers, r.Resolver)
	}
	return resolvers
}
