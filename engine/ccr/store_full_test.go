//go:build !js

package ccr

import (
	"bytes"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPutObjectReturnsStorageFullInsteadOfMintingHandle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var pages int
	if err := store.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA max_page_count = ` + strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(Object{
		Type: ObjectFileObservation, SessionID: "storage-full", Source: "test",
		Data: bytes.Repeat([]byte("exact-context"), 1024*1024),
	}); err == nil {
		t.Fatal("full CCR storage accepted object and could expose a false recovery handle")
	}
}
