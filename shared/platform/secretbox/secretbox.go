// Package secretbox provides AES-256-GCM authenticated encryption for secrets
// at rest (webhook signing secrets, OIDC client secrets, …), keyed by the
// CAVE_LOCAL_ENCRYPTION_KEY environment variable.
//
// It is the shared, module-level home for the same scheme the control-api store
// uses for provider credentials, so that services which cannot import the
// control-api internal package (notably the worker, which must decrypt a
// webhook secret to sign deliveries) can encrypt/decrypt with byte-identical
// semantics.
//
// Wire format (raw bytes): nonce(12) || GCM(ciphertext+tag). Callers that need
// a text form should hex- or base64-encode the result themselves; the BYTEA
// columns store the raw bytes directly.
package secretbox

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/kms"
	"github.com/JuliusBrussee/caveman/shared/platform/runtimeenv"
)

// envKey is the name of the environment variable holding the base64-encoded
// 32-byte master key.
const envKey = "CAVE_LOCAL_ENCRYPTION_KEY"

// loadKey reads and validates the 32-byte AES key from the environment.
func loadKey() ([]byte, error) {
	keyB64 := os.Getenv(envKey)
	if keyB64 == "" {
		return nil, fmt.Errorf("%s is not set; cannot encrypt/decrypt secrets", envKey)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", envKey, err)
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("%s must decode to exactly 32 bytes, got %d", envKey, len(keyBytes))
	}
	return keyBytes, nil
}

// Encrypt seals plaintext with AES-256-GCM and a fresh random nonce, returning
// nonce(12) || ciphertext+tag as raw bytes.
func Encrypt(plaintext []byte) ([]byte, error) {
	if useKMS() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		wrapped, err := kms.Encrypt(ctx, plaintext)
		if err != nil {
			return nil, fmt.Errorf("secretbox: KMS encrypt: %w", err)
		}
		return wrapped, nil
	}
	if runtimeenv.IsProduction() {
		return nil, fmt.Errorf("secretbox: production requires CAVE_KMS_PROVIDER=scaleway")
	}
	keyBytes, err := loadKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce entropy: %w", err)
	}
	// Seal appends the ciphertext+tag to nonce, so the returned slice is the
	// full nonce||ciphertext envelope.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// EncryptPayloadKey wraps an artifact data-encryption key. Production uses the
// dedicated payload KEK; local development retains the same AES-GCM envelope as
// other local secrets.
func EncryptPayloadKey(plaintext []byte) ([]byte, error) {
	if useKMS() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		wrapped, err := kms.EncryptPayload(ctx, plaintext)
		if err != nil {
			return nil, fmt.Errorf("secretbox: payload KMS encrypt: %w", err)
		}
		return wrapped, nil
	}
	return Encrypt(plaintext)
}

// Decrypt reverses Encrypt: it expects nonce(12) || ciphertext+tag.
func Decrypt(envelope []byte) ([]byte, error) {
	if kms.IsEnvelope(envelope) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		plaintext, err := kms.Decrypt(ctx, envelope)
		if err != nil {
			return nil, fmt.Errorf("secretbox: KMS decrypt: %w", err)
		}
		return plaintext, nil
	}
	if runtimeenv.IsProduction() &&
		!strings.EqualFold(strings.TrimSpace(os.Getenv("CAVE_KMS_ALLOW_LEGACY_LOCAL_DECRYPT")), "true") {
		return nil, fmt.Errorf("secretbox: production refuses legacy local ciphertext")
	}
	keyBytes, err := loadKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(envelope) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := envelope[:ns], envelope[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm open: %w", err)
	}
	return plain, nil
}

// DecryptPayloadKey unwraps an artifact data-encryption key. KMS envelopes are
// restricted to the configured payload key plus the explicit legacy secrets
// key used before key separation.
func DecryptPayloadKey(envelope []byte) ([]byte, error) {
	if kms.IsEnvelope(envelope) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		plaintext, err := kms.DecryptPayload(ctx, envelope)
		if err != nil {
			return nil, fmt.Errorf("secretbox: payload KMS decrypt: %w", err)
		}
		return plaintext, nil
	}
	return Decrypt(envelope)
}

func useKMS() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("CAVE_KMS_PROVIDER")), kms.ProviderScaleway)
}

// ResolveEnvironmentSecret loads a boot-time secret. In production plaintext
// environment variables are rejected: operators must provide a base64-encoded
// secretbox/KMS envelope in ciphertextEnv. Local development may continue using
// plaintextEnv. An entirely absent optional secret returns an empty string.
func ResolveEnvironmentSecret(plaintextEnv, ciphertextEnv string) (string, error) {
	plain := strings.TrimSpace(os.Getenv(plaintextEnv))
	encoded := strings.TrimSpace(os.Getenv(ciphertextEnv))
	production := runtimeenv.IsProduction()
	if production && plain != "" {
		return "", fmt.Errorf("secretbox: production refuses plaintext %s; use %s", plaintextEnv, ciphertextEnv)
	}
	if encoded == "" {
		return plain, nil
	}
	wrapped, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secretbox: %s is not valid base64", ciphertextEnv)
	}
	decrypted, err := Decrypt(wrapped)
	if err != nil {
		return "", fmt.Errorf("secretbox: decrypt %s: %w", ciphertextEnv, err)
	}
	if len(decrypted) == 0 {
		return "", fmt.Errorf("secretbox: %s decrypted to an empty secret", ciphertextEnv)
	}
	return string(decrypted), nil
}
