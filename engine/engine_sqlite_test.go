//go:build !js

package engine_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	_ "modernc.org/sqlite"
)

func TestRetrieveDoesNotFallThroughOnCorruptRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handle, err := store.Put(ccr.Recovery{Original: []byte("exact original bytes"), ContentType: "text"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE recoveries SET original = ? WHERE handle = ?`, []byte("damaged"), handle); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(store, nil)
	_, err = eng.Retrieve(handle)
	if !errors.Is(err, ccr.ErrCorruptRecovery) {
		t.Fatalf("Retrieve err = %v, want ErrCorruptRecovery", err)
	}
	if errors.Is(err, ccr.ErrNotFound) {
		t.Fatal("corrupt recovery must not read as not found")
	}
	if _, err := eng.RetrieveQuery(handle, "bytes"); !errors.Is(err, ccr.ErrCorruptRecovery) {
		t.Fatalf("RetrieveQuery err = %v, want ErrCorruptRecovery", err)
	}
}
