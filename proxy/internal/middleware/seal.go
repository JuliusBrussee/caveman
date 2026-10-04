package middleware

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Keyring seals middleware originals at rest with AES-256-GCM. The first key
// seals; every key opens, so an operator rotates by putting a new key first and
// dropping the old one once max_retention_seconds has passed. Each row stores
// its key ID, the first 16 hex characters of SHA-256(key). The associated data
// binds a ciphertext to its authority and digest, so a row copied under another
// authority does not open.
//
// The format is secretbox's (nonce(12) || ciphertext+tag). secretbox itself is
// not used: it reads exactly one key from CAVE_LOCAL_ENCRYPTION_KEY, has no key
// IDs to rotate with, and refuses local keys when CAVE_ENV=prod.
//
// A nil Keyring stores originals in plaintext (key ID ""). Once a key is
// configured, plaintext rows are refused unless plaintext is set (the operator
// is migrating a store written before encryption); sealed rows need their key.
type Keyring struct {
	active    string
	keys      map[string]cipher.AEAD
	plaintext bool
}

var errPlaintextRefused = errors.New("middleware original stored in plaintext is refused while a key is configured")

// LoadKeyring parses base64 32-byte keys from inline (comma or newline
// separated) or, when inline is empty, from the file at path. Both empty means
// no encryption.
func LoadKeyring(inline, path string) (*Keyring, error) {
	raw := inline
	if raw == "" && path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("middleware encryption key file: %w", err)
		}
		raw = string(b)
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
	if len(fields) == 0 {
		if inline != "" || path != "" {
			return nil, errors.New("middleware encryption key: no key found")
		}
		return nil, nil
	}
	k := &Keyring{keys: map[string]cipher.AEAD{}}
	for i, field := range fields {
		key, err := base64.StdEncoding.DecodeString(field)
		if err != nil || len(key) != 32 {
			// Never echo the value: this error reaches the proxy log.
			return nil, fmt.Errorf("middleware encryption key %d must be base64 of exactly 32 bytes", i+1)
		}
		block, _ := aes.NewCipher(key)
		aead, _ := cipher.NewGCM(block)
		sum := sha256.Sum256(key)
		id := hex.EncodeToString(sum[:8])
		if i == 0 {
			k.active = id
		}
		k.keys[id] = aead
	}
	return k, nil
}

func sealingData(authority, digest string) []byte { return []byte(authority + "\x00" + digest) }

func (k *Keyring) seal(authority, digest string, plaintext []byte) ([]byte, string, error) {
	if k == nil {
		return plaintext, "", nil
	}
	aead := k.keys[k.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return aead.Seal(nonce, nonce, plaintext, sealingData(authority, digest)), k.active, nil
}

// usable reports whether open can open an original sealed with keyID.
func (k *Keyring) usable(keyID string) bool {
	if keyID == "" {
		return k == nil || k.plaintext
	}
	return k != nil && k.keys[keyID] != nil
}

func (k *Keyring) open(authority, digest string, body []byte, keyID string) ([]byte, error) {
	if keyID == "" {
		if !k.usable("") {
			return nil, errPlaintextRefused
		}
		return body, nil
	}
	var aead cipher.AEAD
	if k != nil {
		aead = k.keys[keyID]
	}
	if aead == nil || len(body) < aead.NonceSize() {
		return nil, fmt.Errorf("middleware original sealed with unavailable key %s", keyID)
	}
	return aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], sealingData(authority, digest))
}
