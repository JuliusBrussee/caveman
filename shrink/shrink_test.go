package shrink

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

const catalog = `{
  "tools": [
    {
      "name": "search_files",
      "description": "Search the workspace for files. This long description walks the tree, honours .gitignore, ranks matches, and returns context lines so the agent can choose what to open next without another round trip.",
      "inputSchema": {
        "type": "object",
        "title": "SearchArgs",
        "properties": {
          "query":  {"type": "string", "description": "The query.", "examples": ["a", "b"]},
          "mode":   {"type": "string", "enum": ["regex", "literal", "glob"], "default": "literal", "description": "Interpretation mode with a long winded explanation that adds bytes but no selection signal at all."},
          "limit":  {"type": "integer", "default": 50, "description": "Max results to return, capped to keep the context window lean."}
        },
        "required": ["query"]
      }
    },
    {
      "name": "run_command",
      "description": "Execute a shell command and capture stdout and stderr. Prefer dedicated tools when one exists because shell output is unstructured.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "command": {"type": "string", "description": "The shell command to run, expanded by the login shell."}
        },
        "required": ["command"]
      }
    }
  ]
}`

// tmpStore returns a WithStorePath option pointing at a fresh per-test database
// file, so tests never write to the shared ~/.caveman/ccr.db.
func tmpStore(t *testing.T) Option {
	t.Helper()
	return WithStorePath(filepath.Join(t.TempDir(), "ccr.db"))
}

func TestShrinkCompressesCatalog(t *testing.T) {
	res, err := Shrink([]byte(catalog), tmpStore(t))
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if res.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", res.Basis)
	}
	if res.Ratio <= 0 {
		t.Fatalf("expected positive ratio, got %v", res.Ratio)
	}
	if !json.Valid(res.Output) {
		t.Fatal("compressed output is not valid JSON")
	}
	if res.RecoveryHandle == "" {
		t.Error("a lossy shrink must record a CCR recovery handle")
	}
}

// TestShrinkRoundTripsThroughAFreshStore is the reversibility gate. It proves the
// recovery handle a shrink mints is resolvable from a SEPARATE store instance
// opened at the same path — i.e. from a later process, which is the only recovery
// that matters. The previous in-memory store made this impossible: the handle was
// unresolvable the instant Shrink returned, so "reversible — nothing is destroyed"
// was false on every call. A tautological handle != "" check hid that; this test
// actually retrieves the bytes and compares them to the original.
func TestShrinkRoundTripsThroughAFreshStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ccr.db")

	res, err := Shrink([]byte(catalog), WithStorePath(dbPath))
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if res.RecoveryHandle == "" {
		t.Fatal("compressing shrink must mint a recovery handle")
	}
	if bytes.Equal(res.Output, []byte(catalog)) {
		t.Fatal("expected the shrink to actually transform the catalog")
	}

	// A FRESH store instance at the same path — this is what a `recover` invocation
	// in a new process sees. Nothing from the first call is held in memory.
	got, err := Recover(res.RecoveryHandle, WithStorePath(dbPath))
	if err != nil {
		t.Fatalf("recover from a fresh store: %v", err)
	}
	if !bytes.Equal(got, []byte(catalog)) {
		t.Fatalf("recovered bytes are not the original:\n got=%s\nwant=%s", got, catalog)
	}
}

