package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type issuer struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

var serial int64

func nextSerial() *big.Int { serial++; return big.NewInt(serial) }

func newIssuer(t *testing.T, name string) issuer {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: nextSerial(), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return issuer{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf issues a certificate and returns its PEM certificate and key.
func (i issuer) leaf(t *testing.T, template *x509.Certificate, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template.SerialNumber, template.NotBefore, template.NotAfter = nextSerial(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	template.ExtKeyUsage = []x509.ExtKeyUsage{usage}
	der, err := x509.CreateCertificate(rand.Reader, template, i.cert, &key.PublicKey, i.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestTLSListenerMapsClientCertificatesToPrincipals(t *testing.T) {
	dir := t.TempDir()
	ca, stranger := newIssuer(t, "caveman test CA"), newIssuer(t, "someone else")
	serverCert, serverKey := ca.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "server-1"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}, x509.ExtKeyUsageServerAuth)
	files := map[string][]byte{"server.crt": serverCert, "server.key": serverKey, "ca.crt": ca.pem}
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), string(content))
	}
	writeFile(t, filepath.Join(dir, "tokens.yaml"), "principals:\n  - name: mtls:uri:spiffe://example.org/agent-a\n    namespaces: [\"team-a/*\"]\n"+
		entry("team-a", `["team-a/*"]`, tokenA1))
	serverTLS, err := NewServerTLS(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New(Config{Token: "legacy-shared-token-0123", TokenMapFile: filepath.Join(dir, "tokens.yaml"), MTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver.UseServerTLS(serverTLS)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := resolver.Identify(r)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		allowed := "no"
		if p.Allows("team-a/x") {
			allowed = "yes"
		}
		_, _ = io.WriteString(w, p.Name+" "+p.Mechanism+" "+allowed)
	})}
	go func() { _ = server.Serve(tls.NewListener(listener, serverTLS.Config())) }()
	t.Cleanup(func() { _ = server.Close() })
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.pem)
	call := func(client issuer, template *x509.Certificate, maxVersion uint16, header string) (string, error) {
		t.Helper()
		config := &tls.Config{RootCAs: roots, MaxVersion: maxVersion}
		if template != nil {
			certPEM, keyPEM := client.leaf(t, template, x509.ExtKeyUsageClientAuth)
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				t.Fatal(err)
			}
			// Always send it, even when its issuer is not one the server
			// advertises, so the server's own verification is what is tested.
			config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil }
		}
		httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: config}}
		defer httpClient.CloseIdleConnections()
		req, _ := http.NewRequest(http.MethodGet, "https://"+listener.Addr().String()+"/", nil)
		if header != "" {
			req.Header.Set("Authorization", "Bearer "+header)
		}
		response, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.Status + " " + string(body), nil
	}
	spiffe, _ := url.Parse("spiffe://example.org/agent-a")
	for name, tc := range map[string]struct {
		client   issuer
		template *x509.Certificate
		header   string
		want     string
	}{
		"URI SAN wins": {ca, &x509.Certificate{Subject: pkix.Name{CommonName: "cn"}, URIs: []*url.URL{spiffe}, DNSNames: []string{"agent-a.internal"}}, "", "200 OK mtls:uri:spiffe://example.org/agent-a mtls yes"},
		"DNS SAN next": {ca, &x509.Certificate{Subject: pkix.Name{CommonName: "cn"}, DNSNames: []string{"agent-b.internal"}}, "", "200 OK mtls:dns:agent-b.internal mtls no"},
		// S2: byte for byte, so an upper-case scheme is another principal.
		"URI SAN case kept": {ca, &x509.Certificate{URIs: []*url.URL{{Scheme: "SPIFFE", Host: "example.org", Path: "/agent-a"}}}, "", "200 OK mtls:uri:SPIFFE://example.org/agent-a mtls no"},
		// S2: the CN is opt-in; by default a CN naming the token principal
		// team-a identifies no one.
		"CN off by default":  {ca, &x509.Certificate{Subject: pkix.Name{CommonName: "team-a"}}, "", "401 Unauthorized "},
		"operator name":      {ca, &x509.Certificate{Subject: pkix.Name{CommonName: Operator}}, "", "401 Unauthorized "},
		"no certificate":     {ca, nil, "", "401 Unauthorized "},
		"bearer over TLS":    {ca, nil, "legacy-shared-token-0123", "200 OK single_operator token yes"},
		"bearer beats cert":  {ca, &x509.Certificate{Subject: pkix.Name{CommonName: "agent-c"}}, "wrong-token-0123456789", "401 Unauthorized "},
		"no usable identity": {ca, &x509.Certificate{}, "", "401 Unauthorized "},
	} {
		got, err := call(tc.client, tc.template, 0, tc.header)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", name, got, err, tc.want)
		}
	}
	if got, err := call(stranger, &x509.Certificate{Subject: pkix.Name{CommonName: "agent-a"}}, 0, ""); err == nil {
		t.Errorf("certificate from an unknown CA accepted: %q", got)
	}
	if got, err := call(ca, nil, tls.VersionTLS11, "legacy-shared-token-0123"); err == nil {
		t.Errorf("TLS 1.1 accepted: %q", got)
	}

	// A renewed server certificate is served after Reload, without a restart.
	renewedCert, renewedKey := ca.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "server-2"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}, x509.ExtKeyUsageServerAuth)
	writeFile(t, filepath.Join(dir, "server.crt"), string(renewedCert))
	writeFile(t, filepath.Join(dir, "server.key"), string(renewedKey))
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(dir, "server.crt"), future, future)
	if reloaded, err := serverTLS.Reload(false); !reloaded || err != nil {
		t.Fatalf("reload: %v %v", reloaded, err)
	}
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "server-2" {
		t.Fatalf("served %q after reload, want server-2", cn)
	}
	// A broken replacement keeps the working certificate.
	writeFile(t, filepath.Join(dir, "server.key"), "not a key")
	if _, err := serverTLS.Reload(true); err == nil || strings.Contains(err.Error(), "not a key") {
		t.Fatalf("broken key: %v", err)
	}
	if _, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots}); err != nil {
		t.Fatalf("broken reload took the listener down: %v", err)
	}
}

func TestNewServerTLSRejectsIncompleteFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "empty-ca.crt"), "no certificates here")
	ca := newIssuer(t, "ca")
	certPEM, keyPEM := ca.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "s"}}, x509.ExtKeyUsageServerAuth)
	writeFile(t, filepath.Join(dir, "s.crt"), string(certPEM))
	writeFile(t, filepath.Join(dir, "s.key"), string(keyPEM))
	for name, files := range map[string][3]string{
		"no key":      {filepath.Join(dir, "s.crt"), "", ""},
		"missing":     {filepath.Join(dir, "absent.crt"), filepath.Join(dir, "s.key"), ""},
		"empty CA":    {filepath.Join(dir, "s.crt"), filepath.Join(dir, "s.key"), filepath.Join(dir, "empty-ca.crt")},
		"swapped key": {filepath.Join(dir, "s.key"), filepath.Join(dir, "s.crt"), ""},
	} {
		if _, err := NewServerTLS(files[0], files[1], files[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// serverFiles writes a server certificate from ca and returns dir.
func serverFiles(t *testing.T, ca issuer) string {
	t.Helper()
	dir := t.TempDir()
	certPEM, keyPEM := ca.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "server"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}, x509.ExtKeyUsageServerAuth)
	for name, content := range map[string][]byte{"server.crt": certPEM, "server.key": keyPEM, "ca.crt": ca.pem} {
		writeFile(t, filepath.Join(dir, name), string(content))
	}
	return dir
}

// S6: rotating the client CA cuts a keep-alive connection opened under the old
// CA at its next request, not at its next handshake.
func TestClientCARotationCutsLiveConnections(t *testing.T) {
	ca, next := newIssuer(t, "old client CA"), newIssuer(t, "new client CA")
	dir := serverFiles(t, ca)
	writeFile(t, filepath.Join(dir, "tokens.yaml"), "principals:\n  - name: mtls:dns:agent.internal\n    namespaces: [\"*\"]\n")
	serverTLS, err := NewServerTLS(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New(Config{TokenMapFile: filepath.Join(dir, "tokens.yaml"), MTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	resolver.UseServerTLS(serverTLS)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := resolver.Identify(r); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
		}
	})}
	go func() { _ = server.Serve(tls.NewListener(listener, serverTLS.Config())) }()
	t.Cleanup(func() { _ = server.Close() })
	certPEM, keyPEM := ca.leaf(t, &x509.Certificate{DNSNames: []string{"agent.internal"}}, x509.ExtKeyUsageClientAuth)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.pem)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &pair, nil }}}}
	t.Cleanup(client.CloseIdleConnections)
	call := func() (status int, reused bool) {
		t.Helper()
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+listener.Addr().String()+"/", nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode, reused
	}
	if status, _ := call(); status != http.StatusOK {
		t.Fatalf("before rotation: %d", status)
	}
	// Reloading an unchanged CA, as a server certificate renewal does, keeps
	// the connection working.
	if _, err := serverTLS.Reload(true); err != nil {
		t.Fatal(err)
	}
	if status, reused := call(); status != http.StatusOK || !reused {
		t.Fatalf("after an unchanged reload: %d, reused %v", status, reused)
	}
	writeFile(t, filepath.Join(dir, "ca.crt"), string(next.pem))
	if _, err := serverTLS.Reload(true); err != nil {
		t.Fatal(err)
	}
	if status, reused := call(); status != http.StatusUnauthorized || !reused {
		t.Fatalf("after rotation, same connection: %d, reused %v", status, reused)
	}
}

// S2: a CN names a principal only when enabled, and then as mtls:cn:<CN>,
// never the token principal it spells. With no ServerTLS to re-verify
// against, no certificate identifies anyone.
func TestCommonNameIsOptInAndPrefixed(t *testing.T) {
	ca := newIssuer(t, "client CA")
	dir := serverFiles(t, ca)
	writeFile(t, filepath.Join(dir, "tokens.yaml"), mapFile(entry("team-a", `["team-a/*"]`, tokenA1)))
	serverTLS, err := NewServerTLS(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := ca.leaf(t, &x509.Certificate{Subject: pkix.Name{CommonName: "team-a"}}, x509.ExtKeyUsageClientAuth)
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	req := bearer("")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca.cert}}}
	for _, tc := range []struct {
		commonName, attached bool
		want                 string
	}{{false, true, ""}, {true, true, "mtls:cn:team-a"}, {true, false, ""}} {
		r, err := New(Config{TokenMapFile: filepath.Join(dir, "tokens.yaml"), MTLS: true, MTLSCommonName: tc.commonName})
		if err != nil {
			t.Fatal(err)
		}
		if tc.attached {
			r.UseServerTLS(serverTLS)
		}
		p, err := r.Identify(req)
		if p.Name != tc.want || (err == nil) != (tc.want != "") || p.Allows("team-a/x") {
			t.Errorf("CN %v, ServerTLS %v: %q %v, want %q", tc.commonName, tc.attached, p.Name, err, tc.want)
		}
	}
}
