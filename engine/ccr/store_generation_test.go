//go:build !js

package ccr

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type generationRequest struct {
	Op, Data, Handle string
}

type generationResponse struct {
	Handle, Data, Error string
	Changed             bool
	Skipped             bool
	Preserved           bool
}

// Run each connection in its own process: same-process SQLite VFS shared-memory
// caches can hide the actual cross-process journal failure. The helper exits
// without SQLite cleanup so quarantine tests release descriptors via the OS.
func TestCCRGenerationProcess(t *testing.T) {
	path := os.Getenv("CAVEMAN_TEST_CCR_PROCESS")
	if path == "" {
		return
	}
	store, err := OpenWithBudget(path, DefaultMaxStorageBytes)
	encoder := json.NewEncoder(os.Stdout)
	if err != nil {
		_ = encoder.Encode(generationResponse{Error: err.Error()})
		os.Exit(1)
	}
	_ = encoder.Encode(generationResponse{})
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	for {
		var req generationRequest
		if decoder.Decode(&req) != nil {
			os.Exit(0)
		}
		var resp generationResponse
		var err error
		switch req.Op {
		case "put":
			resp.Handle, err = store.Put(Recovery{Original: []byte(req.Data), Metadata: []byte("metadata:" + req.Data)})
		case "get":
			var data []byte
			data, err = store.Get(req.Handle)
			resp.Data = string(data)
		case "metadata":
			var data []byte
			data, err = store.GetMetadata(req.Handle)
			resp.Data = string(data)
		case "put_object":
			resp.Handle, err = store.PutObject(Object{Type: ObjectTaskDecision, SessionID: "replacement-session", Data: []byte(req.Data)})
		case "get_object":
			var obj Object
			obj, err = store.GetObject(req.Handle)
			resp.Data = string(obj.Data)
		case "currentness":
			err = store.SetObjectCurrentness(req.Handle, Stale)
		case "lifecycle":
			err = store.SetObjectLifecycle(req.Handle, Cold)
		case "list":
			_, err = store.ListSessionObjects("replacement-session", 10)
		case "decision":
			var obj Object
			obj, err = store.FindTaskDecision("decision-1")
			resp.Data = string(obj.Data)
		case "summary":
			_, err = store.Summary()
		case "checkpoint":
			_, err = store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		case "close":
			err = store.Close()
		default:
			err = fmt.Errorf("unknown helper operation %q", req.Op)
		}
		if err != nil {
			resp.Error = err.Error()
			resp.Changed = errors.Is(err, ErrStorageChanged)
		}
		if encoder.Encode(resp) != nil {
			os.Exit(1)
		}
	}
}

type generationProcess struct {
	t       *testing.T
	encoder *json.Encoder
	decoder *json.Decoder
	stop    func()
}

func startGenerationProcess(t *testing.T, path string) *generationProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCCRGenerationProcess$")
	cmd.Env = append(os.Environ(), "CAVEMAN_TEST_CCR_PROCESS="+path)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = stdin.Close()
			if err := cmd.Wait(); err != nil {
				t.Errorf("CCR helper process: %v", err)
			}
			cancel()
		})
	}
	t.Cleanup(stop)
	process := &generationProcess{t, json.NewEncoder(stdin), json.NewDecoder(stdout), stop}
	var opened generationResponse
	if err := process.decoder.Decode(&opened); err != nil || opened.Error != "" {
		t.Fatalf("open helper database: %v %+v", err, opened)
	}
	return process
}

func (p *generationProcess) call(req generationRequest) generationResponse {
	p.t.Helper()
	if err := p.encoder.Encode(req); err != nil {
		p.t.Fatal(err)
	}
	var response generationResponse
	if err := p.decoder.Decode(&response); err != nil {
		p.t.Fatal(err)
	}
	return response
}

func (p *generationProcess) ok(req generationRequest) generationResponse {
	p.t.Helper()
	response := p.call(req)
	if response.Error != "" {
		p.t.Fatalf("helper %s: %s", req.Op, response.Error)
	}
	return response
}

func windowsDeniedOpenFileReplacement(err error) bool {
	// ERROR_SHARING_VIOLATION is not os.ErrPermission in Go's Windows mapping.
	return runtime.GOOS == "windows" && (errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33)))
}

