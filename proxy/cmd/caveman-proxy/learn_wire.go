package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// The learn subcommands below print one JSON document on stdout, like the rest
// of `learn`; the CLI owns the human rendering.

func runLearnExperiment(logger *slog.Logger, spend *store.Store, cwd string, sources, args []string) {
	positionals := learnSinkPositionals(args)
	action, label := "", ""
	if len(positionals) > 0 {
		action = positionals[0]
	}
	if len(positionals) > 1 {
		label = positionals[1]
	}
	needLabel := func(usage string) {
		if label == "" {
			fatalJSON(logger, fmt.Errorf("usage: caveman-proxy learn experiment %s", usage))
		}
	}
	var (
		out any
		err error
	)
	switch action {
	case "start":
		needLabel("start <label> [--sink <id>] [--fix-kind <kind>] [--note <text>]")
		out, err = spend.StartExperiment(label, argFlag(args, "--sink", ""), argFlag(args, "--fix-kind", ""), argFlag(args, "--note", ""))
	case "arm":
		needLabel("arm <label> on|off")
		if len(positionals) < 3 {
			fatalJSON(logger, fmt.Errorf("usage: caveman-proxy learn experiment arm <label> on|off"))
		}
		out, err = spend.SwitchExperimentArm(label, positionals[2])
	case "report":
		needLabel("report <label> [--since <window>] [--sources <list>]")
		// Default the window to the experiment's own lifetime: a fixed 30d would
		// silently drop the early arm of a longer holdout, and sessions before
		// the start belong to no arm anyway.
		since := argFlag(args, "--since", "")
		if since == "" {
			experiment, loadErr := spend.LoadExperiment(label)
			if loadErr != nil {
				fatalJSON(logger, loadErr)
			}
			since = experiment.CreatedAt
		}
		out, err = spend.BuildExperimentReport(cwd, label, sources, since)
	case "list":
		out, err = spend.ListExperiments()
	case "stop":
		needLabel("stop <label>")
		out, err = spend.StopExperiment(label)
	default:
		fatalJSON(logger, fmt.Errorf("usage: caveman-proxy learn experiment start|arm|report|list|stop <label>"))
	}
	if err != nil {
		fatalJSON(logger, err)
	}
	printJSON(out)
}

// runLearnExport writes the privacy-safe digest to a file the user inspects
// before deciding to share it. Nothing is sent anywhere.
func runLearnExport(logger *slog.Logger, spend *store.Store, home, cwd string, sources []string, since string, args []string) {
	digest, err := spend.BuildLearnDigest(cwd, sources, since)
	if err != nil {
		fatalJSON(logger, err)
	}
	out := argFlag(args, "--out", filepath.Join(home, "reports", "caveman-learn-digest.json"))
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		fatalJSON(logger, err)
	}
	raw, _ := json.MarshalIndent(digest, "", "  ")
	if err := os.WriteFile(out, append(raw, '\n'), 0o600); err != nil {
		fatalJSON(logger, err)
	}
	printJSON(map[string]any{"path": out, "summary": digest.DigestSummaryLine(), "digest": digest})
}

func runLearnReconcile(logger *slog.Logger, spend *store.Store, cwd string, sources []string, since string, args []string) {
	exportPath := argFlag(args, "--usage-export", "")
	if exportPath == "" {
		fatalJSON(logger, fmt.Errorf("usage: caveman-proxy learn reconcile --usage-export <csv> [--since <window>]"))
	}
	report, err := spend.BuildLearnReconcile(cwd, exportPath, sources, since)
	if err != nil {
		fatalJSON(logger, err)
	}
	printJSON(report)
}
