package cabundle

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func selfSigned(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// A bundle is all-or-nothing: one bad block rejects the file so the trust store
// is never silently incomplete.
func TestCertificatesFailClosed(t *testing.T) {
	good := selfSigned(t)
	corrupt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	cases := map[string][]byte{
		"garbage":            []byte("not a certificate\n"),
		"corrupt second":     append(append([]byte(nil), good...), corrupt...),
		"corrupt first":      append(append([]byte(nil), corrupt...), good...),
		"truncated tail":     append(append([]byte(nil), good...), good[:len(good)/2]...),
		"only a private key": []byte("-----BEGIN EC PRIVATE KEY-----\nAA==\n-----END EC PRIVATE KEY-----\n"),
	}
	for name, bundle := range cases {
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, bundle, 0o600); err != nil {
			t.Fatal(err)
		}
		if certs, err := Certificates(path); err == nil {
			t.Fatalf("%s: accepted %d certificate(s) from an unusable bundle", name, len(certs))
		}
	}
	if _, err := Certificates(filepath.Join(t.TempDir(), "absent.pem")); err == nil {
		t.Fatal("missing file accepted")
	}

	two := filepath.Join(t.TempDir(), "two.pem")
	if err := os.WriteFile(two, append(append([]byte(nil), good...), selfSigned(t)...), 0o600); err != nil {
		t.Fatal(err)
	}
	certs, err := Certificates(two)
	if err != nil || len(certs) != 2 {
		t.Fatalf("two-cert bundle: certs=%d err=%v", len(certs), err)
	}
	if pool, err := Pool(two); err != nil || pool == nil {
		t.Fatalf("Pool: %v", err)
	}
}
