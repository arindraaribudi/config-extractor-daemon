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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
)

// sourceLabels mirrors `sources` by index — describes which credential
// source yielded creds in the chain. First non-nil wins; the label for
// the winning index is logged at resolve time and cached for the process.
var sourceLabels = []string{
	"tke-pod-identity-auto",
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
	tkeAutoSource,
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


func tkeAutoSource(_ context.Context) (*Credentials, error) {
	// TKE pod identity webhook auto-injects TKE_ROLE_ARN + a projected JWT
	// at /var/run/secrets/cloud.tencent.com/serviceaccount/token, but it
	// does NOT inject TKE_REGION / TKE_PROVIDER_ID — those are cluster
	// constants and are recoverable from the JWT's `iss` claim:
	//   https://{region}-oidc.tke.tencentcs.com/id/{providerId}
	// We read the token, parse the issuer, and call NewOIDCRoleArnProvider
	// with explicit values so deployments don't need to set those env vars.
	roleArn := os.Getenv("TKE_ROLE_ARN")
	if roleArn == "" {
		return nil, nil
	}
	tokenFile := os.Getenv("TKE_WEB_IDENTITY_TOKEN_FILE")
	if tokenFile == "" {
		tokenFile = "/var/run/secrets/cloud.tencent.com/serviceaccount/token"
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, nil // not a TKE pod → not configured
	}
	region, providerID, err := parseTKEIssuer(string(tokenBytes))
	if err != nil {
		return nil, nil // token present but issuer not TKE-shaped → fall through
	}
	sessionName := "tencentcloud-go-sdk-" + strconv.FormatInt(time.Now().UnixNano()/1000, 10)
	p := common.NewOIDCRoleArnProvider(region, providerID, string(tokenBytes), roleArn, sessionName, 7200)
	return extract(p)
}

// parseTKEIssuer extracts region + provider ID from a TKE OIDC JWT.
// Issuer format: https://{region}-oidc.tke.tencentcs.com/id/{providerId}.
// Returns error when the token is not TKE-shaped; callers translate that
// to (nil, nil) so the chain falls through.
func parseTKEIssuer(token string) (region, providerID string, err error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", err
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", err
	}
	u, err := url.Parse(claims.Iss)
	if err != nil {
		return "", "", err
	}
	host := u.Host
	if !strings.HasSuffix(host, ".tke.tencentcs.com") {
		return "", "", fmt.Errorf("not a TKE issuer: %s", host)
	}
	regionPart, _, ok := strings.Cut(host, "-oidc.")
	if !ok {
		return "", "", fmt.Errorf("unexpected TKE host: %s", host)
	}
	idPath := strings.TrimPrefix(u.Path, "/id/")
	if idPath == "" || u.Path != "/id/"+idPath {
		return "", "", fmt.Errorf("unexpected TKE path: %s", u.Path)
	}
	return regionPart, idPath, nil
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
	// DefaultProfileProvider returns the SDK's unexported fileDoseNotExist
	// sentinel ("could not find config file") when ~/.tencentcloud/credentials
	// is absent. That sentinel aborts the chain via resolve(), so stat the
	// path ourselves and return (nil, nil) when the file is missing —
	// matching the not-configured contract used by stsSource / envSource.
	if _, ok := os.LookupEnv("TENCENTCLOUD_CREDENTIALS_FILE"); !ok {
		if path := defaultProfilePath(); path != "" {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
		}
	}
	return extract(common.DefaultProfileProvider())
}

// defaultProfilePath returns ~/.tencentcloud/credentials on *nix or
// %USERPROFILE%\.tencentcloud\credentials on Windows. Empty string means
// home is unset → not configured.
func defaultProfilePath() string {
	var home string
	if runtime.GOOS == "windows" {
		home = os.Getenv("USERPROFILE")
	} else {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".tencentcloud", "credentials")
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
