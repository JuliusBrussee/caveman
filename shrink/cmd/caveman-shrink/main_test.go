package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cliRoundTripCatalog = `{"tools":[{"name":"search","description":"Search files in the workspace. This intentionally long filler sentence describes ranking, traversal, context windows, result formatting, pagination, workspace roots, ignore files, symlinks, hidden files, binary detection, and many other details that do not affect tool selection.","inputSchema":{"type":"object","title":"A deliberately verbose annotation title that is removable without changing selection or argument construction","$comment":"A deliberately verbose schema comment containing implementation notes, examples, historical context, and other annotation-only prose that the compressor may remove safely.","properties":{"query":{"type":"string","description":"Query text.","examples":["first long example query","second long example query","third long example query","fourth long example query"]}},"required":["query"]}}]}`

func TestShrinkAndRecoverAcrossProcesses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ccr.db")
	shrinkCmd := helperCommand(t, dbPath, "shrink")
	shrinkCmd.Stdin = bytes.NewBufferString(cliRoundTripCatalog)
	var shrunk, report bytes.Buffer
	shrinkCmd.Stdout = &shrunk
	shrinkCmd.Stderr = &report
	if err := shrinkCmd.Run(); err != nil {
		t.Fatalf("shrink subprocess: %v\nstderr=%s", err, report.String())
	}
	if bytes.Equal(shrunk.Bytes(), []byte(cliRoundTripCatalog)) {
		t.Fatal("fixture did not compress")
	}
	var result struct {
		RecoveryHandle string `json:"recovery_handle"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(report.Bytes()), &result); err != nil {
		t.Fatalf("decode shrink report: %v\nreport=%s", err, report.String())
	}
	if result.RecoveryHandle == "" {
		t.Fatal("compressing CLI run did not print a recovery handle")
	}

	recoverCmd := helperCommand(t, dbPath, "recover", result.RecoveryHandle)
	var recovered, recoverErr bytes.Buffer
	recoverCmd.Stdout = &recovered
	recoverCmd.Stderr = &recoverErr
	if err := recoverCmd.Run(); err != nil {
		t.Fatalf("recover subprocess: %v\nstderr=%s", err, recoverErr.String())
	}
	if !bytes.Equal(recovered.Bytes(), []byte(cliRoundTripCatalog)) {
		t.Fatalf("recovered bytes differ:\n got=%s\nwant=%s", recovered.Bytes(), cliRoundTripCatalog)
	}
}

func TestReadBoundedInput(t *testing.T) {
	got, err := readBoundedInput(strings.NewReader("1234"), 4)
	if err != nil || string(got) != "1234" {
		t.Fatalf("input at limit: got=%q err=%v", got, err)
	}
	if _, err := readBoundedInput(strings.NewReader("12345"), 4); err == nil || !strings.Contains(err.Error(), "cave_input_too_large") {
		t.Fatalf("oversized input error = %v", err)
	}
}

func helperCommand(t *testing.T, dbPath string, args ...string) *exec.Cmd {
	t.Helper()
	commandArgs := append([]string{"-test.run=TestShrinkCLIHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], commandArgs...)
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CAVEMAN_SHRINK_HELPER=") || strings.HasPrefix(entry, "CAVEMAN_CCR_DB=") {
			continue
		}
		environment = append(environment, entry)
	}
	cmd.Env = append(environment, "CAVEMAN_SHRINK_HELPER=1", "CAVEMAN_CCR_DB="+dbPath)
	return cmd
}

func TestShrinkCLIHelperProcess(t *testing.T) {
	if os.Getenv("CAVEMAN_SHRINK_HELPER") != "1" {
		return
	}
	separator := 0
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index + 1
			break
		}
	}
	if separator == 0 {
		os.Exit(2)
	}
	os.Args = append([]string{"caveman-shrink"}, os.Args[separator:]...)
	main()
	os.Exit(0)
}
