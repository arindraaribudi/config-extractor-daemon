package tencentcred

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolve_EnvVars(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRETID", "env-id")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "env-key")
	resetCache()

	// Point metadata server at a 500-er to confirm we never hit it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("metadata server must not be called when env vars set; got %s", r.URL.Path)
	}))
	defer srv.Close()
	prevBase := metadataBase
	metadataBase = srv.URL
	defer func() { metadataBase = prevBase }()

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "env-id" || got.SecretKey != "env-key" {
		t.Fatalf("got %+v, want env-id/env-key", got)
	}
}

func TestResolve_MetadataFallback(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRETID", "")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "")
	resetCache()

	// Role-name endpoint returns the role; credentials endpoint returns the creds.
	mux := http.NewServeMux()
	mux.HandleFunc("/latest/meta-data/cam/security-credentials/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("my-role"))
	})
	mux.HandleFunc("/latest/meta-data/cam/security-credentials/my-role", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"TmpSecretId":"meta-id","TmpSecretKey":"meta-key"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	prevBase := metadataBase
	metadataBase = srv.URL
	defer func() { metadataBase = prevBase }()

	got, err := Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.SecretID != "meta-id" || got.SecretKey != "meta-key" {
		t.Fatalf("got %+v, want meta-id/meta-key", got)
	}
}

func TestResolve_NoCredentials(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRETID", "")
	t.Setenv("TENCENTCLOUD_SECRETKEY", "")
	resetCache()

	// Metadata server returns 404 → not on CVM/TKE.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	prevBase := metadataBase
	metadataBase = srv.URL
	defer func() { metadataBase = prevBase }()

	_, err := Resolve(t.Context())
	if err == nil {
		t.Fatal("expected error when no creds available")
	}
	if want := "TENCENTCLOUD_SECRETID"; !contains(err.Error(), want) {
		t.Fatalf("error %q should mention %q", err.Error(), want)
	}
}

func resetCache() {
	cacheMu.Lock()
	cached = nil
	cacheErr = nil
	cacheMu.Unlock()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
