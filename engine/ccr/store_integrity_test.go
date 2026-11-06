//go:build !js

package ccr

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// damagedStore builds a store whose recoveries b-tree has the damage #1149
// and #1369 report: two interior cells name the same child page, so a page has
// two parents, rowids run out of order and one leaf is unreachable. typed
// objects stay intact. It returns the handles written, in insert order.
func damagedStore(t *testing.T, path string, mode string) []string {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var handles []string
	for i := 0; i < 60; i++ {
		original := []byte(fmt.Sprintf("%04d %s", i, strings.Repeat("recovery payload ", 60)))
		handle, err := store.Put(Recovery{ContentType: "text", Compressor: "test", TokensBefore: 10, TokensAfter: 5, Original: original})
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
	}
	if _, err := store.PutObject(Object{Type: ObjectFileObservation, SessionID: "intact", Source: "test", Data: []byte("intact object")}); err != nil {
		t.Fatal(err)
	}
	var root, pageSize int64
	if err := store.db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = 'recoveries'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	page := make([]byte, pageSize)
	if _, err := file.ReadAt(page, (root-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	if page[0] != 0x05 {
		t.Fatalf("recoveries root page type %#x, want an interior table page", page[0])
	}
	if cells := binary.BigEndian.Uint16(page[3:5]); cells < 2 {
		t.Fatalf("recoveries root has %d cells", cells)
	}
	first := binary.BigEndian.Uint16(page[12:14])
	second := binary.BigEndian.Uint16(page[14:16])
	switch mode {
	case "double-parent":
		copy(page[first:first+4], page[second:second+4])
		if _, err := file.WriteAt(page, (root-1)*pageSize); err != nil {
			t.Fatal(err)
		}
	case "bad-leaf":
		child := int64(binary.BigEndian.Uint32(page[first : first+4]))
		if _, err := file.WriteAt([]byte{0x00}, (child-1)*pageSize); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown damage mode %q", mode)
	}
	return handles
}

func TestDamagedStoreRefusesWritesAfterIntegrityCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	handles := damagedStore(t, path, "double-parent")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	report, err := store.CheckIntegrity()
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != IntegrityDamaged || len(report.Findings) == 0 {
		t.Fatalf("check = %+v, want damaged findings", report)
	}
	_, err = store.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("must not land in a damaged tree")})
	if !errors.Is(err, ErrStoreDamaged) {
		t.Fatalf("put into a damaged store = %v, want ErrStoreDamaged", err)
	}
	_, err = store.PutObject(Object{Type: ObjectFileObservation, SessionID: "s", Source: "test", Data: []byte("x")})
	if !errors.Is(err, ErrStoreDamaged) {
		t.Fatalf("object put into a damaged store = %v, want ErrStoreDamaged", err)
	}
	// Reads go on: the last handle sits in a leaf the damage did not orphan.
	last := handles[len(handles)-1]
	if _, err := store.Get(last); err != nil {
		t.Fatalf("read from a damaged store: %v", err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Integrity != IntegrityDamaged || stats.IntegrityDetail == "" {
		t.Fatalf("summary integrity = %q (%q), want damaged", stats.Integrity, stats.IntegrityDetail)
	}
}

func TestSQLiteCorruptionMidOperationStopsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	handles := damagedStore(t, path, "bad-leaf")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var sawCorrupt bool
	for _, handle := range handles {
		if _, err := store.Get(handle); err != nil && isCorrupt(err) {
			sawCorrupt = true
			break
		}
	}
	if !sawCorrupt {
		t.Fatal("no read reached the damaged leaf")
	}
	_, err = store.PutObject(Object{Type: ObjectFileObservation, SessionID: "s", Source: "test", Data: []byte("x")})
	if !errors.Is(err, ErrStoreDamaged) {
		t.Fatalf("put after SQLite reported damage = %v, want ErrStoreDamaged", err)
	}
}

func TestReclaimNeverWritesToADamagedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	damagedStore(t, path, "double-parent")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.refused.Store(refusalPageCap)
	freed := store.reclaimForWrite()
	store.mu.Unlock()
	if freed {
		t.Fatal("reclaim reported room in a damaged store")
	}
	if store.integrity.Load() != integrityDamaged {
		t.Fatal("reclaim did not run its integrity check")
	}
	var walFrames int64
	if info, err := os.Stat(path + "-wal"); err == nil {
		walFrames = info.Size()
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || walFrames > 0 {
		t.Fatalf("reclaim wrote to a damaged store (wal %d bytes)", walFrames)
	}
}

func TestCheckIntegrityOnceRecordsAPassPerBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store := openTestStore(t, path)
	first, err := store.CheckIntegrityOnce("bin-v9.9.9")
	if err != nil || first.Result != IntegrityOK || first.Skipped {
		t.Fatalf("first check = %+v, %v", first, err)
	}
	again, err := store.CheckIntegrityOnce("bin-v9.9.9")
	if err != nil || !again.Skipped || again.Result != IntegrityOK {
		t.Fatalf("same build checked again: %+v, %v", again, err)
	}
	upgraded, err := store.CheckIntegrityOnce("bin-v9.9.10")
	if err != nil || upgraded.Skipped {
		t.Fatalf("a new build skipped its check: %+v, %v", upgraded, err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Integrity != IntegrityOK {
		t.Fatalf("summary integrity = %q, want ok", stats.Integrity)
	}
}

func TestCheckIntegrityOnceNeverRecordsADamagedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	damagedStore(t, path, "double-parent")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	report, err := store.CheckIntegrityOnce("bin-v9.9.9")
	if err != nil || report.Result != IntegrityDamaged {
		t.Fatalf("check = %+v, %v", report, err)
	}
	raw, err := withStore(store, func() (string, error) { return store.metaValue(store.db, metaIntegrityCheck) })
	if err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Fatalf("a damaged store recorded a check: %s", raw)
	}
}
