package evals_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/evals"
)

func TestRunDirUsesNamedCorpusAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	manifest := "fixtures:\n  - name: named-failure\n    file: input.txt\n    mode: record\n    graders:\n      - {type: contains, value: absent-marker}\n"
	if err := os.WriteFile(filepath.Join(root, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("caller supplied corpus"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := evals.RunDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || len(report.Fixtures) != 1 || report.Fixtures[0].Name != "named-failure" {
		t.Fatalf("custom failing corpus was not authoritative: %+v", report)
	}

	escape := "fixtures:\n  - name: escape\n    file: ../outside.txt\n    mode: record\n    graders:\n      - {type: contains, value: x}\n"
	if err := os.WriteFile(filepath.Join(root, "manifest.yaml"), []byte(escape), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := evals.RunDir(root); err == nil {
		t.Fatal("fixture traversal must fail closed")
	}
}

func TestEmbeddedFixturesPass(t *testing.T) {
	if !cgoBuild {
		t.Skip("embedded Python/TypeScript code fixtures require the cgo tree-sitter build")
	}
	report, err := evals.Run()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		for _, f := range report.Fixtures {
			if !f.Passed {
				t.Errorf("fixture %q failed: %v (ratio=%.4f)", f.Name, f.Failures, f.Ratio)
			}
		}
		t.Fatal("embedded eval fixtures must all pass")
	}
	// Every fixture must report the local-estimate basis.
	for _, f := range report.Fixtures {
		if f.Basis != "inferred" {
			t.Errorf("fixture %q basis = %q, want inferred", f.Name, f.Basis)
		}
	}
}

// A seeded gate failure must make the harness report Passed=false, so
// `caveman evals run` exits non-zero.
func TestSeededRatioFailureFailsClosed(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name:    "impossible-ratio",
		File:    "any",
		Mode:    "compress",
		Graders: []evals.Grader{{Type: "ratio_threshold", Min: 0.999}},
	}}}
	read := func(string) ([]byte, error) {
		return []byte(`{"results":[1,2,3,4,5,6,7,8,9,10,11,12]}`), nil
	}
	report, err := evals.RunManifest(m, read)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Error("an impossible ratio threshold must fail the gate")
	}
}

func TestUnknownGraderFailsClosed(t *testing.T) {
	v := evals.Grade(evals.Grader{Type: "totally_made_up"}, evals.Subject{})
	if v.Passed {
		t.Error("an unknown grader type must fail closed")
	}
}

func TestExactMatchGrader(t *testing.T) {
	v := evals.Grade(evals.Grader{Type: "exact_match", Value: "Paris"}, evals.Subject{Output: []byte(" Paris ")})
	if !v.Passed {
		t.Fatalf("exact_match should normalize whitespace: %v", v.Reason)
	}
	v = evals.Grade(evals.Grader{Type: "exact_match", Value: "Paris"}, evals.Subject{Output: []byte("paris")})
	if v.Passed {
		t.Fatal("exact_match must remain case-sensitive")
	}
}

func TestJSONSchemaGrader(t *testing.T) {
	v := evals.Grade(evals.Grader{
		Type: "json_schema",
		Options: map[string]any{"schema": map[string]any{
			"type":     "object",
			"required": []any{"tool_calls"},
			"properties": map[string]any{
				"tool_calls": map[string]any{
					"type":     "array",
					"minItems": float64(1),
					"items": map[string]any{
						"type":     "object",
						"required": []any{"name"},
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "enum": []any{"search_files"}},
						},
					},
				},
			},
		}},
	}, evals.Subject{Output: []byte(`{"tool_calls":[{"name":"search_files"}]}`)})
	if !v.Passed {
		t.Fatalf("json_schema should pass: %v", v.Reason)
	}

	v = evals.Grade(evals.Grader{
		Type:    "json_schema",
		Options: map[string]any{"schema": map[string]any{"type": "object", "required": []any{"tool_calls"}}},
	}, evals.Subject{Output: []byte(`{"other":[]}`)})
	if v.Passed {
		t.Fatal("json_schema must fail when required fields are missing")
	}
}

func TestToolSequenceGrader(t *testing.T) {
	v := evals.Grade(evals.Grader{
		Type:    "tool_sequence",
		Options: map[string]any{"tools": []any{"search_files", "run_command"}},
	}, evals.Subject{Output: []byte(`{"tool_calls":[{"name":"search_files"},{"function":{"name":"run_command"}}]}`)})
	if !v.Passed {
		t.Fatalf("tool_sequence should pass: %v", v.Reason)
	}

	v = evals.Grade(evals.Grader{
		Type:    "tool_sequence",
		Options: map[string]any{"tools": []any{"run_command", "search_files"}},
	}, evals.Subject{Output: []byte(`{"tool_calls":[{"name":"search_files"},{"name":"run_command"}]}`)})
	if v.Passed {
		t.Fatal("tool_sequence must fail on reordered tools")
	}
}

func TestFixtureWithNoGradersFailsClosed(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{Name: "ungraded", File: "any", Mode: "record"}}}
	read := func(string) ([]byte, error) { return []byte("x"), nil }
	report, err := evals.RunManifest(m, read)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Error("a fixture with no graders must not pass")
	}
}

func TestRunManifestReportsWordRatio(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name:    "text",
		File:    "text",
		Mode:    "record",
		Graders: []evals.Grader{{Type: "byte_identical"}},
	}}}
	read := func(string) ([]byte, error) { return []byte("one two three"), nil }
	report, err := evals.RunManifest(m, read)
	if err != nil {
		t.Fatal(err)
	}
	if report.WordsBefore != 3 || report.WordsAfter != 3 {
		t.Fatalf("words before/after = %d/%d, want 3/3", report.WordsBefore, report.WordsAfter)
	}
	if report.WordRatio != 0 {
		t.Fatalf("record mode word ratio = %.4f, want 0", report.WordRatio)
	}
}

