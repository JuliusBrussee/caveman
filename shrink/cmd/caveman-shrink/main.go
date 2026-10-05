// Command caveman-shrink compresses MCP/OpenAI tool catalogs.
//
//	caveman-shrink               compress stdin → stdout; JSON report on stderr
//	caveman-shrink shrink        same as above (explicit)
//	caveman-shrink lint FILE     print per-tool inferred token reductions
//	caveman-shrink recover HAND  print the original catalog for a recovery handle
//	caveman-shrink version       print the version; --json adds capabilities
//
// Everything it reports is `inferred`. The S4 compress path is fail-open: on any
// problem it forwards the original bytes unchanged. A shrink writes the original to
// the durable recovery store (CAVEMAN_CCR_DB / ~/.caveman/ccr.db), so a handle it
// prints resolves later via `recover`.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/JuliusBrussee/caveman/shrink"
)

const maxStdinBytes int64 = 32 << 20

// version is the build version (go build -ldflags "-X main.version=...").
var version = "dev"

// runVersion prints the version, or with --json {version, capabilities}: the
// shape every caveman binary answers so the CLI can probe what a build supports.
// The capabilities are its subcommands.
func runVersion(args []string, stdout io.Writer) {
	if len(args) > 0 && args[0] == "--json" {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"version":      version,
			"capabilities": []string{"shrink", "lint", "recover"},
		})
		return
	}
	fmt.Fprintln(stdout, version)
}

func main() {
	args := os.Args[1:]
	cmd := "shrink"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "shrink":
		runShrink()
	case "lint":
		runLint(args[1:])
	case "recover":
		runRecover(args[1:])
	case "version":
		runVersion(args[1:], os.Stdout)
	case "help", "--help", "-h":
		fmt.Fprintln(os.Stderr, "caveman-shrink [shrink] | lint <file> | recover <handle> | version [--json]")
	default:
		// No subcommand: treat the whole invocation as a shrink over stdin.
		runShrink()
	}
}

func runShrink() {
	input, err := readBoundedInput(os.Stdin, maxStdinBytes)
	if err != nil {
		fatal("read stdin: %v", err)
	}
	res, err := shrink.Shrink(input)
	if err != nil {
		// Byte-safe: forward the original unchanged rather than fail.
		_, _ = os.Stdout.Write(input)
		fmt.Fprintf(os.Stderr, `{"ratio":0,"basis":"inferred","note":"passed through: %v"}`+"\n", err)
		return
	}
	if _, err := os.Stdout.Write(res.Output); err != nil {
		fatal("write stdout: %v", err)
	}
	report, _ := json.Marshal(res)
	fmt.Fprintln(os.Stderr, string(report))
}

func readBoundedInput(r io.Reader, maxBytes int64) ([]byte, error) {
	input, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(input)) > maxBytes {
		return nil, fmt.Errorf("cave_input_too_large: stdin exceeds %d bytes", maxBytes)
	}
	return input, nil
}

func runLint(args []string) {
	if len(args) < 1 {
		fatal("usage: caveman-shrink lint <file>")
	}
	input, err := os.ReadFile(args[0])
	if err != nil {
		fatal("read %s: %v", args[0], err)
	}
	rep, err := shrink.Lint(input)
	if err != nil {
		fatal("lint: %v", err)
	}
	fmt.Printf("%-28s %10s %10s %8s\n", "TOOL", "BEFORE", "AFTER", "RATIO")
	for _, t := range rep.Tools {
		fmt.Printf("%-28s %10d %10d %7.1f%%\n", t.Name, t.TokensBefore, t.TokensAfter, t.Ratio*100)
	}
	fmt.Printf("%-28s %10d %10d %7.1f%%  (basis: %s)\n", "TOTAL", rep.TokensBefore, rep.TokensAfter, rep.Ratio*100, rep.Basis)
}

func runRecover(args []string) {
	if len(args) < 1 {
		fatal("usage: caveman-shrink recover <handle>")
	}
	original, err := shrink.Recover(args[0])
	if err != nil {
		fatal("recover %s: %v", args[0], err)
	}
	if _, err := os.Stdout.Write(original); err != nil {
		fatal("write stdout: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
