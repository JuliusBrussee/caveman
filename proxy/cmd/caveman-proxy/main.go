// Command caveman-proxy is the standalone byte-safe proxy binary. `caveman start`
// launches it; it loads caveman.yaml, opens the local ~/.caveman/ spend store,
// and serves the byte-safe lifecycle on 127.0.0.1:8787 with zero cloud
// dependencies. Every savings figure it records is `inferred`.
//
// Subcommands:
//
//	caveman-proxy          serve the proxy (default)
//	caveman-proxy serve    serve the proxy
//	caveman-proxy stats    print local spend summary JSON; --recent N prints newest rows
//	caveman-proxy trial    analyze/report/export local trial data
//	caveman-proxy usage    import/link/refresh/unlink local usage data
//	caveman-proxy learn    scan/report/apply the local setup profiler (Cave Score + token sinks)
//	                       scan --retro [--behavior-budget-ms N] [--retro-budget-ms N]
//	                       bounds both passes and adds the retrospective
//	                       "would-have-saved" block for the scanned window
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/mem"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/nativehook"
	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
	"github.com/JuliusBrussee/caveman/proxy/internal/runstate"
	"github.com/JuliusBrussee/caveman/proxy/internal/standalone"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
	"gopkg.in/yaml.v3"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: redact.SlogReplaceAttr}))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "stats":
		runStats(logger, os.Args[2:])
	case "agent-evidence":
		runAgentEvidence(logger, os.Args[2:])
	case "trial":
		runTrial(logger, os.Args[2:])
	case "usage":
		runUsage(logger, os.Args[2:])
	case "learn":
		runLearn(logger, os.Args[2:])
	case "status":
		runStatus(logger, os.Args[2:])
	case "version":
		runVersion(os.Args[2:])
	case "native-why":
		runNativeWhy(logger, os.Args[2:])
	case "native-hook":
		runNativeHookBridge(os.Args[2:])
	case "serve":
		runServe(logger)
	default:
		logger.Error("unknown caveman-proxy subcommand", "command", cmd)
		os.Exit(2)
	}
}

func runNativeHookBridge(args []string) {
	if len(args) == 0 {
		return
	}
	agent := args[0]
	adapter := argFlag(args[1:], "--adapter", "")
	home := env.String("CAVEMAN_HOME", "")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return
		}
		home = filepath.Join(userHome, ".caveman")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 {
		return
	}
	_ = nativehook.Run(context.Background(), home, agent, adapter, raw, os.Stdout, os.Stderr)
}

func runNativeWhy(logger *slog.Logger, args []string) {
	decisionID := argFlag(args, "--decision", "")
	if decisionID == "" && len(args) == 1 && !strings.HasPrefix(args[0], "-") {
		decisionID = args[0]
	}
	if decisionID == "" {
		fmt.Fprintln(os.Stderr, "usage: caveman-proxy native-why --decision <dec_id>")
		os.Exit(2)
	}
	home := mustHome(logger)
	recovery, err := ccr.Open(ccrPath(home))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open local Decision Ledger")
		os.Exit(1)
	}
	defer recovery.Close()
	explanation, err := nativeruntime.ExplainDecision(recovery, decisionID)
	if errors.Is(err, ccr.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "decision %s not found\n", decisionID)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	printJSON(explanation)
}

func runAgentEvidence(logger *slog.Logger, args []string) {
	home := mustHome(logger)
	sessionID := argFlag(args, "--session", "")
	buildSHA256 := argFlag(args, "--build", "")
	planSHA256 := argFlag(args, "--plan", "")
	spend, err := store.Open(dbPath(home), logger)
	if err != nil {
		logger.Error("cannot open local spend store", "error", err)
		os.Exit(1)
	}
	defer spend.Close()
	evidence, err := spend.AgentEvidenceForBuild(sessionID, buildSHA256, planSHA256)
	if err != nil {
		logger.Error("cannot read agent evidence", "error", err)
		os.Exit(2)
	}
	printJSON(map[string]any{
		"schema":       "caveman.agent.evidence.v1",
		"basis":        "inferred",
		"verified_usd": 0,
		"requests":     evidence,
	})
}

