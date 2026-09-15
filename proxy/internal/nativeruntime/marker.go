package nativeruntime

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const sessionKeyBytes = 32

var markerPattern = regexp.MustCompile(`\[\[caveman-session-v1 sid="([A-Za-z0-9_-]{1,384})" sig="([0-9a-f]{64})"\]\]`)

// LoadOrCreateSessionKey returns one user-only HMAC key shared by CLI adapters
// and local proxy. O_EXCL makes concurrent first startup converge on one key.
func LoadOrCreateSessionKey(home string) ([]byte, error) {
	dir := filepath.Join(home, "runtime")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("native session key mkdir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("native session key chmod dir: %w", err)
	}
	path := filepath.Join(dir, "session.key")
	key := make([]byte, sessionKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("native session key random: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.Write(key); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("native session key write: %w", writeErr)
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("native session key sync: %w", syncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("native session key close: %w", closeErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("native session key create: %w", err)
	}
	key, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("native session key read: %w", err)
	}
	if len(key) != sessionKeyBytes {
		return nil, fmt.Errorf("native session key length = %d, want %d", len(key), sessionKeyBytes)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("native session key chmod: %w", err)
	}
	return key, nil
}

// SessionMarker builds model-temporary correlation context. Local proxy removes
// valid markers byte-surgically before provider inspection or forwarding.
func SessionMarker(key []byte, sessionID string) (string, error) {
	if len(key) != sessionKeyBytes || sessionID == "" || len(sessionID) > 256 {
		return "", errors.New("native session marker: invalid key or session id")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(sessionID))
	sig := markerMAC(key, encoded)
	return fmt.Sprintf(`[[caveman-session-v1 sid="%s" sig="%s"]]`, encoded, sig), nil
}

// StripSessionMarkers removes only valid HMAC-signed markers. Invalid marker-
// shaped user text remains byte-identical. Conflicting valid session IDs are
// stripped but return no correlation identity.
func StripSessionMarkers(body, key []byte) (stripped []byte, sessionID string, changed bool) {
	if len(key) != sessionKeyBytes {
		return body, "", false
	}
	matches := markerPattern.FindAllSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return body, "", false
	}
	out := make([]byte, 0, len(body))
	last := 0
	conflict := false
	for _, match := range matches {
		encoded := string(body[match[2]:match[3]])
		signature := string(body[match[4]:match[5]])
		expected, decodeErr := hex.DecodeString(markerMAC(key, encoded))
		actual, signatureErr := hex.DecodeString(signature)
		decoded, sidErr := base64.RawURLEncoding.DecodeString(encoded)
		if decodeErr != nil || signatureErr != nil || sidErr != nil || len(decoded) == 0 || len(decoded) > 256 || !hmac.Equal(actual, expected) {
			continue
		}
		start := match[0]
		if start >= 2 && bytes.Equal(body[start-2:start], []byte(`\n`)) {
			start -= 2
		} else if start > 0 && (body[start-1] == '\n' || body[start-1] == ' ') {
			start--
		}
		out = append(out, body[last:start]...)
		last = match[1]
		changed = true
		candidate := string(decoded)
		if sessionID == "" {
			sessionID = candidate
		} else if sessionID != candidate {
			conflict = true
		}
	}
	if !changed {
		return body, "", false
	}
	out = append(out, body[last:]...)
	if conflict {
		sessionID = ""
	}
	return out, sessionID, true
}

func markerMAC(key []byte, encodedSessionID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("caveman-session-v1\x00" + encodedSessionID))
	return hex.EncodeToString(mac.Sum(nil))
}
