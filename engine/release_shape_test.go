package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// releaseShapeJS is plain JavaScript; the fixtures directory has TS and Python.
const releaseShapeJS = `const fs = require("fs");

function readConfig(path) {
  const raw = fs.readFileSync(path, "utf8");
  const parsed = JSON.parse(raw);
  if (!parsed.name) {
    throw new Error("config: missing name");
  }
  return parsed;
}

function mergeDefaults(config, defaults) {
  const out = { ...defaults };
  for (const key of Object.keys(config)) {
    if (config[key] !== undefined) {
      out[key] = config[key];
    }
  }
  return out;
}

module.exports = { readConfig, mergeDefaults };
`

// The released binaries are what users run, so a coding agent's Read of a
// TypeScript, JavaScript or Python file has to compress in the build they are
// compiled with, not only in a developer's cgo build. No build tag on purpose:
// under CGO_ENABLED=0 the code compressor parses Go only, every one of these
// passes through untouched, and this test fails. That gap shipped in every
// release up to bin-v2.0.2 (#1020, #1120); the release-shape CI lane runs this
// test with scripts/build-release-binaries.mjs's own flags.
func TestReleaseShapeReadListingsCompress(t *testing.T) {
	ts, err := os.ReadFile(filepath.Join("evals", "fixtures", "sample.ts"))
	if err != nil {
		t.Fatal(err)
	}
	py, err := os.ReadFile(filepath.Join("evals", "fixtures", "sample.py"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(nil, nil)
	for _, c := range []struct{ name, source string }{
		{"typescript", string(ts)},
		{"javascript", releaseShapeJS},
		{"python", string(py)},
	} {
		res := e.Simulate([]byte(gutterLines(c.source)), Options{Mode: ModeCompress})
		if res.ContentType != TypeCode {
			t.Errorf("%s Read listing detected as %q, want %q", c.name, res.ContentType, TypeCode)
			continue
		}
		if res.TokensAfter >= res.TokensBefore {
			t.Errorf("%s Read listing compressed nothing (%d -> %d tokens): this build has no tree-sitter code compressor", c.name, res.TokensBefore, res.TokensAfter)
		}
	}
}