func runServe(logger *slog.Logger) {
	home := mustHome(logger)
	cfg, err := config.Load(env.String("CAVEMAN_CONFIG", filepath.Join(home, "caveman.yaml")))
	if err != nil {
		logger.Error("cannot load caveman.yaml", "error", err)
		os.Exit(1)
	}
	spend, err := store.Open(dbPath(home), logger)
	if err != nil {
		logger.Error("cannot open local spend store", "error", err)
		os.Exit(1)
	}
	defer spend.Close()
	recovery, sessionMarkerKey := initializeNativePersistence(home, ccrPath(home), &cfg, logger)
	if recovery != nil {
		defer recovery.Close()
	}

	// One CCR store backs both exact S4 recovery and typed native-agent session
	// objects. Record mode still never writes recovery originals: it only permits
	// metadata-safe native runtime state when an installed host pack sends events.
	opts := standalone.Options{SessionMarkerKey: sessionMarkerKey}
	switch {
	case (cfg.Mode == "compress" || cfg.Mode == "pixel") && recovery != nil:
		opts.Compressor = standalone.NewEngineCompressor(recovery)
		// The spend store doubles as the durable replacement cache that keeps a
		// compressed message byte-identical on every later turn of a conversation.
		opts.PrefixCache = spend
	case cfg.ObserveEstimate && cfg.Mode == "record":
		opts.Compressor = standalone.NewEstimateCompressor()
		opts.ObserveEstimate = true
	}
	var nativeRuntime *nativeruntime.Runtime
	if recovery != nil {
		nativeRuntime = nativeruntime.NewWithReceiptsAndUsage(recovery, filepath.Join(home, "receipts"), spend)
		opts.SessionFallback = func(now time.Time, provider, model string) (string, string) {
			return nativeRuntime.UniqueRecentSession(now, 5*time.Second, provider, model)
		}
	}

	server := standalone.New(cfg, spend, opts)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if nativeRuntime != nil {
		go func() {
			if err := nativeruntime.Serve(ctx, home, nativeRuntime); err != nil && ctx.Err() == nil {
				logger.Warn("native runtime unavailable; hooks remain fail-open", "error", err)
			}
		}()
	}
	if env.String("CAVEMAN_PROXY_OWNER", "start") == "wrap" {
		idleTimeout := 30 * time.Minute
		if raw := env.String("CAVEMAN_NATIVE_IDLE_TIMEOUT", ""); raw != "" {
			parsed, parseErr := time.ParseDuration(raw)
			if parseErr != nil || parsed <= 0 {
				logger.Warn("invalid native idle timeout; using default", "value", raw, "default", idleTimeout)
			} else {
				idleTimeout = parsed
			}
		}
		if nativeRuntime != nil {
			go func() {
				if nativeRuntime.WaitForIdle(ctx, idleTimeout) {
					logger.Info("caveman proxy idle; shutting down", "idle_timeout", idleTimeout)
					cancel()
				}
			}()
		}
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("cannot bind proxy listener", "addr", cfg.Listen, "error", err)
		os.Exit(1)
	}
	state, err := runstate.New(cfg.Listen, cfg.Mode, env.String("CAVEMAN_PROXY_OWNER", "start"), version)
	if err != nil {
		_ = listener.Close()
		logger.Error("cannot create proxy run state", "error", err)
		os.Exit(1)
	}
	// Publish active gate inputs so a later wrap can prove a reused proxy matches
	// its requested recovery contract before making a compression claim.
	state.RecoveryViaMCP = env.String("CAVEMAN_RECOVERY", "") == "mcp"
	if err := runstate.Write(home, state); err != nil {
		_ = listener.Close()
		logger.Error("cannot write proxy run state", "error", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("caveman proxy listening", "addr", cfg.Listen, "mode", cfg.Mode, "basis", "inferred")
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("proxy stopped", "error", err)
			cancel()
		}
	}()
	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := runstate.RemoveMatching(home, state.Port, state.InstanceToken); err != nil {
		logger.Warn("cannot remove proxy run state", "error", err)
	}
}

