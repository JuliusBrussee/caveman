package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
)

func TestInitializeNativePersistenceCorruptCCRForcesRecordPassThrough(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "ccr.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: "compress", ObserveEstimate: true}
	recovery, key := initializeNativePersistence(home, path, &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if recovery != nil || key != nil {
		t.Fatal("corrupt CCR must not produce recovery runtime or marker key")
	}
	if cfg.Mode != "record" || cfg.ObserveEstimate {
		t.Fatalf("corrupt CCR mode = %q observe=%v, want record pass-through", cfg.Mode, cfg.ObserveEstimate)
	}
}

func TestInitializeNativePersistenceHealthyCCRRetainsRequestedMode(t *testing.T) {
	home := t.TempDir()
	cfg := config.Config{Mode: "compress"}
	recovery, key := initializeNativePersistence(home, filepath.Join(home, "ccr.db"), &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if recovery == nil {
		t.Fatal("healthy CCR did not open")
	}
	defer recovery.Close()
	if len(key) != 32 || cfg.Mode != "compress" {
		t.Fatalf("healthy persistence key=%d mode=%q", len(key), cfg.Mode)
	}
}

func TestReadNativeHookPayloadReturnsBeforeEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	got := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() {
		raw, err := readNativeHookPayload(reader)
		if err != nil {
			errs <- err
			return
		}
		got <- raw
	}()

	payload := []byte(`{"hook_event_name":"SessionStart","session_id":"codex-1"}`)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-errs:
		t.Fatal(err)
	case raw := <-got:
		if string(raw) != string(payload) {
			t.Fatalf("payload = %q, want %q", raw, payload)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("native hook payload read waited for EOF after a complete JSON object")
	}
}

func TestLearnRetroOptionsCarriesBothPassBudgets(t *testing.T) {
	got := learnRetroOptions([]string{
		"--retro",
		"--behavior-budget-ms", "20000",
		"--retro-budget-ms", "60000",
	})
	if !got.Enabled || got.BehaviorBudgetMS != 20000 || got.BudgetMS != 60000 {
		t.Fatalf("retro options = %+v, want enabled behavior=20000 retro=60000", got)
	}
}

func TestFirstPositionalDoesNotConsumeValueAfterBooleanFlag(t *testing.T) {
	if got := firstPositional([]string{"--dry-run", "claude_md_weight:user"}); got != "claude_md_weight:user" {
		t.Fatalf("boolean flag positional = %q", got)
	}
	if got := firstPositional([]string{"--since", "30d", "--json", "sink"}); got != "sink" {
		t.Fatalf("value/boolean flag positionals = %q", got)
	}
}

func TestCompatUpstreamsPublishesBuiltinAndUserMounts(t *testing.T) {
	cfg := config.Config{Compat: map[string]config.CompatConfig{
		"myprovider": {BaseURL: "http://127.0.0.1:4000", APIKeyEnv: "MYPROVIDER_API_KEY"},
	}}
	got := compatUpstreams(cfg)
	if got["myprovider"] != "http://127.0.0.1:4000" {
		t.Fatalf("user mount = %q", got["myprovider"])
	}
	if got["opencode-go"] != "https://opencode.ai/zen/go" {
		t.Fatalf("built-in mount = %q", got["opencode-go"])
	}
	if len(got) != 2 {
		t.Fatalf("published mounts = %v", got)
	}
}