func TestRetentionProbesClassifyVisibleRecoverableAndLost(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name: "probed",
		File: "probed",
		Mode: "compress",
		Probes: []evals.RetentionProbe{
			{Dimension: "error", Value: "ERR_VISIBLE"},
			{Dimension: "identifier", Value: "customer-37"},
		},
		Graders: []evals.Grader{{Type: "ratio_threshold", Min: 0}},
	}}}
	read := func(string) ([]byte, error) {
		return []byte("ERR_VISIBLE customer-37 original detail"), nil
	}

	recoverable := fixedTransform{
		output:      []byte("ERR_VISIBLE"),
		recoverable: true,
	}
	report, err := evals.RunManifestWithSystem(context.Background(), m, read, recoverable, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatalf("recoverable missing probe should pass: %+v", report)
	}
	if report.Probes.Retained != 1 || report.Probes.Recoverable != 1 || report.Probes.Lost != 0 {
		t.Fatalf("unexpected probe tally: %+v", report.Probes)
	}
	if got := report.Fixtures[0].Probes[0]; got.State != evals.ProbeRetained || got.ValueSHA256 == "" {
		t.Fatalf("visible probe = %+v", got)
	}
	if got := report.Fixtures[0].Probes[1]; got.State != evals.ProbeRecoverable {
		t.Fatalf("recoverable probe = %+v", got)
	}

	lost := fixedTransform{output: []byte("ERR_VISIBLE"), recoverable: false}
	report, err = evals.RunManifestWithSystem(context.Background(), m, read, lost, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Probes.Lost != 1 {
		t.Fatalf("lost probe must fail closed: %+v", report)
	}
}

func TestRetentionProbeRejectsEmptyValue(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name:    "empty-probe",
		File:    "empty-probe",
		Mode:    "record",
		Probes:  []evals.RetentionProbe{{Dimension: "identifier"}},
		Graders: []evals.Grader{{Type: "byte_identical"}},
	}}}
	read := func(string) ([]byte, error) { return []byte("unchanged"), nil }
	report, err := evals.RunManifest(m, read)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatal("empty retention probe value must fail fixture")
	}
	if got := report.Fixtures[0].Failures; len(got) == 0 {
		t.Fatal("empty retention probe failure was not reported")
	}
}

type fixedTransform struct {
	output      []byte
	recoverable bool
}

func (f fixedTransform) Transform(_ context.Context, _ evals.Fixture, input []byte) (evals.TransformResult, error) {
	return evals.TransformResult{
		Output:        f.output,
		ContentType:   "text",
		Method:        "test",
		TokensBefore:  len(input),
		TokensAfter:   len(f.output),
		Ratio:         0.5,
		Basis:         "inferred",
		Recoverable:   f.recoverable,
		PassedThrough: false,
	}, nil
}

func TestRunManifestWithQualityPassesEchoModel(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name:    "json",
		File:    "json",
		Mode:    "compress",
		Graders: []evals.Grader{{Type: "json_or_toon_round_trip"}, {Type: "contains", Value: "warnings"}},
		QualityTask: evals.QualityTask{
			Question: "Answer exactly with the warning field name.",
			Graders:  []evals.Grader{{Type: "exact_match", Value: "warnings"}},
		},
	}}}
	read := func(string) ([]byte, error) { return []byte(`{"warnings":["disk"]}`), nil }
	report, err := evals.RunManifestWithQuality(context.Background(), m, read, evals.QualityOptions{
		Runner: evals.AnswerKeyModel{},
		Floor:  0.99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatalf("echo quality should pass: %+v", report.Quality)
	}
	if report.Quality == nil || report.Quality.Retention != 1 {
		t.Fatalf("quality retention = %+v, want 1", report.Quality)
	}
}

func TestQualityStageFailsClosedWithoutQualityGraders(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name:    "only-ratio",
		File:    "json",
		Mode:    "compress",
		Graders: []evals.Grader{{Type: "ratio_threshold", Min: 0}},
	}}}
	read := func(string) ([]byte, error) { return []byte(`{"results":[1,2,3,4,5,6,7,8,9,10]}`), nil }
	report, err := evals.RunManifestWithQuality(context.Background(), m, read, evals.QualityOptions{Runner: evals.AnswerKeyModel{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatal("quality stage with no quality graders must fail closed")
	}
	if report.Quality == nil || report.Quality.BaselineFailures == 0 || report.Quality.CompressedFailures == 0 {
		t.Fatalf("quality failures not reported: %+v", report.Quality)
	}
}

type failingRunner struct{}

func (failingRunner) Complete(context.Context, evals.ModelRequest) (evals.ModelResponse, error) {
	return evals.ModelResponse{}, errors.New("boom")
}

func TestQualityRunnerErrorReturnsError(t *testing.T) {
	m := evals.Manifest{Fixtures: []evals.Fixture{{
		Name:    "text",
		File:    "text",
		Mode:    "record",
		Graders: []evals.Grader{{Type: "byte_identical"}},
		QualityTask: evals.QualityTask{
			Question: "Answer exactly x.",
			Graders:  []evals.Grader{{Type: "exact_match", Value: "x"}},
		},
	}}}
	read := func(string) ([]byte, error) { return []byte("x"), nil }
	_, err := evals.RunManifestWithQuality(context.Background(), m, read, evals.QualityOptions{Runner: failingRunner{}})
	if err == nil {
		t.Fatal("runner error must surface")
	}
}
