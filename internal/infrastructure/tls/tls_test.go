package tls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"testing"
)

func TestCertPoolIncludesEmbeddedBundle(t *testing.T) {
	pool := CertPool()
	if pool == nil {
		t.Fatal("CertPool returned nil")
	}
	// Embedded bundle must contribute at least one cert.
	subjects := pool.Subjects()
	if len(subjects) == 0 {
		t.Fatal("pool has no subjects after appending embedded bundle")
	}
}

func TestGRPCOptionReturnsClientOption(t *testing.T) {
	opt := GRPCOption()
	if opt == nil {
		t.Fatal("GRPCOption returned nil")
	}
}

func TestCertPoolFinalize_NilPool(t *testing.T) {
	bundle := []byte("-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n")
	pool := finalizeCertPool(nil, nil, bundle)
	if pool == nil {
		t.Fatal("finalizeCertPool returned nil for nil pool input")
	}
	if len(pool.Subjects()) == 0 {
		t.Log("bundle had no valid PEM; pooled as-is (acceptable)")
	}
}

func TestCertPoolFinalize_ErrorFromSystemCertPool(t *testing.T) {
	bundle := []byte("-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n")
	pool := finalizeCertPool(nil, errors.New("synthetic system failure"), bundle)
	if pool == nil {
		t.Fatal("finalizeCertPool returned nil when an error was passed")
	}
}

func TestCertPoolFinalize_AppendsBundle(t *testing.T) {
	pool := finalizeCertPool(x509.NewCertPool(), nil, embeddedCACerts)
	if len(pool.Subjects()) == 0 {
		t.Fatal("finalizeCertPool did not append embedded bundle")
	}
}

func TestHTTPClientTrustsEmbeddedBundle(t *testing.T) {
	c := HTTPClient()
	if c == nil {
		t.Fatal("HTTPClient returned nil")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport", c.Transport)
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("RootCAs is nil")
	}
	if tr.TLSClientConfig.MinVersion != 0 {
		// Default MinVersion should be unset (lets stdlib pick). We only
		// assert RootCAs plumbing here.
		t.Logf("MinVersion=%v (informational)", tr.TLSClientConfig.MinVersion)
	}
	cfg := &tls.Config{RootCAs: CertPool()}
	if cfg.RootCAs == nil {
		t.Fatal("tls.Config RootCAs is nil")
	}
}
