package evals

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

//go:embed fixtures
var fixturesFS embed.FS

// Fixture is one entry in the manifest: a payload file plus the graders that
// must pass when the engine compresses it.
type Fixture struct {
	Name           string           `yaml:"name"`
	File           string           `yaml:"file"`
	Type           string           `yaml:"type"`  // force a content type; empty = auto-detect
	Mode           string           `yaml:"mode"`  // "compress" (default) or "record"
	Query          string           `yaml:"query"` // optional relevance query for query-aware compressors
	Probes         []RetentionProbe `yaml:"probes"`
	Graders        []Grader         `yaml:"graders"`
	QualityGraders []Grader         `yaml:"quality_graders"`
	QualityTask    QualityTask      `yaml:"quality_task"`
}

// Manifest is the fixture set.
type Manifest struct {
	Fixtures []Fixture `yaml:"fixtures"`
}

// TransformResult is one system-under-test transform result.
type TransformResult struct {
	Output          []byte
	ContentType     string
	Method          string
	TokensBefore    int
	TokensAfter     int
	Ratio           float64
	Basis           string
	PassedThrough   bool
	Recoverable     bool
	LosslessToModel *bool
}

// TransformRunner lets non-Caveman systems run through the same fixture,
// quality, and reporting path.
type TransformRunner interface {
	Transform(context.Context, Fixture, []byte) (TransformResult, error)
}

// FixtureReport is the outcome for one fixture.
type FixtureReport struct {
	Name            string         `json:"name"`
	ContentType     string         `json:"content_type"`
	TokensBefore    int            `json:"tokens_before"`
	TokensAfter     int            `json:"tokens_after"`
	Ratio           float64        `json:"ratio"`
	WordsBefore     int            `json:"words_before"`
	WordsAfter      int            `json:"words_after"`
	WordRatio       float64        `json:"word_ratio"`
	Basis           string         `json:"basis"`
	Passed          bool           `json:"passed"`
	ByteRecoverable bool           `json:"byte_recoverable"`
	Failures        []string       `json:"failures,omitempty"`
	Probes          []ProbeResult  `json:"probes,omitempty"`
	ProbeSummary    *ProbeSummary  `json:"probe_summary,omitempty"`
	Quality         *QualityReport `json:"quality,omitempty"`
}

// Report is the harness result.
type Report struct {
	Passed       bool            `json:"passed"`
	TokensBefore int             `json:"tokens_before"`
	TokensAfter  int             `json:"tokens_after"`
	TokenRatio   float64         `json:"token_ratio"`
	WordsBefore  int             `json:"words_before"`
	WordsAfter   int             `json:"words_after"`
	WordRatio    float64         `json:"word_ratio"`
	Probes       ProbeSummary    `json:"probes"`
	Quality      *QualitySummary `json:"quality,omitempty"`
	Fixtures     []FixtureReport `json:"fixtures"`
}

// Run replays the embedded fixture set behind its graders and returns the
// report. It runs the engine's configured quality gate.
func Run() (Report, error) {
	var m Manifest
	raw, err := fixturesFS.ReadFile("fixtures/manifest.yaml")
	if err != nil {
		return Report{}, fmt.Errorf("read manifest: %w", err)
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Report{}, fmt.Errorf("parse manifest: %w", err)
	}
	read := func(name string) ([]byte, error) {
		return fixturesFS.ReadFile("fixtures/" + name)
	}
	return RunManifest(m, read)
}