// TestStructuralSelectionSurfaceInvariant proves the compressed catalog exposes
// the exact same structural surface (param names, enums, required) as the
// original. It deliberately makes no same-tool behavioral claim: descriptions
// are model-visible and lossy.
func TestStructuralSelectionSurfaceInvariant(t *testing.T) {
	before, err := SelectionProfile([]byte(catalog))
	if err != nil {
		t.Fatalf("profile(input): %v", err)
	}
	res, err := Shrink([]byte(catalog), tmpStore(t))
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	after, err := SelectionProfile(res.Output)
	if err != nil {
		t.Fatalf("profile(output): %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("selection surface regressed:\n before=%+v\n after =%+v", before, after)
	}
	if _, ok := before["search_files"]; !ok {
		t.Fatal("expected search_files in the selection profile")
	}
	if enums := before["search_files"].Enums["mode"]; len(enums) != 3 {
		t.Fatalf("expected 3 enum values for mode, got %v", enums)
	}
}

// TestShrinkPreservesArgumentConstraints is the call-validity gate at the product
// boundary: a shrunk catalog must keep the sentences an agent needs to build a
// valid call. Selection surviving is not enough — arguments are constructed from
// descriptions, and dropping an RFC3339 / mutual-exclusion / required rule yields
// invalid calls that the structural profile cannot detect.
func TestShrinkPreservesArgumentConstraints(t *testing.T) {
	const constrained = `{
	  "tools": [
	    {
	      "name": "put_object",
	      "description": "Store an object in the bucket and return its version id and etag so the caller can confirm the write landed and later address the exact revision. This trailing clause is filler prose with no rule and should be dropped once the text grows large. Provide exactly one of body or body_b64.",
	      "inputSchema": {
	        "type": "object",
	        "properties": {
	          "path":     {"type": "string", "description": "Destination, e.g. /srv/x. The path must be an absolute path."},
	          "body":     {"type": "string", "description": "Raw contents. Provide exactly one of body or body_b64."},
	          "when":     {"type": "string", "description": "Timestamp. The format must be RFC3339."}
	        },
	        "required": ["path"]
	      }
	    }
	  ]
	}`
	res, err := Shrink([]byte(constrained), tmpStore(t))
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if res.Ratio <= 0 {
		t.Fatalf("expected a reduction, got ratio %v", res.Ratio)
	}
	for _, must := range []string{
		"must be an absolute path", // absolute-path rule
		"exactly one of body",      // mutual exclusion
		"format must be RFC3339",   // timestamp format
		"e.g. /srv/x",              // abbreviation kept intact (not cut to "e.")
	} {
		if !bytes.Contains(res.Output, []byte(must)) {
			t.Errorf("constraint %q did not survive shrink; output=%s", must, res.Output)
		}
	}
	if bytes.Contains(res.Output, []byte("This trailing clause is filler")) {
		t.Error("filler prose should have been dropped from the large description")
	}
}

func TestShrinkByteSafeOnMalformed(t *testing.T) {
	const bad = `{not a valid catalog`
	res, err := Shrink([]byte(bad), tmpStore(t))
	if err != nil {
		t.Fatalf("shrink should not error on malformed input: %v", err)
	}
	if string(res.Output) != bad {
		t.Errorf("malformed input must pass through byte-identical, got %q", res.Output)
	}
	if res.Ratio != 0 || res.RecoveryHandle != "" {
		t.Errorf("pass-through must have ratio 0 and no handle, got ratio=%v handle=%q", res.Ratio, res.RecoveryHandle)
	}
}

func TestShrinkReturnsPassThroughResultWhenCCRIsUnavailable(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := Shrink([]byte(catalog), WithStore(store))
	if err == nil {
		t.Fatal("closed CCR store must return an operational error")
	}
	if !bytes.Equal(res.Output, []byte(catalog)) || res.Ratio != 0 || res.RecoveryHandle != "" || res.TokensAfter != res.TokensBefore {
		t.Fatalf("quota failure must return accounted original-byte pass-through: %+v", res)
	}
}

func TestRecoverUnknownHandleFails(t *testing.T) {
	if _, err := Recover("ccr_deadbeef", tmpStore(t)); err == nil {
		t.Fatal("recovering an unknown handle must fail, not guess")
	}
}

func TestLintReportsInferredReductions(t *testing.T) {
	rep, err := Lint([]byte(catalog))
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if rep.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", rep.Basis)
	}
	if len(rep.Tools) != 2 {
		t.Fatalf("expected 2 tool reports, got %d", len(rep.Tools))
	}
	if rep.Ratio <= 0 || rep.TokensAfter >= rep.TokensBefore {
		t.Fatalf("expected an overall reduction, got before=%d after=%d ratio=%v", rep.TokensBefore, rep.TokensAfter, rep.Ratio)
	}
	for _, tr := range rep.Tools {
		if tr.Name == "" {
			t.Error("a tool report is missing its name")
		}
	}
}
