// Package tlscentralises the trust-store helpers used by every cloud
// adapter. Both GCP (gRPC) and AWS (HTTP) SDK clients need to verify TLS
// against a bundle that includes any custom / intercepting CA — and the
// binary runs in scratch containers without a system cert store, so the
// bundle is embedded at build time.
//
// This package exists so every adapter uses the same cert pool and the
// `certs/ca-certificates.crt` embed directive lives in exactly one place.
package tls

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"net/http"

	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

//go:embed certs/ca-certificates.crt
var embeddedCACerts []byte

// CertPool returns a pool that trusts both the system CA store and the
// embedded bundle. Falls back to an empty pool when SystemCertPool is
// unavailable (e.g. scratch container before embedded bundle loads).
func CertPool() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	return finalizeCertPool(pool, err, embeddedCACerts)
}

// finalizeCertPool returns a pool that always includes bundle.
// Falls back to a fresh pool when SystemCertPool is unavailable.
func finalizeCertPool(pool *x509.CertPool, err error, bundle []byte) *x509.CertPool {
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(bundle)
	return pool
}

// GRPCOption returns a gRPC dial option that trusts the CertPool above.
// Use as: grpc.Dial(addr, tls.GRPCOption()).
func GRPCOption() option.ClientOption {
	creds := credentials.NewTLS(&tls.Config{RootCAs: CertPool()})
	return option.WithGRPCDialOption(grpc.WithTransportCredentials(creds))
}

// HTTPClient returns an *http.Client whose transport trusts CertPool.
// The transport is cloned from DefaultTransport so it inherits the
// connection pool, keep-alive, and timeouts — only TLS roots change.
func HTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: CertPool()}
	return &http.Client{Transport: t}
}