// initializeNativePersistence keeps provider routing usable when local recovery
// state is corrupt or unavailable. Compression and native runtime both depend on
// durable exact recovery, so degradation is explicit record pass-through; no
// in-memory handle or correlation marker is minted.
func initializeNativePersistence(home, path string, cfg *config.Config, logger *slog.Logger) (*ccr.Store, []byte) {
	recovery, err := ccr.Open(path)
	if err != nil {
		logger.Warn("CCR unavailable; forcing record pass-through and disabling native runtime", "error", err)
		cfg.Mode = "record"
		cfg.ObserveEstimate = false
		return nil, nil
	}
	key, err := nativeruntime.LoadOrCreateSessionKey(home)
	if err != nil {
		_ = recovery.Close()
		logger.Warn("native session correlation unavailable; forcing record pass-through and disabling native runtime", "error", err)
		cfg.Mode = "record"
		cfg.ObserveEstimate = false
		return nil, nil
	}
	return recovery, key
}

func runStatus(logger *slog.Logger, args []string) {
	home := mustHome(logger)
	port := 0
	if raw := argFlag(args, "--port", ""); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			printJSON(runstate.Unknown())
			return
		}
		port = parsed
	} else {
		cfg, err := config.Load(env.String("CAVEMAN_CONFIG", filepath.Join(home, "caveman.yaml")))
		if err != nil {
			printJSON(runstate.Unknown())
			return
		}
		port, err = runstate.PortFromListen(cfg.Listen)
		if err != nil {
			printJSON(runstate.Unknown())
			return
		}
	}
	printJSON(runstate.ReadValidated(home, port))
}

func runVersion(args []string) {
	if hasArg(args, "--json") {
		printJSON(map[string]any{
			"version": version,
			"schema":  runstate.Schema,
			"capabilities": []string{
				"run_state",
				"sessions_scanned",
				"observe_token_accounting",
				"native_runtime_v1",
				"native_hook_bridge_v1",
				"typed_ccr",
				"decision_ledger_v1",
			},
		})
		return
	}
	fmt.Println(version)
}

