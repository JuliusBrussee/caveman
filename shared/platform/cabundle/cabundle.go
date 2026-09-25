// Package cabundle builds an x509 root pool from the system trust store plus
// operator-supplied PEM bundles — the shape corporate TLS inspection needs, where
// a private root must be trusted alongside the public ones.
package cabundle

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// Pool returns the system pool with every certificate in each PEM file appended.
// Appending (rather than replacing) keeps public endpoints verifiable while a
// private CA is trusted for the inspected ones. Any unusable bundle fails the
// whole pool CLOSED; see Certificates.
func Pool(paths ...string) (*x509.CertPool, error) {
	var certs []*x509.Certificate
	for _, path := range paths {
		loaded, err := Certificates(path)
		if err != nil {
			return nil, err
		}
		certs = append(certs, loaded...)
	}
	return PoolOf(certs)
}

// PoolOf returns the system pool with certs appended.
func PoolOf(certs []*x509.Certificate) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}
	for _, cert := range certs {
		roots.AddCert(cert)
	}
	return roots, nil
}

// Certificates parses every certificate in the PEM bundle at path.
//
// The bundle is parsed block by block instead of via CertPool.AppendCertsFromPEM,
// which reports success as soon as ONE certificate parses and silently drops the
// rest. A truncated or corrupt bundle would then be half-trusted: the endpoints
// whose issuer survived keep verifying and the ones whose issuer was dropped fail
// later, looking like a network fault. Any unusable certificate block — or a
// trailing PEM header with no complete block behind it — rejects the whole
// bundle instead, and nothing from it is returned.
func Certificates(path string) ([]*x509.Certificate, error) {
	bundle, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var certs []*x509.Certificate
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: certificate %d is unparseable, so the bundle is incomplete and must not be half-trusted: %w", path, len(certs)+1, err)
		}
		certs = append(certs, cert)
	}
	if bytes.Contains(rest, []byte("-----BEGIN")) {
		return nil, fmt.Errorf("%s: trailing PEM block is truncated after %d certificate(s), so the bundle is incomplete and must not be half-trusted", path, len(certs))
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s contains no valid PEM certificate", path)
	}
	return certs, nil
}
