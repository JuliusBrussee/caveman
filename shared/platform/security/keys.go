package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordBytes = 12
	MaxPasswordBytes = 1024
)

func ValidatePassword(password string) error {
	if len(password) < MinPasswordBytes {
		return fmt.Errorf("password must be at least %d bytes", MinPasswordBytes)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	return nil
}

func GenerateProjectKey() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	full := "cave_live_" + secret[:12] + "_" + secret[12:]
	return full, secret[:12], nil
}

func HashProjectKey(pepper, full string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(full))
	return hex.EncodeToString(mac.Sum(nil))
}

func ParseProjectKey(value string) (string, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "Bearer ")
	rest, ok := strings.CutPrefix(value, "cave_live_")
	if !ok {
		return "", false
	}
	// The prefix is the first 12 base64url characters of the secret, which may
	// themselves contain '_' — parse by position, never by splitting on '_'
	// (splitting rejected ~17% of honestly minted keys: any whose prefix drew
	// an underscore).
	if len(rest) < 14 || rest[12] != '_' {
		return "", false
	}
	return rest[:12], true
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

func CheckPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return dummyPasswordCheck(password)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return dummyPasswordCheck(password)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return dummyPasswordCheck(password)
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, uint32(len(want)))
	return hmac.Equal(got, want)
}

func dummyPasswordCheck(password string) bool {
	// Fixed non-secret salt + impossible comparison give unknown users the same
	// Argon2 work factor as known users without maintaining a reusable password.
	salt := []byte("cave-login-dummy")
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return hmac.Equal(got, make([]byte, len(got)))
}

func HMACSHA256(secret, data string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
