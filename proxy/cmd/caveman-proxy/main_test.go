package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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
