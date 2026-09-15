package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	handled, code := handleArgs([]string{"version", "--json"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("handled=%v code=%d stderr=%q", handled, code, stderr.String())
	}
	var got struct {
		Version      string   `json:"version"`
		Schema       string   `json:"schema"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.Version != version {
		t.Fatalf("version=%q want %q", got.Version, version)
	}
	if got.Schema != "caveman.mcp.version.v1" {
		t.Fatalf("schema=%q", got.Schema)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "mcp_recovery" {
		t.Fatalf("capabilities=%v", got.Capabilities)
	}
}

func TestUnknownArgumentExitsTwo(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	handled, code := handleArgs([]string{"unknown"}, &stdout, &stderr)
	if !handled || code != 2 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.String() != "usage: caveman-mcp [version --json]\n" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestNoArgumentsStartsServer(t *testing.T) {
	handled, code := handleArgs(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if handled || code != 0 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
}
