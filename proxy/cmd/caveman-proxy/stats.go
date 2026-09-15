package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// The richer report is explicitly selected. Existing stats --json callers
// (telemetry and session footers) keep their original schema and behavior.
func parseStatsReportOptions(args []string) (store.StatsReportOptions, bool, string, error) {
	opts := store.StatsReportOptions{Days: 30, Now: time.Now()}
	writeReport, out := false, ""
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if seen[arg] {
			return opts, false, "", fmt.Errorf("duplicate %s", arg)
		}
		seen[arg] = true
		switch arg {
		case "--report", "--json":
		case "--write-report":
			writeReport = true
		case "--days", "--provider", "--model", "--agent", "--auth", "--out":
			i++
			if i >= len(args) || args[i] == "" || strings.HasPrefix(args[i], "--") || strings.ContainsAny(args[i], "\x00\r\n") {
				return opts, false, "", fmt.Errorf("%s requires a value", arg)
			}
			value := args[i]
			switch arg {
			case "--days":
				days, err := strconv.Atoi(value)
				if err != nil || days < 0 || days > 3660 {
					return opts, false, "", fmt.Errorf("--days must be an integer from 0 to 3660")
				}
				opts.Days = days
			case "--provider":
				opts.Provider = value
			case "--model":
				opts.Model = value
			case "--agent":
				opts.Agent = value
			case "--auth":
				opts.AuthMode = value
			case "--out":
				out, writeReport = value, true
			}
		default:
			return opts, false, "", fmt.Errorf("unknown stats report option %s", arg)
		}
	}
	return opts, writeReport, out, nil
}

func runStatsReport(logger *slog.Logger, args []string) {
	opts, writeReport, out, err := parseStatsReportOptions(args)
	if err != nil {
		logger.Error("invalid stats options", "error", err)
		os.Exit(2)
	}
	home := mustHome(logger)
	spend := mustStore(logger, home)
	defer spend.Close()
	report, err := spend.BuildStatsReport(opts)
	if err != nil {
		fatalJSON(logger, err)
		return
	}
	if writeReport {
		if out == "" {
			out = filepath.Join(home, "reports", "caveman-stats.html")
		}
		out, err = filepath.Abs(out)
		if err == nil {
			err = spend.WriteStatsHTML(report, out)
		}
		if err != nil {
			fatalJSON(logger, err)
			return
		}
	}
	// The report path is a presentation field, separate from the accounting
	// contract. Keeping it in stdout avoids stale-file guesses by the CLI.
	printJSON(struct {
		store.StatsReport
		ReportPath string `json:"report_path,omitempty"`
	}{StatsReport: report, ReportPath: out})
}
