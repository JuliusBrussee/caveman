//go:build !js

package ccr

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// The supported connection hook stops at a deterministic point after the
// connection opens its journals, before Open records their identities. Separate
// writer processes create both real WAL generations. No mock SQLite operations
// or private driver APIs are involved.
func TestCCRFailedOpenProcess(t *testing.T) {
	path := os.Getenv("CAVEMAN_TEST_CCR_FAILED_OPEN_PATH")
	mode := os.Getenv("CAVEMAN_TEST_CCR_FAILED_OPEN_MODE")
	if path == "" || mode == "" {
		return
	}
	if mode == "stale" || mode == "fresh" {
		db, err := sql.Open("sqlite", SQLiteDSN(path)+"&mode=rw")
		if err == nil {
			_, err = db.Exec(`INSERT INTO unrelated VALUES (?)`, mode)
		}
		if err != nil {
			t.Fatal(err)
		}
		// Leave the committed WAL pending and release descriptors via OS exit.
		os.Exit(0)
	}
	db, err := sql.Open("sqlite", SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema + `
CREATE TABLE unrelated(value TEXT);
INSERT INTO unrelated VALUES ('base');
DROP TABLE typed_objects;
CREATE VIEW typed_objects AS SELECT 1 AS value;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range sqliteSuffixes[1:] {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("initial database starts with journal %s: %v", suffix, err)
		}
	}
	writeGeneration := func(mode string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCCRFailedOpenProcess$")
		cmd.Env = append(os.Environ(), "CAVEMAN_TEST_CCR_FAILED_OPEN_MODE="+mode)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initial-open writer %s: %v: %s", mode, err, output)
		}
	}
	var before map[string][]byte
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		if _, err := conn.ExecContext(context.Background(), `SELECT value FROM unrelated`, nil); err != nil {
			return err
		}
		writeGeneration("stale")
		if _, err := conn.ExecContext(context.Background(), `SELECT value FROM unrelated`, nil); err != nil {
			return err
		}
		for _, suffix := range sqliteSuffixes[1:] {
			if err := os.Rename(path+suffix, path+suffix+".candidate"); err != nil {
				if windowsDeniedOpenFileReplacement(err) {
					_ = json.NewEncoder(os.Stdout).Encode(generationResponse{Skipped: true, Error: err.Error()})
					os.Exit(0)
				}
				t.Fatal(err)
			}
		}
		writeGeneration("fresh")
		before = readGenerationBytes(t, path)
		return nil
	})
	store, err := Open(path)
	response := generationResponse{Preserved: before != nil}
	if err != nil {
		response.Error = err.Error()
	}
	if store != nil {
		t.Fatal("initial open accepted a view where its indexed table must exist")
	}
	for suffix, want := range before {
		got, err := os.ReadFile(path + suffix)
		response.Preserved = response.Preserved && err == nil && bytes.Equal(got, want)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}

func TestFailedInitialOpenPreservesJournalsReplacedBeforeCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCCRFailedOpenProcess$")
	cmd.Env = append(os.Environ(), "CAVEMAN_TEST_CCR_FAILED_OPEN_PATH="+path, "CAVEMAN_TEST_CCR_FAILED_OPEN_MODE=reader")
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("initial-open reader: %v", err)
	}
	var response generationResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("initial-open response: %v: %s", err, output)
	}
	if response.Skipped {
		t.Skip(response.Error)
	}
	if !strings.Contains(response.Error, "views may not be indexed") || !response.Preserved {
		t.Fatalf("failed initial open damaged another writer's journals: %+v", response)
	}
}

func TestOpenRejectsImpossibleBudgetBeforePreparingFiles(t *testing.T) {
	for _, budget := range []int64{-1, 0, 1, minimumStoragePages*512 - 1} {
		path := filepath.Join(t.TempDir(), "must-not-exist.db")
		if store, err := OpenWithBudget(path, budget); err == nil || store != nil {
			t.Fatalf("accepted impossible storage budget %d: %v %v", budget, store, err)
		}
		for _, suffix := range sqliteSuffixes {
			if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("budget %d prepared database%s: %v", budget, suffix, err)
			}
		}
	}
}