func moveSQLiteGeneration(t *testing.T, path string, suffixes []string) {
	t.Helper()
	for _, suffix := range suffixes {
		if err := os.Rename(path+suffix, path+suffix+".retired"); err != nil {
			if windowsDeniedOpenFileReplacement(err) {
				t.Skipf("Windows denied replacing an open SQLite file: %v", err)
			}
			t.Fatal(err)
		}
	}
}

func readGenerationBytes(t *testing.T, path string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, suffix := range sqliteSuffixes {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		out[suffix] = data
	}
	return out
}

func assertGenerationBytes(t *testing.T, path string, want map[string][]byte) {
	t.Helper()
	for suffix, before := range want {
		after, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("replacement database%s changed: %v", suffix, err)
		}
	}
}

func TestStoreRequiresRestartAfterCompleteReplacementAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery with spaces.db")
	old := startGenerationProcess(t, path)
	oldHandle := old.ok(generationRequest{Op: "put", Data: "retired original"}).Handle
	moveSQLiteGeneration(t, path, sqliteSuffixes[:])
	fresh := startGenerationProcess(t, path)
	freshHandle := fresh.ok(generationRequest{Op: "put", Data: "replacement original"}).Handle
	objectData := `{"decision_id":"decision-1","value":"replacement object"}`
	objectID := fresh.ok(generationRequest{Op: "put_object", Data: objectData}).Handle
	before := readGenerationBytes(t, path)
	for _, req := range []generationRequest{
		{Op: "get", Handle: freshHandle}, {Op: "get", Handle: oldHandle},
		{Op: "metadata", Handle: freshHandle}, {Op: "put", Data: "must not publish"},
		{Op: "put_object", Data: "must not publish"}, {Op: "get_object", Handle: objectID},
		{Op: "currentness", Handle: objectID}, {Op: "lifecycle", Handle: objectID},
		{Op: "list"}, {Op: "decision"}, {Op: "summary"}, {Op: "close"}, {Op: "close"},
	} {
		resp := old.call(req)
		if !resp.Changed || resp.Handle != "" || resp.Data != "" || !strings.Contains(resp.Error, "restart") {
			t.Fatalf("%s did not fail closed after replacement: %+v", req.Op, resp)
		}
		assertGenerationBytes(t, path, before)
	}
	old.stop()
	restarted := startGenerationProcess(t, path)
	if got := restarted.ok(generationRequest{Op: "get", Handle: freshHandle}).Data; got != "replacement original" {
		t.Fatalf("fresh process could not retrieve replacement: %q", got)
	}
	if got := restarted.ok(generationRequest{Op: "get_object", Handle: objectID}).Data; got != objectData {
		t.Fatalf("typed object after restart: %q", got)
	}
	if got := restarted.ok(generationRequest{Op: "metadata", Handle: freshHandle}).Data; got != "metadata:replacement original" {
		t.Fatalf("metadata after restart: %q", got)
	}
	if got := restarted.ok(generationRequest{Op: "decision"}).Data; got != objectData {
		t.Fatalf("decision after restart: %q", got)
	}
	handle := restarted.ok(generationRequest{Op: "put", Data: "written after process restart"}).Handle
	if got := fresh.ok(generationRequest{Op: "get", Handle: handle}).Data; got != "written after process restart" {
		t.Fatalf("acknowledged fresh-process write missing: %q", got)
	}
	restarted.ok(generationRequest{Op: "currentness", Handle: objectID})
	restarted.ok(generationRequest{Op: "lifecycle", Handle: objectID})
	restarted.ok(generationRequest{Op: "list"})
	restarted.ok(generationRequest{Op: "summary"})
	restarted.ok(generationRequest{Op: "close"})
	fresh.ok(generationRequest{Op: "close"})
}

func TestStoreCloseAfterCompleteReplacementPreservesLiveJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	old := startGenerationProcess(t, path)
	old.ok(generationRequest{Op: "put", Data: "retired uncheckpointed data"})
	moveSQLiteGeneration(t, path, sqliteSuffixes[:])
	fresh := startGenerationProcess(t, path)
	handle := fresh.ok(generationRequest{Op: "put", Data: "live replacement data"}).Handle
	before := readGenerationBytes(t, path)
	if resp := old.call(generationRequest{Op: "close"}); !resp.Changed {
		t.Fatalf("Close accepted a replaced database: %+v", resp)
	}
	assertGenerationBytes(t, path, before)
	if got := fresh.ok(generationRequest{Op: "get", Handle: handle}).Data; got != "live replacement data" {
		t.Fatalf("replacement was damaged by retired Close: %q", got)
	}
	fresh.ok(generationRequest{Op: "close"})
}

