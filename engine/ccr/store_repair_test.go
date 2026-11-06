//go:build !js

package ccr

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepairRebuildsDamagedStoreAndKeepsEveryVerifiedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	handles := damagedStore(t, path, "double-parent")
	// A row whose bytes no longer match its handle, as in #1369.
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wrong := handles[len(handles)-1]
	if _, err := store.db.Exec(`UPDATE recoveries SET original = ? WHERE handle = ?`, []byte("not the original"), wrong); err != nil {
		t.Fatal(err)
	}
	var objectID string
	if err := store.db.QueryRow(`SELECT object_id FROM typed_objects`).Scan(&objectID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	damagedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	report, err := RepairStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != "repaired" || len(report.Findings) == 0 || !report.RestartNeeded {
		t.Fatalf("report = %+v", report)
	}
	// The damaged file is kept, byte for byte, under the backup name.
	backup, err := os.ReadFile(report.BackupPath)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !bytes.Equal(backup, damagedBytes) {
		t.Fatal("backup differs from the damaged file")
	}
	// Every handle is accounted for: kept, dropped for a mismatch, or listed
	// as unreadable.
	r := report.Recoveries
	if r.Kept+r.Dropped+r.Unreadable != len(handles) {
		t.Fatalf("recoveries %+v do not account for %d handles", r, len(handles))
	}
	if r.Dropped != 1 || r.DroppedIDs[0] != wrong {
		t.Fatalf("dropped = %v, want only %s", r.DroppedIDs, wrong)
	}
	if report.TypedObjects.Kept != 1 {
		t.Fatalf("typed objects %+v", report.TypedObjects)
	}

	repaired, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repaired.Close()
	check, err := repaired.CheckIntegrity()
	if err != nil || check.Result != IntegrityOK {
		t.Fatalf("repaired store check = %+v, %v", check, err)
	}
	unreadable := map[string]bool{}
	for _, h := range r.UnreadableIDs {
		unreadable[h] = true
	}
	for _, h := range handles {
		got, err := repaired.Get(h)
		switch {
		case h == wrong || unreadable[h]:
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s: %v, want ErrNotFound", h, err)
			}
		case err != nil || Handle(got) != h:
			t.Fatalf("%s lost in repair: %v", h, err)
		}
	}
	if _, err := repaired.GetObject(objectID); err != nil {
		t.Fatalf("intact typed object lost in repair: %v", err)
	}
	if _, err := repaired.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("after repair")}); err != nil {
		t.Fatalf("write after repair: %v", err)
	}
}

func TestRepairRefusesAStoreAnotherConnectionHolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	damagedStore(t, path, "double-parent")
	holder, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RepairStore(path, false); !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("repair while open = %v, want ErrStoreInUse", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a refused repair changed the store")
	}
	matches, _ := filepath.Glob(path + ".*")
	if len(matches) != 0 {
		t.Fatalf("a refused repair left files: %v", matches)
	}
}

func TestRepairLeavesAnIntactStoreAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("kept")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := RepairStore(path, false)
	if err != nil || report.Result != "intact" || report.BackupPath != "" {
		t.Fatalf("report = %+v, %v", report, err)
	}
	matches, _ := filepath.Glob(path + ".*")
	if len(matches) != 0 {
		t.Fatalf("an intact store gained files: %v", matches)
	}
	// --force rebuilds anyway and keeps the original as a backup.
	report, err = RepairStore(path, true)
	if err != nil || report.Result != "repaired" || !strings.Contains(report.BackupPath, ".before-rebuild-") {
		t.Fatalf("forced report = %+v, %v", report, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Get(handle); err != nil || string(got) != "kept" {
		t.Fatalf("forced rebuild lost a row: %v", err)
	}
}

func TestRepairResolvesReferencesThroughAnyVerifiedCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("map"), 1000)
	lost := bytes.Repeat([]byte("other"), 1000)
	now := testNow()
	mustInsertObjectRow(t, store.db, "ccr_obj_owner_bad", ObjectRepositoryMap, "a", Warm, now, data, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_owner_good", ObjectRepositoryMap, "b", Warm, now, data, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_ref", ObjectRepositoryMap, "c", Warm, now, data, "ccr_obj_owner_bad")
	mustInsertObjectRow(t, store.db, "ccr_obj_lost_owner", ObjectRepositoryMap, "d", Warm, now, lost, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_lost_ref", ObjectRepositoryMap, "e", Warm, now, lost, "ccr_obj_lost_owner")
	for _, id := range []string{"ccr_obj_owner_bad", "ccr_obj_lost_owner"} {
		if _, err := store.db.Exec(`UPDATE typed_objects SET data = ? WHERE object_id = ?`, []byte("damaged"), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := RepairStore(path, true)
	if err != nil {
		t.Fatal(err)
	}
	o := report.TypedObjects
	if o.Kept != 2 || o.Dropped != 3 {
		t.Fatalf("typed objects %+v, want 2 kept (good owner, re-pointed ref) and 3 dropped", o)
	}
	repaired := openTestStore(t, path)
	got, err := repaired.GetObject("ccr_obj_ref")
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("reference whose owner was damaged: %v", err)
	}
	if _, err := repaired.GetObject("ccr_obj_lost_ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reference with no verified copy left: %v, want ErrNotFound", err)
	}
}

func TestInspectStoreNeverCreatesOrWrites(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "ccr.db")
	report, err := InspectStore(missing, DefaultMaxStorageBytes, true)
	if err != nil || report.Exists {
		t.Fatalf("missing store: %+v, %v", report, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspect created the store")
	}
	path := filepath.Join(dir, "damaged.db")
	damagedStore(t, path, "double-parent")
	before, _ := os.ReadFile(path)
	report, err = InspectStore(path, DefaultMaxStorageBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Exists || report.Integrity != IntegrityDamaged || len(report.IntegrityFindings) == 0 {
		t.Fatalf("damaged store report = %+v", report)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("inspect wrote to the store")
	}
}

func TestInspectStoreReportsAFullStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	const budget = 256 << 10
	store, err := OpenWithBudget(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	fillWithObjects(t, store.db, "fresh", "s", Hot, testNow(), 6000)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := InspectStore(path, budget, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Full || report.FullReason != StorageFullPageCap || report.Integrity != "" {
		t.Fatalf("report = %+v, want full at the page cap and no integrity verdict", report)
	}
}

func testNow() time.Time { return time.Now().UTC() }
