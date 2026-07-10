// Package tencentcred resolves Tencent Cloud credentials for adapters in
// this module. Credential resolution walks a chain of sources, first
// non-nil wins: TKE pod identity STS → env vars → tccli SSO
// (~/.tccli/default.credential JSON) → tccli INI profile
// (~/.tencentcloud/credentials). The result is cached for the process
// lifetime so a single Fetch that runs both a source and a secret
// resolver hits the network once.
//
// We hand-roll the loop instead of common.NewProviderChain because that
// chain treats only its own unexported "not configured" sentinels as
// skip-signals, and DefaultTkeOIDCRoleArnProvider's missing-env error
// doesn't match them. tccli SSO is parsed in-process because the SDK's
// DefaultProfileProvider only understands INI.
package tencentcred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

// sourceLabels mirrors `sources` by index — describes which credential
// source yielded creds in the chain. First non-nil wins; the label for
// the winning index is logged at resolve time and cached for the process.
var sourceLabels = []string{
	"tke-pod-identity-sts",
	"env",
	"tccli-sso",
	"tccli-profile",
}

// Credentials holds a Tencent Cloud credential set. Token is non-empty for
// STS-style creds (TKE pod identity and tccli SSO).
type Credentials struct {
	SecretID  string
	SecretKey string
	Token     string
}

// ErrNoCredentials is returned when no source in the chain yields
// credentials.
var ErrNoCredentials = errors.New("tencent creds: no credentials via TKE pod identity STS, env, tccli SSO, or ~/.tencentcloud/credentials profile")

var (
	cacheMu      sync.Mutex
	cached       *Credentials
	cachedSource string
	cacheErr     error
)

// sources is the credential lookup order. First non-nil wins.
// Order: TKE pod identity STS > env > tccli SSO > tccli INI profile.
var sources = []func(ctx context.Context) (*Credentials, error){
	stsSource,
	envSource,
	tccliSSOSource,
	profileSource,
}

// Resolve returns a Credentials value from the first source that yields
// one. Cached after first success.
func Resolve(ctx context.Context) (*Credentials, error) {
	c, _, err := resolve(ctx)
	return c, err
}

// resolve is the unexported worker that also returns the source label.
// Public callers use Resolve; Source is logged here once at first hit.
func resolve(ctx context.Context) (*Credentials, string, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cached != nil {
		return cached, cachedSource, nil
	}
	if cacheErr != nil {
		return nil, "", cacheErr
	}

	for i, src := range sources {
		creds, err := src(ctx)
		if err != nil {
			cacheErr = fmt.Errorf("%w: %v", ErrNoCredentials, err)
			return nil, "", cacheErr
		}
		if creds != nil {
			cached = creds
			cachedSource = sourceLabels[i]
			log.Printf("tencent creds: source=%s", cachedSource)
			return cached, cachedSource, nil
		}
	}
	return nil, "", ErrNoCredentials
}

func stsSource(_ context.Context) (*Credentials, error) {
	p, err := common.DefaultTkeOIDCRoleArnProvider()
	if err != nil {
		return nil, nil // env vars missing → not configured
	}
	return extract(p)
}

func envSource(_ context.Context) (*Credentials, error) {
	creds, err := extract(common.DefaultEnvProvider())
	if err != nil {
		return nil, nil // env vars missing → not configured, fall through to next source
	}
	return creds, nil
}

func profileSource(_ context.Context) (*Credentials, error) {
	return extract(common.DefaultProfileProvider())
}

// tccliSSOCredsPath returns the path to the tccli SSO credential file.
// Overridden in tests.
var tccliSSOCredsPath = func() string {
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".tccli", "default.credential")
	}
	return ".tccli/default.credential"
}

// tccliSSOSource reads ~/.tccli/default.credential (JSON, written by
// `tccli sso login`). Returns (nil, nil) when the file is missing or
// unparseable; callers should still try the INI profile afterwards.
func tccliSSOSource(_ context.Context) (*Credentials, error) {
	data, err := os.ReadFile(tccliSSOCredsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, nil
	}
	var f struct {
		SecretID  string `json:"secretId"`
		SecretKey string `json:"secretKey"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, nil
	}
	if f.SecretID == "" || f.SecretKey == "" {
		return nil, nil
	}
	return &Credentials{SecretID: f.SecretID, SecretKey: f.SecretKey, Token: f.Token}, nil
}

func extract(p common.Provider) (*Credentials, error) {
	cred, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return &Credentials{
		SecretID:  cred.GetSecretId(),
		SecretKey: cred.GetSecretKey(),
		Token:     cred.GetToken(),
	}, nil
}

// ResetForTest clears the credential cache. Test-only.
func ResetForTest() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cached = nil
	cachedSource = ""
	cacheErr = nil
}
