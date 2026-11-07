package main

// `caveman-proxy recovery-store` and the serve-time integrity check (#1369,
// #1370). The recovery store (ccr.db) refuses writes when it is full or
// damaged, and compression then passes every input through; these make that
// state visible to `caveman status` and `caveman doctor`, and repairable.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

const recoveryStoreSchema = "caveman.recovery_store.v1"

const recoveryStoreUsage = "usage: caveman-proxy recovery-store [status|check|repair [--force]]"

// recoveryDamage is the last damage a check found, kept outside the damaged
// database so status can show it without reading the whole file again. A
// passing check or a repair removes it.
type recoveryDamage struct {
	Path      string    `json:"path"`
	Findings  []string  `json:"findings"`
	CheckedAt time.Time `json:"checked_at"`
}

func recoveryDamagePath(home string) string {
	return filepath.Join(home, "run", "ccr-integrity.json")
}

func recordRecoveryDamage(home, path string, findings []string) {
	marker := recoveryDamagePath(home)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(recoveryDamage{Path: path, Findings: findings, CheckedAt: time.Now().UTC()})
	if err != nil {
		return
	}
	tmp := marker + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, marker); err != nil {
		_ = os.Remove(tmp)
	}
}

func clearRecoveryDamage(home string) { _ = os.Remove(recoveryDamagePath(home)) }

func readRecoveryDamage(home, path string) *recoveryDamage {
	raw, err := os.ReadFile(recoveryDamagePath(home))
	if err != nil {
		return nil
	}
	var damage recoveryDamage
	if json.Unmarshal(raw, &damage) != nil || damage.Path != path {
		return nil
	}
	return &damage
}

// checkRecoveryIntegrity runs once per serve, off the request path. A release
// checks a store it has not seen intact before (#1369); the store refuses
// writes as soon as damage is found and logs the fix.
func checkRecoveryIntegrity(home string, recovery *ccr.Store, logger *slog.Logger) {
	report, err := recovery.CheckIntegrityOnce(version)
	if err != nil {
		logger.Warn("recovery store integrity check failed to run", "error", err)
		return
	}
	if report.Result == ccr.IntegrityDamaged {
		recordRecoveryDamage(home, recovery.Path(), report.Findings)
		return
	}
	if !report.Skipped {
		logger.Info("recovery store integrity check passed", "build", version)
	}
	clearRecoveryDamage(home)
}

// recoveryStoreReport is the JSON the CLI reads.
type recoveryStoreReport struct {
	Schema string `json:"schema"`
	ccr.StoreReport
	// KnownDamage is the last damage a check found, when no check has
	// passed since.
	KnownDamage *recoveryDamage `json:"known_damage,omitempty"`
	// WritesRefused says the store refuses new recoveries, so compression
	// passes inputs through; Fix names what clears it.
	WritesRefused bool   `json:"writes_refused"`
	Reason        string `json:"reason,omitempty"`
	Fix           string `json:"fix,omitempty"`
}

func runRecoveryStore(logger *slog.Logger, args []string) {
	home := mustHome(logger)
	action := "status"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		action, args = args[0], args[1:]
	}
	path := ccrPath(home)
	switch action {
	case "status", "check":
		budget, err := ccr.BudgetFromEnv()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		inspected, err := ccr.InspectStore(path, budget, action == "check")
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot inspect the recovery store:", err)
			os.Exit(1)
		}
		out := recoveryStoreReport{Schema: recoveryStoreSchema, StoreReport: inspected}
		switch {
		case action == "check" && inspected.Integrity == ccr.IntegrityDamaged:
			recordRecoveryDamage(home, inspected.Path, inspected.IntegrityFindings)
		case action == "check" && inspected.Integrity == ccr.IntegrityOK:
			clearRecoveryDamage(home)
		}
		if inspected.Integrity == "" {
			out.KnownDamage = readRecoveryDamage(home, inspected.Path)
		}
		describeRecoveryStore(&out)
		printJSON(out)
		if action == "check" && inspected.Integrity == ccr.IntegrityDamaged {
			os.Exit(1)
		}
	case "repair":
		report, err := ccr.RepairStore(path, hasArg(args, "--force"))
		if report.Result == "repaired" {
			clearRecoveryDamage(home)
		}
		printJSON(map[string]any{"schema": recoveryStoreSchema, "repair": report, "error": errorText(err)})
		switch {
		case errors.Is(err, ccr.ErrStoreInUse):
			os.Exit(3)
		case err != nil:
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, recoveryStoreUsage)
		os.Exit(2)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// describeRecoveryStore states, in the words status and doctor print, whether
// the store refuses writes, why, and what clears it.
func describeRecoveryStore(out *recoveryStoreReport) {
	const repair = "caveman stop, close agent sessions, then caveman-proxy recovery-store repair"
	limit := "page cap"
	if out.FullReason == ccr.StorageFullPayloadBudget {
		limit = "payload budget"
	}
	switch {
	case !out.Exists:
	case out.Integrity == ccr.IntegrityDamaged || (out.Integrity == "" && out.KnownDamage != nil):
		out.WritesRefused, out.Reason, out.Fix = true, "recovery store is damaged", repair
	case out.ReadError != "":
		out.WritesRefused, out.Reason, out.Fix = true, "recovery store cannot be read: "+out.ReadError, "caveman doctor"
	case out.Full && out.ExpirableObjects == 0:
		out.WritesRefused = true
		out.Reason = "recovery store is full (" + limit + ") and holds nothing old enough to expire"
		out.Fix = fmt.Sprintf("raise CAVEMAN_CCR_MAX_BYTES above %d and restart caveman", out.MaxPayloadBytes)
	case out.Full:
		// Not refused: the next write expires these and goes through.
		out.Reason = fmt.Sprintf("recovery store is full (%s); its next write expires %d objects of sessions that ended 14+ days ago", limit, out.ExpirableObjects)
	}
}