// RunDir replays a caller-supplied fixture directory containing manifest.yaml.
// Manifest file paths are slash-separated, relative, and confined to root even
// through symlinks; a custom empty corpus is a configuration error, never green.
func RunDir(root string) (Report, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve fixture directory: %w", err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(resolvedRoot, "manifest.yaml"))
	if err != nil {
		return Report{}, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(manifestRaw, &m); err != nil {
		return Report{}, fmt.Errorf("parse manifest: %w", err)
	}
	if len(m.Fixtures) == 0 {
		return Report{}, fmt.Errorf("manifest has no fixtures")
	}
	read := func(name string) ([]byte, error) {
		if !fs.ValidPath(name) || name == "." {
			return nil, fmt.Errorf("invalid fixture path %q", name)
		}
		candidate, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(resolvedRoot, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("fixture path escapes root: %q", name)
		}
		return os.ReadFile(candidate)
	}
	return RunManifest(m, read)
}

// RunWithQuality replays the embedded fixture set and also sends baseline and
// compressed prompts through a model runner before grading the answers.
func RunWithQuality(ctx context.Context, opts QualityOptions) (Report, error) {
	m, err := EmbeddedManifest()
	if err != nil {
		return Report{}, err
	}
	read := func(name string) ([]byte, error) {
		return fixturesFS.ReadFile("fixtures/" + name)
	}
	return RunManifestWithQuality(ctx, m, read, opts)
}

// EmbeddedManifest returns the built-in fixture manifest.
func EmbeddedManifest() (Manifest, error) {
	var m Manifest
	raw, err := fixturesFS.ReadFile("fixtures/manifest.yaml")
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return m, nil
}

// ReadEmbeddedFixture reads one built-in fixture file.
func ReadEmbeddedFixture(name string) ([]byte, error) {
	return fixturesFS.ReadFile("fixtures/" + name)
}

// RunManifest replays an arbitrary manifest, reading fixture files via read.
// It is exported so tests can seed a deliberately-failing manifest and assert
// the gate fails closed.
func RunManifest(m Manifest, read func(name string) ([]byte, error)) (Report, error) {
	return runManifest(context.Background(), m, read, nil)
}

// RunManifestWithQuality is RunManifest plus the LLM-in-the-loop quality layer.
// The runner is injected so CI can use a deterministic local runner while release
// jobs can call a real gateway/model.
func RunManifestWithQuality(ctx context.Context, m Manifest, read func(name string) ([]byte, error), opts QualityOptions) (Report, error) {
	if opts.Runner == nil {
		return Report{}, fmt.Errorf("quality runner is required")
	}
	return runManifest(ctx, m, read, &opts)
}

// RunManifestWithSystem runs a caller-supplied system-under-test through the
// same graders and optional quality layer as the built-in engine.
func RunManifestWithSystem(ctx context.Context, m Manifest, read func(name string) ([]byte, error), system TransformRunner, opts *QualityOptions) (Report, error) {
	if system == nil {
		return Report{}, fmt.Errorf("system runner is required")
	}
	if opts != nil && opts.Runner == nil {
		return Report{}, fmt.Errorf("quality runner is required")
	}
	return runManifestWithSystem(ctx, m, read, system, opts)
}

func runManifest(ctx context.Context, m Manifest, read func(name string) ([]byte, error), quality *QualityOptions) (Report, error) {
	store, err := ccr.OpenMemory()
	if err != nil {
		return Report{}, fmt.Errorf("open recovery store: %w", err)
	}
	defer store.Close()
	eng := engine.New(store, nil)
	return runManifestWithSystem(ctx, m, read, engineSystem{eng: eng}, quality)
}

func runManifestWithSystem(ctx context.Context, m Manifest, read func(name string) ([]byte, error), system TransformRunner, quality *QualityOptions) (Report, error) {
	report := Report{Passed: true}
	for _, f := range m.Fixtures {
		input, err := read(f.File)
		if err != nil {
			return Report{}, fmt.Errorf("read fixture %q: %w", f.File, err)
		}
		res, err := system.Transform(ctx, f, input)
		if err != nil {
			return Report{}, fmt.Errorf("compress fixture %q: %w", f.Name, err)
		}
		subject := Subject{
			Input:         input,
			Output:        res.Output,
			Ratio:         res.Ratio,
			ContentType:   res.ContentType,
			Method:        res.Method,
			PassedThrough: res.PassedThrough,
		}
		fr := FixtureReport{
			Name:            f.Name,
			ContentType:     res.ContentType,
			TokensBefore:    res.TokensBefore,
			TokensAfter:     res.TokensAfter,
			Ratio:           res.Ratio,
			WordsBefore:     CountWords(input),
			WordsAfter:      CountWords(res.Output),
			Basis:           res.Basis,
			Passed:          true,
			ByteRecoverable: res.Recoverable,
		}
		fr.WordRatio = ratio(fr.WordsBefore, fr.WordsAfter)
		probeResults, probeSummary, probeFailures := classifyProbes(f.Probes, res.Output, res.Recoverable)
		fr.Probes = probeResults
		if len(f.Probes) > 0 {
			fr.ProbeSummary = &probeSummary
		}
		if len(probeFailures) > 0 {
			fr.Passed = false
			fr.Failures = append(fr.Failures, probeFailures...)
		}
		mergeProbeSummary(&report.Probes, probeSummary)
		// A fixture with no graders is a configuration error, not a free pass.
		if len(f.Graders) == 0 {
			fr.Passed = false
			fr.Failures = append(fr.Failures, "no graders configured")
		}
		for _, g := range f.Graders {
			if v := Grade(g, subject); !v.Passed {
				fr.Passed = false
				fr.Failures = append(fr.Failures, v.Reason)
			}
		}
		if !fr.Passed {
			report.Passed = false
		}
		if quality != nil {
			qr, err := runQuality(ctx, *quality, f, input, res.Output, res.ContentType, res.Ratio, res.PassedThrough)
			if err != nil {
				return Report{}, fmt.Errorf("quality fixture %q: %w", f.Name, err)
			}
			fr.Quality = &qr
		}
		report.TokensBefore += fr.TokensBefore
		report.TokensAfter += fr.TokensAfter
		report.WordsBefore += fr.WordsBefore
		report.WordsAfter += fr.WordsAfter
		report.Fixtures = append(report.Fixtures, fr)
	}
	report.TokenRatio = ratio(report.TokensBefore, report.TokensAfter)
	report.WordRatio = ratio(report.WordsBefore, report.WordsAfter)
	if quality != nil {
		report.Quality = summarizeQuality(report.Fixtures, *quality)
		if !report.Quality.Passed {
			report.Passed = false
		}
	}
	return report, nil
}

type engineSystem struct {
	eng *engine.Engine
}

func (s engineSystem) Transform(_ context.Context, f Fixture, input []byte) (TransformResult, error) {
	mode := engine.ModeCompress
	if f.Mode == string(engine.ModeRecord) {
		mode = engine.ModeRecord
	}
	res, err := s.eng.Compress(input, engine.Options{Mode: mode, Type: f.Type, Query: f.Query})
	if err != nil {
		return TransformResult{}, err
	}
	recoverable := bytes.Equal(input, res.Output)
	if res.RecoveryHandle != "" {
		original, err := s.eng.Retrieve(res.RecoveryHandle)
		recoverable = err == nil && bytes.Equal(original, input)
	}
	return TransformResult{
		Output:          res.Output,
		ContentType:     res.ContentType,
		Method:          res.Method,
		TokensBefore:    res.TokensBefore,
		TokensAfter:     res.TokensAfter,
		Ratio:           res.Ratio,
		Basis:           res.Basis,
		PassedThrough:   res.PassedThrough(),
		Recoverable:     recoverable,
		LosslessToModel: res.LosslessToModel,
	}, nil
}

// FixtureFiles lists the embedded fixture file names (excluding the manifest),
// for tests and tooling.
func FixtureFiles() ([]string, error) {
	entries, err := fs.ReadDir(fixturesFS, "fixtures")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Name() != "manifest.yaml" {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
