package identity

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
)

// ServerTLS is the proxy's TLS listener configuration. Certificate, key and
// client CA are re-read on Reload, so a renewed certificate (cert-manager, a
// rotated Secret) takes effect without a restart. Every handshake uses the
// current files.
type ServerTLS struct {
	cert, key, clientCA string
	stamp               string
	current             atomic.Pointer[tls.Config]
	ca                  atomic.Pointer[clientCA]
}

// clientCA is one load of the client CA file. roots holds the very
// certificate values in pool, so a chain the handshake verified against this
// load ends in one of them, and a chain from any other load does not.
type clientCA struct {
	pem   []byte
	pool  *x509.CertPool
	roots map[*x509.Certificate]bool
}

// NewServerTLS loads the files once and fails on anything unusable.
// clientCA is optional; with it, a client certificate is verified when
// presented (not required: probes and bearer-token clients connect without one).
func NewServerTLS(certFile, keyFile, clientCAFile string) (*ServerTLS, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("tls: both a certificate and a key file are required")
	}
	t := &ServerTLS{cert: certFile, key: keyFile, clientCA: clientCAFile}
	if _, err := t.Reload(true); err != nil {
		return nil, err
	}
	return t, nil
}

// Config is the listener's tls.Config: TLS 1.2 or later, and in TLS 1.2 only
// forward-secret AEAD suites (TLS 1.3 suites are all of that kind already).
func (t *ServerTLS) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return t.current.Load(), nil
	}}
}

// Reload re-reads the files; unless force, only when one changed. Files that no
// longer load keep the previous configuration and return the error.
func (t *ServerTLS) Reload(force bool) (bool, error) {
	stamp := fileStamp(t.cert, t.key, t.clientCA)
	if !force && stamp == t.stamp {
		return false, nil
	}
	pair, err := tls.LoadX509KeyPair(t.cert, t.key)
	if err != nil {
		return false, fmt.Errorf("tls certificate: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		// HTTP/1.1 only: the listener is served by http.Server.Serve, which does
		// not set up HTTP/2.
		NextProtos: []string{"http/1.1"},
	}
	if t.clientCA != "" {
		data, err := os.ReadFile(t.clientCA)
		if err != nil {
			return false, fmt.Errorf("tls client CA: %w", err)
		}
		// An unchanged file keeps its load, so renewing the server certificate
		// leaves live connections on verified's fast path.
		ca := t.ca.Load()
		if ca == nil || !bytes.Equal(ca.pem, data) {
			if ca, err = loadClientCA(data); err != nil {
				return false, err
			}
		}
		cfg.ClientCAs, cfg.ClientAuth = ca.pool, tls.VerifyClientCertIfGiven
		// Before the config: no request is checked against an older CA than
		// its handshake used.
		t.ca.Store(ca)
	}
	t.current.Store(cfg)
	t.stamp = stamp
	return true, nil
}

// loadClientCA keeps the PEM certificates AppendCertsFromPEM would.
func loadClientCA(data []byte) (*clientCA, error) {
	ca := &clientCA{pem: data, pool: x509.NewCertPool(), roots: map[*x509.Certificate]bool{}}
	for rest := data; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		ca.pool.AddCert(cert)
		ca.roots[cert] = true
	}
	if len(ca.roots) == 0 {
		return nil, errors.New("tls client CA: no certificate in file")
	}
	return ca, nil
}

// verified reports whether the connection's client certificate verifies
// against the client CA in force now, not only against its handshake's.
func (t *ServerTLS) verified(state *tls.ConnectionState) bool {
	ca := t.ca.Load()
	if ca == nil || len(state.PeerCertificates) == 0 {
		return false
	}
	for _, chain := range state.VerifiedChains {
		if len(chain) > 0 && ca.roots[chain[len(chain)-1]] {
			return true // verified against this very load at handshake
		}
	}
	opts := x509.VerifyOptions{Roots: ca.pool, Intermediates: x509.NewCertPool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	for _, cert := range state.PeerCertificates[1:] {
		opts.Intermediates.AddCert(cert)
	}
	_, err := state.PeerCertificates[0].Verify(opts)
	return err == nil
}