// runStats prints the local spend summary as JSON for `caveman stats`. The
// summary's basis is always "inferred"; the figures are never re-projected.
func runStats(logger *slog.Logger, args []string) {
	home := mustHome(logger)
	spend, err := store.Open(dbPath(home), logger)
	if err != nil {
		logger.Error("cannot open local spend store", "error", err)
		os.Exit(1)
	}
	defer spend.Close()
	if recent := argFlag(args, "--recent", ""); recent != "" {
		n, err := strconv.Atoi(recent)
		if err != nil || n < 0 {
			logger.Error("invalid --recent value", "value", recent)
			os.Exit(2)
		}
		rows, err := spend.RecentRequests(n)
		if err != nil {
			logger.Error("cannot read recent request rows", "error", err)
			os.Exit(1)
		}
		printJSON(rows)
		return
	}
	// --json emits the compact observe-estimate object the CLI consumes for the
	// session-end would-have-saved line, optionally filtered to a session start
	// with --since <RFC3339>. Bare `stats` keeps printing the full Summary.
	if hasArg(args, "--json") {
		summary, err := spend.ObserveSummarySince(argFlag(args, "--since", ""))
		if err != nil {
			logger.Error("cannot read observe summary", "error", err)
			os.Exit(1)
		}
		if memory, memErr := mem.Open(mem.Options{}); memErr == nil {
			if count, countErr := memory.Count(); countErr == nil {
				summary.MemBlocks = &count
			}
			_ = memory.Close()
		}
		printJSON(summary)
		return
	}
	summary, err := spend.Summary()
	if err != nil {
		logger.Error("cannot read local spend store", "error", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(summary)
}

func runTrial(logger *slog.Logger, args []string) {
	sub := "report"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	home := mustHome(logger)
	spend := mustStore(logger, home)
	defer spend.Close()

	switch sub {
	case "start":
		trialID := argFlag(args, "--trial-id", "")
		agent := argFlag(args, "--agent", "unknown")
		command := argFlag(args, "--command", "")
		if err := spend.StartTrial(trialID, agent, command); err != nil {
			fatalJSON(logger, err)
		}
		printJSON(map[string]any{"trial_id": trialID, "started": true, "basis": "inferred"})
	case "finish":
		trialID := argFlag(args, "--trial-id", "")
		exitCode, _ := strconv.Atoi(argFlag(args, "--exit-code", "0"))
		if err := spend.FinishTrial(trialID, exitCode); err != nil {
			fatalJSON(logger, err)
		}
		printJSON(map[string]any{"trial_id": trialID, "finished": true, "exit_code": exitCode})
	case "analyze":
		trialID := requiredTrialID(logger, spend, args)
		plan, err := spend.AnalyzeTrial(trialID, ccrPath(home))
		if err != nil {
			fatalJSON(logger, err)
		}
		printJSON(plan)
	case "report":
		trialID := requiredTrialID(logger, spend, args)
		plan, err := spend.BuildTrialPlan(trialID)
		if err != nil {
			fatalJSON(logger, err)
		}
		if hasArg(args, "--json") {
			printJSON(plan)
			return
		}
		out := argFlag(args, "--out", store.DefaultReportPath(home, trialID))
		if err := spend.WriteTrialHTML(plan, out); err != nil {
			fatalJSON(logger, err)
		}
		printJSON(map[string]any{"trial_id": trialID, "report": out, "basis": plan.Basis})
	case "export":
		trialID := requiredTrialID(logger, spend, args)
		plan, err := spend.BuildTrialPlan(trialID)
		if err != nil {
			fatalJSON(logger, err)
		}
		outDir := argFlag(args, "--out", filepath.Join(home, "exports", "trial-"+trialID))
		paths, err := spend.ExportTrial(plan, outDir)
		if err != nil {
			fatalJSON(logger, err)
		}
		printJSON(map[string]any{"trial_id": trialID, "export": paths})
	case "promote":
		trialID := requiredTrialID(logger, spend, args)
		optimizerID := firstPositional(args)
		if optimizerID == "" {
			fatalJSON(logger, fmt.Errorf("usage: caveman-proxy trial promote <optimizer_id> --trial-id <id>"))
		}
		plan, err := spend.BuildTrialPlan(trialID)
		if err != nil {
			fatalJSON(logger, err)
		}
		promoteMove(logger, home, trialID, optimizerID, plan)
	default:
		fatalJSON(logger, fmt.Errorf("unknown trial subcommand: %s", sub))
	}
}

func runUsage(logger *slog.Logger, args []string) {
	if len(args) == 0 {
		fatalJSON(logger, fmt.Errorf("usage: caveman-proxy usage import|link|refresh|unlink <provider>"))
	}
	sub := args[0]
	args = args[1:]
	home := mustHome(logger)
	spend := mustStore(logger, home)
	defer spend.Close()
	provider := firstPositional(args)
	switch sub {
	case "import":
		if provider == "" {
			fatalJSON(logger, fmt.Errorf("usage: caveman-proxy usage import codex|claude"))
		}
		path := argFlag(args, "--path", "")
		since := argFlag(args, "--since", "")
		switch provider {
		case "codex":
			out, err := spend.ImportCodex(path, since)
			if err != nil {
				fatalJSON(logger, err)
			}
			printJSON(out)
		case "claude":
			out, err := spend.ImportClaude(path, since)
			if err != nil {
				fatalJSON(logger, err)
			}
			printJSON(out)
		default:
			fatalJSON(logger, fmt.Errorf("unknown usage source: %s", provider))
		}
	case "link", "refresh":
		switch provider {
		case "", "claude":
			out, err := spend.RefreshClaudeUsageFromEnv()
			if err != nil {
				fatalJSON(logger, err)
			}
			printJSON(out)
		case "codex":
			out, err := spend.ImportCodex(argFlag(args, "--path", ""), argFlag(args, "--since", ""))
			if err != nil {
				fatalJSON(logger, err)
			}
			printJSON(out)
		default:
			fatalJSON(logger, fmt.Errorf("unknown usage link source: %s", provider))
		}
	case "unlink":
		switch provider {
		case "claude", "anthropic":
			if err := spend.DeleteQuotaProvider("anthropic"); err != nil {
				fatalJSON(logger, err)
			}
			printJSON(map[string]any{"unlinked": "claude"})
		case "codex", "openai":
			if err := spend.DeleteQuotaProvider("openai"); err != nil {
				fatalJSON(logger, err)
			}
			printJSON(map[string]any{"unlinked": "codex"})
		default:
			fatalJSON(logger, fmt.Errorf("usage unlink needs claude|codex"))
		}
	default:
		fatalJSON(logger, fmt.Errorf("unknown usage subcommand: %s", sub))
	}
}

func runLearn(logger *slog.Logger, args []string) {
	sub := "scan"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	home := mustHome(logger)
	spend := mustStore(logger, home)
	defer spend.Close()
	cwd, _ := os.Getwd()
	sources := strings.Split(argFlag(args, "--sources", "codex,claude,caveman"), ",")
	since := argFlag(args, "--since", "30d")

	switch sub {
	case "scan":
		fmt.Fprintln(os.Stderr, "scanning local sessions (last 30d)…")
		// --retro is opt-in: without it the scan runs exactly as before. With it,
		// both passes are independently bounded so a cold base scan cannot consume
		// the child deadline before retro returns partial measured coverage.
		retro := learnRetroOptions(args)
		plan, err := spend.LearnScanWithRetro(sources, since, retro)
		if err != nil {
			fatalJSON(logger, err)
		}
		fmt.Fprintf(os.Stderr, "claude-code %d · codex %d · scoring…\n",
			plan.SessionsBySource["claude"], plan.SessionsBySource["codex"])
		if hasArg(args, "--write-report") {
			out := argFlag(args, "--out", store.DefaultLearnReportPath(home))
			if err := spend.WriteLearnHTML(plan, out); err != nil {
				fatalJSON(logger, err)
			}
			generation := argFlag(args, "--write-report-token", "")
			if _, err := spend.WriteLearnSidecars(home, plan, time.Now(), generation); err != nil {
				fatalJSON(logger, err)
			}
		}
		printJSON(plan)
	case "report":
		// --retro is the same opt-in as on scan: without it the report builds
		// exactly as before; with it the "would have saved" replay block renders.
		plan, err := spend.BuildLearnPlanWithRetro(cwd, sources, since, learnRetroOptions(args))
		if err != nil {
			fatalJSON(logger, err)
		}
		out := argFlag(args, "--out", store.DefaultLearnReportPath(home))
		if err := spend.WriteLearnHTML(plan, out); err != nil {
			fatalJSON(logger, err)
		}
		sidecar, err := spend.WriteLearnSidecars(home, plan, time.Now(), "")
		if err != nil {
			fatalJSON(logger, err)
		}
		if hasArg(args, "--json") {
			printJSON(plan)
			return
		}
		printJSON(map[string]any{"report": out, "sidecar": sidecar, "cave_score": plan.CaveScore.Score, "basis": plan.Basis, "sinks": len(plan.Sinks)})
	case "apply":
		sinkID := firstPositional(args)
		if sinkID == "" {
			fatalJSON(logger, fmt.Errorf("usage: caveman-proxy learn apply <sink_id> [--dry-run]"))
		}
		plan, err := spend.BuildLearnPlan(cwd, sources, since)
		if err != nil {
			fatalJSON(logger, err)
		}
		applyLearnSink(logger, home, sinkID, hasArg(args, "--dry-run"), plan)
	default:
		fatalJSON(logger, fmt.Errorf("unknown learn subcommand: %s", sub))
	}
}

func learnRetroOptions(args []string) store.RetroOptions {
	behaviorBudget, _ := strconv.Atoi(argFlag(args, "--behavior-budget-ms", "0"))
	retroBudget, _ := strconv.Atoi(argFlag(args, "--retro-budget-ms", "0"))
	return store.RetroOptions{
		Enabled:          hasArg(args, "--retro"),
		BehaviorBudgetMS: behaviorBudget,
		BudgetMS:         retroBudget,
	}
}

// applyLearnSink materializes a candidate edit for a reducible sink under
// ~/.caveman/candidates/ and returns it. It never edits user config files itself —
// the analyzer is read-only; the consent-gated editing skill performs real edits.
func applyLearnSink(logger *slog.Logger, home, sinkID string, dryRun bool, plan store.LearnPlan) {
	var target *store.Sink
	for i := range plan.Sinks {
		if plan.Sinks[i].SinkID == sinkID {
			target = &plan.Sinks[i]
			break
		}
	}
	if target == nil {
		fatalJSON(logger, fmt.Errorf("sink %q not found in current learn plan", sinkID))
	}
	var candidate map[string]any
	switch target.Class {
	case "reducible":
		candidate = map[string]any{
			"sink_id":                        sinkID,
			"title":                          target.Title,
			"class":                          target.Class,
			"expected_tokens_per_turn_saved": target.TokensPerTurn,
			"suggestion":                     target.Suggestion,
			"evidence":                       target.Evidence,
			"net_token_negative_gate":        "the editing skill must measure before>after tokens/turn and reject any edit that does not reduce them",
			"basis":                          plan.Basis,
		}
	case "recurring_context":
		candidate = offloadCandidate(sinkID, target, plan.Basis, plan.Window.Since)
	default:
		printJSON(map[string]any{
			"sink_id": sinkID, "applied": false, "class": target.Class,
			"reason": "only reducible and recurring_context sinks have a materializable fix; behavioral findings are advice the editing skill turns into a consent-gated nudge",
		})
		return
	}
	if dryRun {
		printJSON(map[string]any{"sink_id": sinkID, "applied": false, "dry_run": true, "candidate": candidate})
		return
	}
	dir := filepath.Join(home, "candidates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fatalJSON(logger, err)
	}
	path := filepath.Join(dir, "learn-"+sanitizeID(sinkID)+".json")
	raw, _ := json.MarshalIndent(candidate, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		fatalJSON(logger, err)
	}
	printJSON(map[string]any{"sink_id": sinkID, "applied": false, "candidate_path": path, "candidate": candidate})
}

// offloadCandidate builds the cavemem_offload candidate for a recurring_context
// sink. It carries content *locators* (file + line + block index + hash), the
// measured numbers, and the net-token-negative inputs — never the block body. The
// editing skill re-reads the body locally, offloads it to cavemem, trims the
// source, and writes the pointer, after explicit consent. The proxy never edits
// config and never calls cavemem.
func offloadCandidate(sinkID string, target *store.Sink, basis, window string) map[string]any {
	ev := target.Evidence
	if ev == nil {
		ev = map[string]any{}
	}
	topic := fmt.Sprintf("%v", ev["topic_label"])
	pointer := fmt.Sprintf("Detail offloaded to cavemem: %s. Recall with `caveman mem recall %q`; byte-exact original via `caveman mem recover <handle>`.", topic, topic)
	pointerCost := (len(pointer) + 3) / 4
	return map[string]any{
		"sink_id":  sinkID,
		"fix_kind": "cavemem_offload",
		"class":    target.Class,
		"title":    target.Title,
		"basis":    basis,
		"what_to_offload": map[string]any{
			"fingerprint":   ev["fingerprint"],
			"block_tokens":  ev["block_tokens"],
			"block_lines":   ev["block_lines"],
			"segmenter":     ev["segmenter"],
			"normalization": ev["normalization"],
			"locators":      ev["locators"],
		},
		"measured": map[string]any{
			"recurrence_sessions":       ev["recurrence_sessions"],
			"occurrences_total":         ev["occurrences_total"],
			"tokens_per_turn_amortized": target.TokensPerTurn,
			"tokens_per_day_rate":       target.TokensPerDayRate,
			"window":                    window,
		},
		"net_token_negative_inputs": map[string]any{
			"repaste_tokens":              ev["block_tokens"],
			"frequency_per_window":        ev["occurrences_total"],
			"pointer_cost_tokens":         pointerCost,
			"expected_recall_cost_tokens": nil,
			"gate":                        "(repaste_tokens * frequency) > (pointer_cost + expected_recall_cost); the editing skill measures expected_recall_cost from a real cavemem recall and rejects the offload if this does not hold",
		},
		"proposed_pointer_text": pointer,
		"applied":               false,
		"boundary":              "Proxy materialized this candidate only. It did NOT edit any config file and did NOT call cavemem. The consent-gated editing skill re-reads the block locally from the locator, offloads it to cavemem, trims the source, and writes the pointer — after explicit approval.",
	}
}

func sanitizeID(id string) string {
	repl := strings.NewReplacer(":", "_", "/", "_", "..", "_", " ", "_")
	return repl.Replace(id)
}

func dbPath(home string) string {
	return env.String("CAVEMAN_DB", filepath.Join(home, "caveman.db"))
}

func ccrPath(home string) string {
	return env.String("CAVEMAN_CCR_DB", filepath.Join(home, "ccr.db"))
}

func mustStore(logger *slog.Logger, home string) *store.Store {
	spend, err := store.Open(dbPath(home), logger)
	if err != nil {
		logger.Error("cannot open local spend store", "error", err)
		os.Exit(1)
	}
	return spend
}

func requiredTrialID(logger *slog.Logger, spend *store.Store, args []string) string {
	trialID := argFlag(args, "--trial-id", "")
	if trialID != "" {
		return trialID
	}
	latest, err := spend.LatestTrialID()
	if err != nil {
		fatalJSON(logger, err)
	}
	if latest == "" {
		fatalJSON(logger, fmt.Errorf("no trial_id supplied and no trial runs exist"))
	}
	return latest
}

func promoteMove(logger *slog.Logger, home, trialID, optimizerID string, plan store.TrialPlan) {
	var move store.TrialMove
	for _, candidate := range plan.Moves {
		if candidate.OptimizerID == optimizerID {
			move = candidate
			break
		}
	}
	if move.OptimizerID == "" {
		fatalJSON(logger, fmt.Errorf("optimizer %q not found in trial %s", optimizerID, trialID))
	}
	if move.Status != "safe_now" || !(strings.HasPrefix(move.SafetyClass, "S0_") || strings.HasPrefix(move.SafetyClass, "S1_")) {
		path := filepath.Join(home, "reports", "caveman-trial-"+trialID+"-"+optimizerID+"-candidate.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			fatalJSON(logger, err)
		}
		raw, _ := yaml.Marshal(map[string]any{
			"mode":         "record",
			"optimizers":   map[string]bool{optimizerID: true},
			"eval_command": "caveman evals run",
		})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			fatalJSON(logger, err)
		}
		printJSON(map[string]any{
			"trial_id": trialID, "optimizer_id": optimizerID, "promoted": false,
			"candidate_config": path, "eval_command": "caveman evals run",
			"reason": "move is not safe_now S0/S1",
		})
		return
	}
	path := env.String("CAVEMAN_CONFIG", filepath.Join(home, "caveman.yaml"))
	cfg := config.Config{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(raw, &cfg)
	}
	if cfg.Optimizers == nil {
		cfg.Optimizers = map[string]bool{}
	}
	cfg.Mode = "active"
	cfg.Optimizers[optimizerID] = true
	raw, _ := yaml.Marshal(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fatalJSON(logger, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		fatalJSON(logger, err)
	}
	printJSON(map[string]any{"trial_id": trialID, "optimizer_id": optimizerID, "promoted": true, "config": path})
}

func argFlag(args []string, name, fallback string) string {
	for i, arg := range args {
		if arg == name {
			if i+1 < len(args) {
				return args[i+1]
			}
			return fallback
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"=")
		}
	}
	return fallback
}

func hasArg(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}

func firstPositional(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			if !strings.Contains(args[i], "=") {
				i++
			}
			continue
		}
		return args[i]
	}
	return ""
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func fatalJSON(logger *slog.Logger, err error) {
	logger.Error("command failed", "error", err)
	os.Exit(1)
}

// mustHome resolves and creates the ~/.caveman directory, honoring CAVEMAN_HOME.
func mustHome(logger *slog.Logger) string {
	home := env.String("CAVEMAN_HOME", "")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			logger.Error("cannot resolve home directory", "error", err)
			os.Exit(1)
		}
		home = filepath.Join(h, ".caveman")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		logger.Error("cannot create ~/.caveman", "error", err)
		os.Exit(1)
	}
	return home
}
