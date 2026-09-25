// Package postgresconfig builds pgx pools with production TLS identity checks.
package postgresconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/runtimeenv"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	caEnvironment     = "CAVE_POSTGRES_CA_CERT"
	caFileEnvironment = "CAVE_POSTGRES_CA_CERT_FILE"
)

// ParsePoolConfig validates the connection string and applies the managed
// database CA to every TLS path; with no CA configured, verify-full checks the
// server against the system roots. Production rejects sslmode=require because it
// encrypts without authenticating the server; verify-full is mandatory.
//
// Outside production a non-loopback host must name its sslmode: verify-full, or
// disable, require or verify-ca as the operator's explicit opt-out (a private
// network, a TLS sidecar). No sslmode means pgx's prefer, which skips
// certificate checks and silently falls back to plaintext, so it and allow are
// refused. Loopback hosts and unix sockets accept any mode.
//
// The mode is read off pgx's parsed config, never the string: pgx resolves
// quoting, a repeated key (the last wins), PGSSLMODE and service files, and a
// check that parsed the string itself could pass a mode pgx does not run.
func ParsePoolConfig(databaseURL string) (*pgxpool.Config, error) {
	production := runtimeenv.IsProduction()
	if production {
		parsed, err := url.Parse(databaseURL)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
			return nil, errors.New("postgres: production DATABASE_URL must be a Postgres URL")
		}
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DATABASE_URL: %w", err)
	}
	caPEM, err := caPEMFromEnvironment()
	if err != nil {
		return nil, err
	}
	verifyFull, mixed := tlsModes(config)
	if production && !verifyFull {
		return nil, errors.New("postgres: production DATABASE_URL requires sslmode=verify-full")
	}
	if !production && mixed && !loopback(config) {
		return nil, fmt.Errorf("postgres: the connection to %s would try TLS that checks no certificate, then plaintext (sslmode prefer or allow, pgx's default); use sslmode=verify-full (or name disable, require or verify-ca explicitly to opt out)", config.ConnConfig.Host)
	}
	if caPEM == "" {
		return config, nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("postgres: %s contains no valid certificate", caEnvironment)
	}
	if config.ConnConfig.TLSConfig == nil {
		return nil, errors.New("postgres: CA certificate configured while TLS is disabled")
	}
	config.ConnConfig.TLSConfig.RootCAs = roots
	config.ConnConfig.TLSConfig.InsecureSkipVerify = false
	config.ConnConfig.TLSConfig.ServerName = config.ConnConfig.Host
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.TLSConfig == nil {
			continue
		}
		fallback.TLSConfig.RootCAs = roots
		fallback.TLSConfig.InsecureSkipVerify = false
		fallback.TLSConfig.ServerName = fallback.Host
	}
	return config, nil
}

// tlsModes reads the sslmode pgx will run off every attempt the pool may make
// (each host, each fallback). verifyFull: every attempt is TLS that verifies
// the certificate and the server name. mixed: TLS and plaintext attempts are
// mixed, which only prefer (pgx's default when nothing names a mode) and allow
// produce; disable is all plaintext, and require and verify-ca all TLS. A unix
// socket attempt is plaintext whatever the mode (pgx, like libpq, runs no TLS
// over one), so it counts toward neither side of mixed.
func tlsModes(config *pgxpool.Config) (verifyFull, mixed bool) {
	type attempt struct {
		host string
		tls  *tls.Config
	}
	attempts := []attempt{{config.ConnConfig.Host, config.ConnConfig.TLSConfig}}
	for _, fallback := range config.ConnConfig.Fallbacks {
		attempts = append(attempts, attempt{fallback.Host, fallback.TLSConfig})
	}
	verifyFull = true
	var encrypted, plaintext bool
	for _, a := range attempts {
		verifyFull = verifyFull && a.tls != nil && !a.tls.InsecureSkipVerify && a.tls.ServerName != ""
		if !strings.HasPrefix(a.host, "/") {
			encrypted, plaintext = encrypted || a.tls != nil, plaintext || a.tls == nil
		}
	}
	return verifyFull, encrypted && plaintext
}

// loopback reports whether every host the pool may dial is a unix socket or a
// loopback address.
func loopback(config *pgxpool.Config) bool {
	hosts := []string{config.ConnConfig.Host}
	for _, fallback := range config.ConnConfig.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); host != "localhost" && !strings.HasPrefix(host, "/") && (ip == nil || !ip.IsLoopback()) {
			return false
		}
	}
	return true
}

func caPEMFromEnvironment() (string, error) {
	filePath := strings.TrimSpace(os.Getenv(caFileEnvironment))
	direct := strings.TrimSpace(os.Getenv(caEnvironment))
	if filePath != "" && direct != "" {
		return "", fmt.Errorf("postgres: set only %s or %s, not both", caFileEnvironment, caEnvironment)
	}
	if filePath == "" {
		return direct, nil
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("postgres: %s: %w", caFileEnvironment, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("postgres: %s must point to a regular file", caFileEnvironment)
	}
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("postgres: read %s: %w", caFileEnvironment, err)
	}
	return strings.TrimSpace(string(contents)), nil
}

// NewPool constructs a pool from the hardened configuration.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := ParsePoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, config)
}

// WithOrg runs fn inside a transaction whose tenant GUC is transaction-local.
// Empty scopes are rejected: tenant work must never degrade to an unscoped
// query when RLS is the hard boundary.
func WithOrg(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(pgx.Tx) error) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return errors.New("postgres: organization scope is required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_organization_id', $1, true)`, orgID); err != nil {
		return fmt.Errorf("postgres: set organization scope: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
