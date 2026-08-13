// Package envelope implements envelope encryption for artifact/payload bodies:
// KMS-compatible envelope encryption with separate secrets/payload
// keys.
//
// Each object is encrypted with a freshly-generated 256-bit data key
// (AES-256-GCM). The data key is then wrapped (encrypted) by the master key via
// internal/platform/secretbox (AES-256-GCM, CAVE_LOCAL_ENCRYPTION_KEY locally /
// KMS in prod). Only the wrapped data key is persisted — never the plaintext
// data key — so master-key rotation re-wraps without re-encrypting bodies, and a
// leaked stored object is useless without the master key.
//
// Metadata (the wrapped data key + body nonce + scheme tag) is returned as a
// JSON document for storage in artifacts.encryption_metadata. The ciphertext is
// what goes to the object store.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/secretbox"
)

const (
	schemeV1 = "envelope-aesgcm-v1"
	schemeV2 = "envelope-aesgcm-tenant-v2"
)

// Scope is authenticated with every v2 payload. ProjectID may be empty for
// organization-wide objects; organization and kind are always required.
type Scope struct {
	OrganizationID string
	ProjectID      string
	Kind           string
}

// Metadata is the per-object envelope metadata persisted alongside the
// ciphertext (in artifacts.encryption_metadata).
type Metadata struct {
	Scheme         string `json:"scheme"`
	WrappedDataKey string `json:"wrapped_data_key"` // base64(secretbox(dataKey))
	ScopeHash      string `json:"scope_hash,omitempty"`
}

// Seal compresses-agnostic: it encrypts plaintext with a fresh data key and
// returns (ciphertext, metadataJSON). ciphertext is nonce(12)||GCM(body).
func Seal(plaintext []byte) (ciphertext []byte, metaJSON []byte, err error) {
	return seal(plaintext, schemeV1, nil, "")
}

// SealForScope binds ciphertext authentication to tenant/object scope. Moving
// ciphertext plus metadata to another tenant, project, or object class makes
// decryption fail even when storage and KMS credentials are compromised.
func SealForScope(plaintext []byte, scope Scope) (ciphertext []byte, metaJSON []byte, err error) {
	aad, scopeHash, err := scopeAAD(scope)
	if err != nil {
		return nil, nil, err
	}
	return seal(plaintext, schemeV2, aad, scopeHash)
}

func seal(plaintext []byte, scheme string, aad []byte, scopeHash string) (ciphertext []byte, metaJSON []byte, err error) {
	dataKey := make([]byte, 32)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, nil, fmt.Errorf("envelope: data key entropy: %w", err)
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("envelope: nonce entropy: %w", err)
	}
	ciphertext = gcm.Seal(nonce, nonce, plaintext, aad)

	wrapped, err := secretbox.EncryptPayloadKey(dataKey)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope: wrap data key: %w", err)
	}
	meta := Metadata{Scheme: scheme, WrappedDataKey: base64.StdEncoding.EncodeToString(wrapped), ScopeHash: scopeHash}
	metaJSON, err = json.Marshal(meta)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope: marshal metadata: %w", err)
	}
	return ciphertext, metaJSON, nil
}

// Open reverses Seal: it unwraps the data key from metadata and decrypts the
// ciphertext. An unknown scheme fails closed.
func Open(ciphertext []byte, metaJSON []byte) ([]byte, error) {
	var meta Metadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, fmt.Errorf("envelope: parse metadata: %w", err)
	}
	if meta.Scheme == schemeV2 {
		return nil, fmt.Errorf("envelope: tenant scope required for scheme %q", meta.Scheme)
	}
	if meta.Scheme != schemeV1 {
		return nil, fmt.Errorf("envelope: unknown scheme %q", meta.Scheme)
	}
	return open(ciphertext, meta, nil)
}

// OpenForScope opens v2 ciphertext only for its authenticated scope. It also
// reads v1 ciphertext during migration; all new tenant-object writes use v2.
func OpenForScope(ciphertext []byte, metaJSON []byte, scope Scope) ([]byte, error) {
	var meta Metadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, fmt.Errorf("envelope: parse metadata: %w", err)
	}
	if meta.Scheme == schemeV1 {
		return open(ciphertext, meta, nil)
	}
	if meta.Scheme != schemeV2 {
		return nil, fmt.Errorf("envelope: unknown scheme %q", meta.Scheme)
	}
	aad, scopeHash, err := scopeAAD(scope)
	if err != nil {
		return nil, err
	}
	if meta.ScopeHash != scopeHash {
		return nil, fmt.Errorf("envelope: tenant scope mismatch")
	}
	return open(ciphertext, meta, aad)
}

func open(ciphertext []byte, meta Metadata, aad []byte) ([]byte, error) {
	wrapped, err := base64.StdEncoding.DecodeString(meta.WrappedDataKey)
	if err != nil {
		return nil, fmt.Errorf("envelope: decode wrapped key: %w", err)
	}
	dataKey, err := secretbox.DecryptPayloadKey(wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap data key: %w", err)
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, fmt.Errorf("envelope: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("envelope: ciphertext too short")
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	plaintext, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("envelope: open: %w", err)
	}
	return plaintext, nil
}

func scopeAAD(scope Scope) ([]byte, string, error) {
	scope.OrganizationID = strings.TrimSpace(scope.OrganizationID)
	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	scope.Kind = strings.TrimSpace(scope.Kind)
	if scope.OrganizationID == "" {
		return nil, "", fmt.Errorf("envelope: organization scope is required")
	}
	if scope.Kind == "" {
		return nil, "", fmt.Errorf("envelope: object kind is required")
	}
	aad, err := json.Marshal(struct {
		Version        int    `json:"version"`
		OrganizationID string `json:"organization_id"`
		ProjectID      string `json:"project_id"`
		Kind           string `json:"kind"`
	}{2, scope.OrganizationID, scope.ProjectID, scope.Kind})
	if err != nil {
		return nil, "", fmt.Errorf("envelope: encode scope: %w", err)
	}
	sum := sha256.Sum256(aad)
	return aad, hex.EncodeToString(sum[:]), nil
}
