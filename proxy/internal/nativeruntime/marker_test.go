package nativeruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionMarkerStripsValidBytesAndPreservesEverythingElse(t *testing.T) {
	key := bytes.Repeat([]byte{7}, sessionKeyBytes)
	marker, err := SessionMarker(key, "claude:host-42")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"system":"Core\n` + marker + `","messages":[{"role":"user","content":"keep exact ✓"}]}`)
	want := []byte(`{"system":"Core","messages":[{"role":"user","content":"keep exact ✓"}]}`)
	got, sessionID, changed := StripSessionMarkers(body, key)
	if !changed || sessionID != "claude:host-42" || !bytes.Equal(got, want) {
		t.Fatalf("strip mismatch: changed=%v session=%q\ngot  %s\nwant %s", changed, sessionID, got, want)
	}
}

func TestSessionMarkerIgnoresInvalidSignatureAndConflictingIDsFailCorrelation(t *testing.T) {
	key := bytes.Repeat([]byte{9}, sessionKeyBytes)
	invalid := []byte(`{"system":"[[caveman-session-v1 sid="Y2xhdWRlOnMx" sig="` + strings.Repeat("0", 64) + `"]]"}`)
	got, sessionID, changed := StripSessionMarkers(invalid, key)
	if changed || sessionID != "" || !bytes.Equal(got, invalid) {
		t.Fatal("invalid marker must remain byte-identical")
	}
	first, _ := SessionMarker(key, "claude:s1")
	second, _ := SessionMarker(key, "claude:s2")
	body := []byte(`{"system":"` + first + ` ` + second + `"}`)
	got, sessionID, changed = StripSessionMarkers(body, key)
	if !changed || sessionID != "" || bytes.Contains(got, []byte("caveman-session")) {
		t.Fatalf("conflicting valid markers must strip but not correlate: changed=%v session=%q body=%s", changed, sessionID, got)
	}
}

// TestSessionMarkerMatchesJSONEscapedMarkerInRealRequestBody exercises the
// shape a real HTTP body actually has: the marker sits inside a JSON string,
// so encoding/json escapes every quote as \". The regex previously only
// matched the literal, unescaped form, so this never matched in production.
func TestSessionMarkerMatchesJSONEscapedMarkerInRealRequestBody(t *testing.T) {
	key := bytes.Repeat([]byte{3}, sessionKeyBytes)
	marker, err := SessionMarker(key, "claude:host-99")
	if err != nil {
		t.Fatal(err)
	}
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	type request struct {
		Messages []message `json:"messages"`
	}
	body, err := json.Marshal(request{Messages: []message{{
		Role:    "user",
		Content: []block{{Type: "text", Text: "Do the task.\n" + marker}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`sid=\"`)) {
		t.Fatalf("test body does not actually contain an escaped marker: %s", body)
	}

	stripped, sessionID, changed := StripSessionMarkers(body, key)
	if !changed || sessionID != "claude:host-99" {
		t.Fatalf("escaped marker not stripped: changed=%v session=%q body=%s", changed, sessionID, stripped)
	}
	if bytes.Contains(stripped, []byte("caveman-session-v1")) {
		t.Fatalf("marker survived stripping: %s", stripped)
	}
	var decoded request
	if err := json.Unmarshal(stripped, &decoded); err != nil {
		t.Fatalf("stripped body is no longer valid JSON: %v\nbody: %s", err, stripped)
	}
	if decoded.Messages[0].Content[0].Text != "Do the task." {
		t.Fatalf("stripped text wrong: %q", decoded.Messages[0].Content[0].Text)
	}
}

func TestLoadOrCreateSessionKeyIsStableAndUserOnly(t *testing.T) {
	home := t.TempDir()
	first, err := LoadOrCreateSessionKey(home)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateSessionKey(home)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) != sessionKeyBytes {
		t.Fatal("session key did not remain stable")
	}
	info, err := os.Stat(filepath.Join(home, "runtime", "session.key"))
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("session key permissions = %o, want 600", info.Mode().Perm())
	}
}
