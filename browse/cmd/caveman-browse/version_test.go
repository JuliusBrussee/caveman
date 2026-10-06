package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestVersionJSON(t *testing.T) {
	var out bytes.Buffer
	runVersion([]string{"--json"}, &out)
	var v struct {
		Version      string   `json:"version"`
		Schema       string   `json:"schema"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("version --json is not JSON: %v: %q", err, out.String())
	}
	if v.Version != version || !strings.HasPrefix(v.Schema, "caveman.") || !slices.Contains(v.Capabilities, "snapshot") {
		t.Fatalf("version --json = %+v", v)
	}
	out.Reset()
	runVersion(nil, &out)
	if out.String() != version+"\n" {
		t.Fatalf("version = %q", out.String())
	}
}