func TestStoreQuarantinesJournalChangesAcrossProcesses(t *testing.T) {
	for _, suffixes := range [][]string{{"-wal"}, {"-shm"}, {"-wal", "-shm"}} {
		t.Run(strings.Join(suffixes, "+"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ccr.db")
			old := startGenerationProcess(t, path)
			old.ok(generationRequest{Op: "put", Data: "checkpointed original"})
			old.ok(generationRequest{Op: "checkpoint"})
			oldHandle := old.ok(generationRequest{Op: "put", Data: "obsolete journal contents"}).Handle
			moveSQLiteGeneration(t, path, suffixes)
			// Replace the detached sidecars with byte copies. Their new identities
			// are the only change; this is a valid generation another process can
			// use, not a synthetic corrupt WAL that SQLite refuses to open.
			for _, suffix := range suffixes {
				data, err := os.ReadFile(path + suffix + ".retired")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path+suffix, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fresh := startGenerationProcess(t, path)
			freshHandle := fresh.ok(generationRequest{Op: "put", Data: "other process replacement journal"}).Handle
			before := readGenerationBytes(t, path)
			for _, op := range []string{"put", "get", "metadata", "put_object", "get_object", "currentness", "lifecycle", "list", "decision", "summary", "close", "close"} {
				resp := old.call(generationRequest{Op: op, Data: "must not be accepted", Handle: oldHandle})
				if !resp.Changed || resp.Handle != "" || resp.Data != "" || !strings.Contains(resp.Error, "restart") {
					t.Fatalf("%s did not fail closed: %+v", op, resp)
				}
				assertGenerationBytes(t, path, before)
			}
			if got := fresh.ok(generationRequest{Op: "get", Handle: freshHandle}).Data; got != "other process replacement journal" {
				t.Fatalf("replacement journal data damaged: %q", got)
			}
			fresh.ok(generationRequest{Op: "close"})
		})
	}
}

func TestStoreRefusesMissingOrInvalidReplacementUntilRestart(t *testing.T) {
	for _, replacement := range []string{"missing", "empty", "corrupt", "unrelated"} {
		t.Run(replacement, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ccr.db")
			old := startGenerationProcess(t, path)
			old.ok(generationRequest{Op: "put", Data: "retired original"})
			moveSQLiteGeneration(t, path, sqliteSuffixes[:])
			if replacement != "missing" {
				data := []byte{}
				if replacement == "corrupt" {
					data = []byte("this is not a SQLite database")
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if replacement == "unrelated" {
					db, err := sqlOpenUnrelated(path)
					if err != nil {
						t.Fatal(err)
					}
					_ = db.Close()
				}
			}
			resp := old.call(generationRequest{Op: "put", Data: "must not acknowledge a stale write"})
			if !resp.Changed || resp.Handle != "" {
				t.Fatalf("invalid replacement accepted: %+v", resp)
			}
			if replacement == "missing" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing recovery database was silently recreated")
				}
			}
			for _, suffix := range sqliteSuffixes {
				_ = os.Remove(path + suffix)
			}
			fresh := startGenerationProcess(t, path)
			handle := fresh.ok(generationRequest{Op: "put", Data: "restored valid database"}).Handle
			before := readGenerationBytes(t, path)
			if resp := old.call(generationRequest{Op: "get", Handle: handle}); !resp.Changed || resp.Data != "" {
				t.Fatalf("invalidated Store resumed after restoration: %+v", resp)
			}
			assertGenerationBytes(t, path, before)
			old.stop()
			restarted := startGenerationProcess(t, path)
			if got := restarted.ok(generationRequest{Op: "get", Handle: handle}).Data; got != "restored valid database" {
				t.Fatalf("restored database after restart: %q", got)
			}
			restarted.ok(generationRequest{Op: "close"})
			fresh.ok(generationRequest{Op: "close"})
		})
	}
}

