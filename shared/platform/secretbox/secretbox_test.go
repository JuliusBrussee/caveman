package secretbox

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func setKey(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	t.Setenv(envKey, base64.StdEncoding.EncodeToString(key))
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	setKey(t)
	plain := []byte("whsec_super_secret_value")
	env, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(env, plain) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := Decrypt(env)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	setKey(t)
	a, _ := Encrypt([]byte("x"))
	b, _ := Encrypt([]byte("x"))
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce reuse)")
	}
}

func TestDecryptRejectsTamper(t *testing.T) {
	setKey(t)
	env, _ := Encrypt([]byte("hello"))
	env[len(env)-1] ^= 0xff // flip a tag bit
	if _, err := Decrypt(env); err == nil {
		t.Fatal("expected GCM auth failure on tampered ciphertext")
	}
}

func TestMissingKey(t *testing.T) {
	t.Setenv(envKey, "")
	if _, err := Encrypt([]byte("x")); err == nil {
		t.Fatal("expected error when key is unset")
	}
}

func TestKeyValidationRejectsMalformedAndWrongLengthKeys(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	for _, encoded := range []string{
		"not-base64",
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
	} {
		t.Setenv(envKey, encoded)
		if _, err := Encrypt([]byte("secret")); err == nil {
			t.Fatalf("invalid key %q accepted", encoded)
		}
	}
}

func TestDecryptRejectsShortCiphertext(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	setKey(t)
	if _, err := Decrypt([]byte("short")); err == nil {
		t.Fatal("short ciphertext accepted")
	}
}

func TestPayloadKeyLocalRoundTrip(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	setKey(t)
	plain := []byte("32-byte artifact data encryption key")
	wrapped, err := EncryptPayloadKey(plain)
	if err != nil {
		t.Fatalf("EncryptPayloadKey: %v", err)
	}
	got, err := DecryptPayloadKey(wrapped)
	if err != nil {
		t.Fatalf("DecryptPayloadKey: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("payload key round-trip = %q, want %q", got, plain)
	}
}

func TestKMSErrorsAreWrappedWithoutLocalFallback(t *testing.T) {
	t.Setenv("CAVE_KMS_PROVIDER", " scaleway ")
	t.Setenv("CAVE_KMS_REGION", "")
	t.Setenv("SCW_DEFAULT_REGION", "")
	t.Setenv("KMS_SECRETS_KEY_ARN", "")
	t.Setenv("KMS_PAYLOADS_KEY_ARN", "")
	t.Setenv("CAVE_KMS_AUTH_TOKEN", "")
	t.Setenv("SCW_SECRET_KEY", "")

	if _, err := Encrypt([]byte("secret")); err == nil {
		t.Fatal("KMS encrypt configuration failure fell back to local encryption")
	}
	if _, err := EncryptPayloadKey([]byte("secret")); err == nil {
		t.Fatal("payload KMS configuration failure fell back to local encryption")
	}
	envelope := []byte(`cave-kms-v1:{}`)
	if _, err := Decrypt(envelope); err == nil {
		t.Fatal("KMS decrypt configuration failure fell back to local decryption")
	}
	if _, err := DecryptPayloadKey(envelope); err == nil {
		t.Fatal("payload KMS decrypt configuration failure fell back to local decryption")
	}
}

func TestProductionRefusesLocalEncryption(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	t.Setenv(envKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := Encrypt([]byte("secret")); err == nil {
		t.Fatal("production accepted local master-key encryption")
	}
}

func TestProductionRefusesLegacyLocalDecryptByDefault(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	setKey(t)
	ciphertext, err := Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_ENV", "prod")
	if _, err := Decrypt(ciphertext); err == nil {
		t.Fatal("production accepted legacy local ciphertext without migration flag")
	}
}

func TestProductionLegacyLocalDecryptRequiresExplicitMigrationFlag(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	setKey(t)
	ciphertext, err := Encrypt([]byte("legacy secret"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CAVE_KMS_ALLOW_LEGACY_LOCAL_DECRYPT", " TRUE ")
	got, err := Decrypt(ciphertext)
	if err != nil || string(got) != "legacy secret" {
		t.Fatalf("explicit legacy decrypt = %q, %v", got, err)
	}
}

func TestResolveEnvironmentSecretLocalPlaintext(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("TEST_PRIVATE_KEY", "local-value")
	got, err := ResolveEnvironmentSecret("TEST_PRIVATE_KEY", "TEST_PRIVATE_KEY_CIPHERTEXT")
	if err != nil || got != "local-value" {
		t.Fatalf("ResolveEnvironmentSecret() = %q, %v", got, err)
	}
}

func TestResolveEnvironmentSecretProductionRejectsPlaintext(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("TEST_PRIVATE_KEY", "must-not-load")
	if _, err := ResolveEnvironmentSecret("TEST_PRIVATE_KEY", "TEST_PRIVATE_KEY_CIPHERTEXT"); err == nil {
		t.Fatal("production accepted a plaintext boot secret")
	}
}

func TestResolveEnvironmentSecretRejectsMalformedCiphertext(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("TEST_PRIVATE_KEY_CIPHERTEXT", "not-base64")
	if _, err := ResolveEnvironmentSecret("TEST_PRIVATE_KEY", "TEST_PRIVATE_KEY_CIPHERTEXT"); err == nil {
		t.Fatal("malformed ciphertext was accepted")
	}
}

func TestResolveEnvironmentSecretCiphertextAndOptionalCases(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CAVE_KMS_PROVIDER", "")
	setKey(t)

	got, err := ResolveEnvironmentSecret("OPTIONAL_PLAIN", "OPTIONAL_CIPHERTEXT")
	if err != nil || got != "" {
		t.Fatalf("absent optional secret = %q, %v", got, err)
	}

	wrapped, err := Encrypt([]byte("ciphertext-value"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPTIONAL_PLAIN", "plaintext-must-not-win")
	t.Setenv("OPTIONAL_CIPHERTEXT", base64.StdEncoding.EncodeToString(wrapped))
	got, err = ResolveEnvironmentSecret("OPTIONAL_PLAIN", "OPTIONAL_CIPHERTEXT")
	if err != nil || got != "ciphertext-value" {
		t.Fatalf("ciphertext secret = %q, %v", got, err)
	}

	empty, err := Encrypt(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPTIONAL_CIPHERTEXT", base64.StdEncoding.EncodeToString(empty))
	if _, err := ResolveEnvironmentSecret("MISSING_PLAIN", "OPTIONAL_CIPHERTEXT"); err == nil {
		t.Fatal("empty decrypted secret accepted")
	}
}
