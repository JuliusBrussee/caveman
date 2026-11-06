package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// damageRecoveryStore writes a store at path and gives its recoveries b-tree a
// page with two parents, the damage #1369 reports.
func damageRecoveryStore(t *testing.T, path string) {
	t.Helper()
	store, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if _, err := store.Put(ccr.Recovery{ContentType: "text", Compressor: "test", Original: []byte(fmt.Sprintf("%04d %s", i, strings.Repeat("payload ", 120)))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var root, pageSize int64
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = 'recoveries'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
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
		t.Fatalf("recoveries root page type %#x", page[0])
	}
	first, second := binary.BigEndian.Uint16(page[12:14]), binary.BigEndian.Uint16(page[14:16])
	copy(page[first:first+4], page[second:second+4])
	if _, err := file.WriteAt(page, (root-1)*pageSize); err != nil {
		t.Fatal(err)
	}
}

func readRecoveryStoreStatus(t *testing.T) recoveryStoreReport {
	t.Helper()
	out := captureStdout(t, func() { runRecoveryStore(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), []string{"status"}) })
	var report recoveryStoreReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status output %q: %v", out, err)
	}
	if report.Schema != recoveryStoreSchema {
		t.Fatalf("schema = %q", report.Schema)
	}
	return report
}

func TestRecoveryStoreStatusOnAHealthyAndAMissingStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CCR_DB", "")
	report := readRecoveryStoreStatus(t)
	if report.Exists || report.WritesRefused {
		t.Fatalf("missing store: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(home, "ccr.db")); !os.IsNotExist(err) {
		t.Fatal("status created the store")
	}
	store, err := ccr.Open(filepath.Join(home, "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ccr.Recovery{ContentType: "text", Compressor: "test", Original: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	report = readRecoveryStoreStatus(t)
	if !report.Exists || report.WritesRefused || report.Reason != "" {
		t.Fatalf("healthy store: %+v", report)
	}
}

// The serve-time check finds the damage once; status then reports it on
// every run without reading the whole file, and a repair clears it.
func TestDamageFoundAtServeReachesStatusUntilRepaired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CCR_DB", "")
	path := filepath.Join(home, "ccr.db")
	damageRecoveryStore(t, path)

	recovery, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	recovery.SetLogger(logger)
	checkRecoveryIntegrity(home, recovery, logger)
	if _, err := recovery.Put(ccr.Recovery{ContentType: "text", Compressor: "test", Original: []byte("refused")}); err == nil {
		t.Fatal("damaged store took a write after the serve-time check")
	}
	if !strings.Contains(logs.String(), "recovery store is damaged") || !strings.Contains(logs.String(), "recovery-store repair") {
		t.Fatalf("damage was not logged with its fix:\n%s", logs.String())
	}
	if err := recovery.Close(); err != nil {
		t.Fatal(err)
	}

	report := readRecoveryStoreStatus(t)
	if !report.WritesRefused || report.KnownDamage == nil || !strings.Contains(report.Fix, "recovery-store repair") {
		t.Fatalf("status after damage: %+v", report)
	}

	out := captureStdout(t, func() { runRecoveryStore(logger, []string{"repair"}) })
	var repaired struct {
		Repair ccr.RepairReport `json:"repair"`
		Error  string           `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &repaired); err != nil || repaired.Repair.Result != "repaired" {
		t.Fatalf("repair output %q: %v", out, err)
	}
	if _, err := os.Stat(repaired.Repair.BackupPath); err != nil {
		t.Fatalf("no backup of the damaged store: %v", err)
	}
	report = readRecoveryStoreStatus(t)
	if report.WritesRefused || report.KnownDamage != nil {
		t.Fatalf("status after repair: %+v", report)
	}
}

func TestDescribeRecoveryStoreNamesWhyAndTheFix(t *testing.T) {
	full := recoveryStoreReport{StoreReport: ccr.StoreReport{Exists: true, Full: true, FullReason: ccr.StorageFullPageCap, MaxPayloadBytes: 512 << 20}}
	describeRecoveryStore(&full)
	if !full.WritesRefused || !strings.Contains(full.Reason, "page cap") || !strings.Contains(full.Fix, "CAVEMAN_CCR_MAX_BYTES") {
		t.Fatalf("full with nothing to expire: %+v", full)
	}
	expirable := recoveryStoreReport{StoreReport: ccr.StoreReport{Exists: true, Full: true, FullReason: ccr.StorageFullPayloadBudget, ExpirableObjects: 12}}
	describeRecoveryStore(&expirable)
	if expirable.WritesRefused || !strings.Contains(expirable.Reason, "expires 12 objects") {
		t.Fatalf("full with objects to expire: %+v", expirable)
	}
}