func TestStoreNormalConcurrentConnectionsAndCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("concurrent original %d", i))
			handle, err := a.Put(Recovery{Original: data})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := b.Get(handle)
			if err != nil || !bytes.Equal(got, data) {
				t.Errorf("normal concurrent recovery: %q %v", got, err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := b.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Put(Recovery{Original: []byte("after checkpoint")}); err != nil {
		t.Fatal(err)
	}
}

// sqlOpenUnrelated constructs a real SQLite file without the recovery schema.
func sqlOpenUnrelated(path string) (io.Closer, error) {
	db, err := sql.Open("sqlite", SQLiteDSN(path))
	if err == nil {
		_, err = db.Exec(`CREATE TABLE unrelated (value TEXT)`)
	}
	return db, err
}

func TestStoreDoesNotPublishHandleWhenFilesChangeDuringPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handle, err := withStore(store, func() (string, error) {
		handle, err := store.put(Recovery{Original: []byte("write raced with database replacement")})
		if err != nil {
			return "", err
		}
		moveSQLiteGeneration(t, path, sqliteSuffixes[:])
		return handle, nil
	})
	// Restoring the old files cannot revive a Store that already observed a change.
	for _, suffix := range sqliteSuffixes {
		if err := os.Rename(path+suffix+".retired", path+suffix); err != nil {
			t.Fatal(err)
		}
	}
	if handle != "" || !errors.Is(err, ErrStorageChanged) {
		t.Fatalf("a raced write published a recoverable handle: %q %v", handle, err)
	}
	if _, err := store.Get(Handle([]byte("write raced with database replacement"))); !errors.Is(err, ErrStorageChanged) {
		t.Fatalf("restoring old files revived an invalidated Store: %v", err)
	}
}

func TestStoreRefusesRemovedJournalWithoutRecreatingIt(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ccr.db")
			old := startGenerationProcess(t, path)
			handle := old.ok(generationRequest{Op: "put", Data: "last original"}).Handle
			moveSQLiteGeneration(t, path, []string{suffix})
			for _, op := range []string{"get", "put", "close"} {
				resp := old.call(generationRequest{Op: op, Data: "new original", Handle: handle})
				if !resp.Changed || resp.Handle != "" || resp.Data != "" {
					t.Fatalf("%s accepted missing journal: %+v", op, resp)
				}
				if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing journal was silently recreated")
				}
			}
		})
	}
}

func TestStoreRefusesSymlinkReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	old := startGenerationProcess(t, path)
	old.ok(generationRequest{Op: "put", Data: "retired original"})
	moveSQLiteGeneration(t, path, sqliteSuffixes[:])
	target := filepath.Join(t.TempDir(), "unrelated.txt")
	original := []byte("unrelated target must survive")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	for _, op := range []string{"get", "put", "close"} {
		if resp := old.call(generationRequest{Op: op, Data: "new original"}); !resp.Changed {
			t.Fatalf("%s followed replacement symlink: %+v", op, resp)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("symlink target changed: %q %v", data, err)
	}
}

