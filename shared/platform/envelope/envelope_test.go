package envelope

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func setKey(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*5 + 1)
	}
	t.Setenv("CAVE_LOCAL_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
}

func TestSealOpenRoundTrip(t *testing.T) {
	setKey(t)
	plain := []byte("artifact body: a large model response with tool calls")
	ct, meta, err := Seal(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := Open(ct, meta)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestSealUsesFreshDataKeyPerObject(t *testing.T) {
	setKey(t)
	_, m1, _ := Seal([]byte("x"))
	_, m2, _ := Seal([]byte("x"))
	var a, b Metadata
	_ = json.Unmarshal(m1, &a)
	_ = json.Unmarshal(m2, &b)
	if a.WrappedDataKey == b.WrappedDataKey {
		t.Fatal("expected a fresh wrapped data key per object")
	}
}

func TestOpenUnknownSchemeFailsClosed(t *testing.T) {
	setKey(t)
	ct, _, _ := Seal([]byte("x"))
	bad, _ := json.Marshal(Metadata{Scheme: "rot13", WrappedDataKey: "AAAA"})
	if _, err := Open(ct, bad); err == nil {
		t.Fatal("expected unknown scheme to fail closed")
	}
}

func TestOpenTamperedCiphertextFails(t *testing.T) {
	setKey(t)
	ct, meta, _ := Seal([]byte("hello world"))
	ct[len(ct)-1] ^= 0xff
	if _, err := Open(ct, meta); err == nil {
		t.Fatal("expected GCM auth failure on tampered ciphertext")
	}
}

func TestScopedEnvelopeRejectsDifferentTenant(t *testing.T) {
	setKey(t)
	scope := Scope{OrganizationID: "org-a", ProjectID: "project-a", Kind: "capture"}
	ciphertext, metadata, err := SealForScope([]byte("tenant payload"), scope)
	if err != nil {
		t.Fatalf("seal scoped envelope: %v", err)
	}
	if _, err := OpenForScope(ciphertext, metadata, Scope{
		OrganizationID: "org-b",
		ProjectID:      scope.ProjectID,
		Kind:           scope.Kind,
	}); err == nil {
		t.Fatal("scoped envelope opened for another tenant")
	}
}

func TestScopedOpenReadsLegacyEnvelopeDuringMigration(t *testing.T) {
	setKey(t)
	plain := []byte("legacy tenant payload")
	ciphertext, metadata, err := Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenForScope(ciphertext, metadata, Scope{
		OrganizationID: "org-a",
		ProjectID:      "project-a",
		Kind:           "artifact",
	})
	if err != nil {
		t.Fatalf("open legacy envelope: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("legacy envelope round-trip mismatch")
	}
}
