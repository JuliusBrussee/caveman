package identity

import (
	"crypto/tls"
	"crypto/x509"
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
		pem, err := os.ReadFile(t.clientCA)
		if err != nil {
			return false, fmt.Errorf("tls client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return false, errors.New("tls client CA: no certificate in file")
		}
		cfg.ClientCAs, cfg.ClientAuth = pool, tls.VerifyClientCertIfGiven
	}
	t.current.Store(cfg)
	t.stamp = stamp
	return true, nil
}