func TestSQLiteGenerationCapturesFileIdentityAtInspection(t *testing.T) {
	// This does not require replacing an open file. On Windows it catches a
	// delayed os.SameFile lookup accidentally identifying the replacement twice.
	// Resolve the temp root the way Open does: inspectSQLiteGeneration takes the
	// canonical path, and a raw t.TempDir() is not one on every runner (macOS
	// /var -> /private/var, Windows 8.3 RUNNER~1 -> runneradmin).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "closed recovery file.db")
	for _, suffix := range sqliteSuffixes {
		if err := os.WriteFile(path+suffix, []byte("original bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := inspectSQLiteGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	moveSQLiteGeneration(t, path, sqliteSuffixes[:])
	for _, suffix := range sqliteSuffixes {
		if err := os.WriteFile(path+suffix, []byte("original bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	after, err := inspectSQLiteGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, suffix := range sqliteSuffixes {
		if sameSQLiteFile(before[i], after[i]) {
			t.Errorf("database%s identity was resolved after replacement", suffix)
		}
	}
}

func TestStoreCloseCannotCheckpointReplacedJournalAfterWriterExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	old := startGenerationProcess(t, path)
	old.ok(generationRequest{Op: "put", Data: "checkpointed original"})
	old.ok(generationRequest{Op: "checkpoint"})
	old.ok(generationRequest{Op: "put", Data: "obsolete uncheckpointed WAL"})
	moveSQLiteGeneration(t, path, []string{"-wal", "-shm"})
	for _, suffix := range []string{"-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix + ".retired")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writer := startGenerationProcess(t, path)
	handle := writer.ok(generationRequest{Op: "put", Data: "new journal writer result"}).Handle
	// A live writer's SQLite shared lock can prevent the old connection's close
	// checkpoint. Exit that writer without SQLite cleanup so the test proves the
	// quarantine rule itself, even when the old connection can obtain exclusivity.
	writer.stop()
	before := readGenerationBytes(t, path)
	if resp := old.call(generationRequest{Op: "close"}); !resp.Changed {
		t.Fatalf("Close accepted a replaced journal: %+v", resp)
	}
	assertGenerationBytes(t, path, before)
	reader := startGenerationProcess(t, path)
	if got := reader.ok(generationRequest{Op: "get", Handle: handle}).Data; got != "new journal writer result" {
		t.Fatalf("stale close lost committed replacement data: %q", got)
	}
	reader.ok(generationRequest{Op: "close"})
}

func TestOpenDoesNotRecreateDatabaseLostAfterPreparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := openWithBudget(path, DefaultMaxStorageBytes, func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	})
	if store != nil || !errors.Is(err, ErrStorageChanged) {
		t.Fatalf("lost prepared database was accepted: %v %v", store, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("driver silently recreated the lost prepared database")
	}
}

func TestStoreQuarantinesMixedMainAndJournalGenerations(t *testing.T) {
	for _, retained := range [][]string{{"-wal", "-shm"}, {"-wal"}, {"-shm"}} {
		for _, first := range []string{"get", "close"} {
			t.Run(strings.Join(retained, "+")+"/"+first, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "ccr.db")
				old := startGenerationProcess(t, path)
				old.ok(generationRequest{Op: "put", Data: "retired pending WAL data"})
				template := filepath.Join(t.TempDir(), "replacement.db")
				fresh := startGenerationProcess(t, template)
				handle := fresh.ok(generationRequest{Op: "put", Data: "replacement main file data"}).Handle
				fresh.ok(generationRequest{Op: "close"})
				for _, suffix := range sqliteSuffixes {
					keep := false
					for _, value := range retained {
						keep = keep || value == suffix
					}
					if keep {
						continue
					}
					moveSQLiteGeneration(t, path, []string{suffix})
					if err := os.Rename(template+suffix, path+suffix); err != nil {
						t.Fatal(err)
					}
				}
				before := readGenerationBytes(t, path)
				for _, op := range []string{first, "get", "put", "close"} {
					response := old.call(generationRequest{Op: op, Handle: handle, Data: "must not write mixed generations"})
					if !response.Changed || response.Handle != "" || response.Data != "" {
						t.Fatalf("%s accepted a replaced main file with retained journals: %+v", op, response)
					}
					assertGenerationBytes(t, path, before)
				}
			})
		}
	}
}

func TestStoreRequiresRestartForCheckpointedReplacementWithoutJournals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	old := startGenerationProcess(t, path)
	old.ok(generationRequest{Op: "put", Data: "retired original"})
	template := filepath.Join(t.TempDir(), "replacement.db")
	fresh := startGenerationProcess(t, template)
	handle := fresh.ok(generationRequest{Op: "put", Data: "checkpointed replacement"}).Handle
	fresh.ok(generationRequest{Op: "close"})
	moveSQLiteGeneration(t, path, sqliteSuffixes[:])
	if err := os.Rename(template, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"get", "put", "close"} {
		resp := old.call(generationRequest{Op: op, Handle: handle, Data: "must not reopen"})
		if !resp.Changed || resp.Handle != "" || resp.Data != "" {
			t.Fatalf("%s adopted a checkpointed replacement: %+v", op, resp)
		}
		for _, suffix := range sqliteSuffixes[1:] {
			if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalidated Store created journal %s: %v", suffix, err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, before) {
			t.Fatalf("invalidated Store modified checkpointed replacement: %v", err)
		}
	}
	old.stop()
	restarted := startGenerationProcess(t, path)
	if got := restarted.ok(generationRequest{Op: "get", Handle: handle}).Data; got != "checkpointed replacement" {
		t.Fatalf("fresh process could not recover checkpointed replacement: %q", got)
	}
	newHandle := restarted.ok(generationRequest{Op: "put", Data: "new write after restart"}).Handle
	if got := restarted.ok(generationRequest{Op: "get", Handle: newHandle}).Data; got != "new write after restart" {
		t.Fatalf("fresh process round trip: %q", got)
	}
	restarted.ok(generationRequest{Op: "close"})
}

func TestCCROpenGenerationProcess(t *testing.T) {
	path := os.Getenv("CAVEMAN_TEST_CCR_OPEN_PATH")
	if path == "" {
		return
	}
	mode := os.Getenv("CAVEMAN_TEST_CCR_OPEN_CHANGE")
	var mutationError error
	var before map[string][]byte
	store, err := openWithBudgetHooks(path, DefaultMaxStorageBytes, nil, func() {
		if mode == "main-missing" {
			mutationError = os.Remove(path)
		} else {
			suffix := "-wal"
			if mode == "shm-replaced" {
				suffix = "-shm"
			}
			mutationError = os.Rename(path+suffix, path+suffix+".retired")
			if mutationError == nil {
				var data []byte
				data, mutationError = os.ReadFile(path + suffix + ".retired")
				if mutationError == nil {
					mutationError = os.WriteFile(path+suffix, data, 0o600)
				}
			}
		}
		before = map[string][]byte{}
		for _, suffix := range sqliteSuffixes {
			if data, err := os.ReadFile(path + suffix); err == nil {
				before[suffix] = data
			}
		}
	})
	response := generationResponse{Changed: errors.Is(err, ErrStorageChanged), Preserved: true}
	if err != nil {
		response.Error = err.Error()
	}
	if store != nil {
		_ = store.Close()
	}
	if mutationError != nil {
		response.Skipped = windowsDeniedOpenFileReplacement(mutationError)
		response.Error = mutationError.Error()
	}
	for suffix, want := range before {
		got, readErr := os.ReadFile(path + suffix)
		response.Preserved = response.Preserved && readErr == nil && bytes.Equal(got, want)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}

func TestOpenRefusesGenerationChangesAfterSQLiteInitialization(t *testing.T) {
	for _, mode := range []string{"main-missing", "wal-replaced", "shm-replaced"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ccr.db")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCCROpenGenerationProcess$")
			cmd.Env = append(os.Environ(), "CAVEMAN_TEST_CCR_OPEN_PATH="+path, "CAVEMAN_TEST_CCR_OPEN_CHANGE="+mode)
			cmd.Stderr = os.Stderr
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("open generation helper: %v", err)
			}
			var response generationResponse
			if err := json.Unmarshal(output, &response); err != nil {
				t.Fatalf("open generation response: %v: %s", err, output)
			}
			if response.Skipped {
				t.Skipf("Windows denied replacing an open SQLite file: %s", response.Error)
			}
			if !response.Changed || !response.Preserved {
				t.Fatalf("post-initialization change was not handled safely: %+v", response)
			}
			if mode == "main-missing" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("post-open security check recreated the lost main database")
				}
			}
		})
	}
}

// A parent directory whose mode is loose for one instant, an antivirus lock, or
// an I/O error means "could not verify", not "was replaced". Quarantine is
// terminal for the process, so latching it on those would make already-stored
// recoveries unreadable for the rest of a session that is otherwise fine.
func TestTransientInspectFailureDoesNotQuarantineStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		// os.Chmod(0o777) only toggles the read-only attribute on Windows, so it
		// cannot produce the loose parent this test needs; the ACL equivalent is
		// covered by TestWindowsACLRejectsBroadWriteGrant.
		t.Skip("POSIX writable-parent setup does not model a Windows DACL")
	}
	dir := t.TempDir()
	store, err := OpenWithBudget(filepath.Join(dir, "ccr.db"), DefaultMaxStorageBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(Recovery{Original: []byte("before")}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(Recovery{Original: []byte("during")}); err == nil {
		t.Fatal("a group/world writable parent must fail the operation")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := store.Put(Recovery{Original: []byte("after")})
	if err != nil {
		t.Fatalf("restored parent mode left the store quarantined: %v", err)
	}
	if got, err := store.Get(handle); err != nil || string(got) != "after" {
		t.Fatalf("Get after recovery = %q, %v", got, err)
	}
}
