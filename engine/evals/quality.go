package evals

import (
	"context"
	"fmt"
	"strings"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

// ModelUsage is model-side accounting for one fixture prompt.
type ModelUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Source       string  `json:"source,omitempty"`
}

// ModelRequest is one baseline or compressed prompt sent through a model runner.
type ModelRequest struct {
	FixtureName    string  `json:"fixture_name"`
	Variant        string  `json:"variant"` // baseline or compressed
	Model          string  `json:"model,omitempty"`
	ContentType    string  `json:"content_type"`
	Question       string  `json:"question,omitempty"`
	ExpectedAnswer string  `json:"expected_answer,omitempty"`
	Seed           int     `json:"seed"`        // sampling seed for multi-seed runs
	Temperature    float64 `json:"temperature"` // sampling temperature; 0 = deterministic
	Prompt         []byte  `json:"-"`
}

// ModelResponse is the answer and optional usage returned by a model runner.
type ModelResponse struct {
	Output []byte     `json:"-"`
	Usage  ModelUsage `json:"usage"`
}

// ModelRunner is the LLM-in-the-loop seam. CI can use EchoModel; release runs
// can provide a gateway-backed runner without changing the harness.
type ModelRunner interface {
	Complete(context.Context, ModelRequest) (ModelResponse, error)
}

// AnswerGrader grades one model answer. LocalGrader is the offline fallback;
// gateway/authoritative runs can inject the canonical optimizer grader service.
type AnswerGrader interface {
	Grade(context.Context, Grader, Subject) (Verdict, error)
}

// LocalGrader uses the engine's local fail-closed grader subset.
type LocalGrader struct{}

func (LocalGrader) Grade(_ context.Context, g Grader, s Subject) (Verdict, error) {
	return Grade(g, s), nil
}

// EchoModel is deterministic and network-free: it returns the prompt as the
// answer. It exercises the quality gate and accounting path without pretending to
// be a vendor model.
type EchoModel struct{}

func (EchoModel) Complete(_ context.Context, req ModelRequest) (ModelResponse, error) {
	return ModelResponse{Output: append([]byte(nil), req.Prompt...)}, nil
}

// QualityOptions configures the model-answer quality layer.
type QualityOptions struct {
	Runner       ModelRunner
	Model        string
	Mode         string
	Floor        float64
	RequireUsage bool
	Grader       AnswerGrader
	// Seed is the sampling seed for this pass. Multi-seed callers vary it across
	// passes; the deterministic offline runner ignores it.
	Seed int
	// Temperature is the sampling temperature for this pass. 0 keeps single-seed
	// runs deterministic; multi-seed runs raise it so seeds actually diverge.
	Temperature float64
}

// QualityTask defines the actual model-answer task for a fixture. It is separate
// from compression graders so payload-shape checks cannot masquerade as quality.
type QualityTask struct {
	Question string   `yaml:"question" json:"question"`
	Graders  []Grader `yaml:"graders" json:"graders"`
}

// QualityReport is the per-fixture LLM-in-the-loop result.
type QualityReport struct {
	Graders            []string   `json:"graders"`
	BaselinePassed     bool       `json:"baseline_passed"`
	CompressedPassed   bool       `json:"compressed_passed"`
	Retention          float64    `json:"retention"`
	BaselineFailures   []string   `json:"baseline_failures,omitempty"`
	CompressedFailures []string   `json:"compressed_failures,omitempty"`
	BaselineUsage      ModelUsage `json:"baseline_usage"`
	CompressedUsage    ModelUsage `json:"compressed_usage"`
}

// QualitySummary is the aggregate quality-preservation gate.
type QualitySummary struct {
	Mode                   string   `json:"mode"`
	Model                  string   `json:"model,omitempty"`
	Floor                  float64  `json:"floor"`
	BaselinePassRate       float64  `json:"baseline_pass_rate"`
	CompressedPassRate     float64  `json:"compressed_pass_rate"`
	Retention              float64  `json:"retention"`
	Passed                 bool     `json:"passed"`
	BaselineFailures       int      `json:"baseline_failures"`
	CompressedFailures     int      `json:"compressed_failures"`
	BaselineInputTokens    int      `json:"baseline_input_tokens"`
	BaselineOutputTokens   int      `json:"baseline_output_tokens"`
	CompressedInputTokens  int      `json:"compressed_input_tokens"`
	CompressedOutputTokens int      `json:"compressed_output_tokens"`
	BaselineCostUSD        float64  `json:"baseline_cost_usd,omitempty"`
	CompressedCostUSD      float64  `json:"compressed_cost_usd,omitempty"`
	UsageSource            string   `json:"usage_source"`
	CostSource             string   `json:"cost_source,omitempty"`
	TasksTotal             int      `json:"tasks_total"`
	TasksPassed            int      `json:"tasks_passed"`
	BaselineCpCTUSD        float64  `json:"baseline_cpct_usd,omitempty"`
	CompressedCpCTUSD      float64  `json:"compressed_cpct_usd,omitempty"`
	CostSavingsRatio       float64  `json:"cost_savings_ratio"`
	GraderTypes            []string `json:"grader_types"`
}

func runQuality(ctx context.Context, opts QualityOptions, f Fixture, input, compressed []byte, contentType string, compressionRatio float64, passedThrough bool) (QualityReport, error) {
	graders := qualityGraders(f)
	names := make([]string, 0, len(graders))
	for _, g := range graders {
		names = append(names, g.Type)
	}

	report := QualityReport{Graders: names}
	if len(graders) == 0 {
		report.BaselineFailures = append(report.BaselineFailures, "no quality graders configured")
		report.CompressedFailures = append(report.CompressedFailures, "no quality graders configured")
		return report, nil
	}
	if strings.TrimSpace(f.QualityTask.Question) == "" {
		report.BaselineFailures = append(report.BaselineFailures, "no quality question configured")
		report.CompressedFailures = append(report.CompressedFailures, "no quality question configured")
		return report, nil
	}
	expected := expectedAnswer(graders)

	baselinePrompt := qualityPrompt(f.QualityTask.Question, input)
	compressedPrompt := qualityPrompt(f.QualityTask.Question, compressed)
	baseline, err := opts.Runner.Complete(ctx, ModelRequest{
		FixtureName:    f.Name,
		Variant:        "baseline",
		Model:          opts.Model,
		ContentType:    contentType,
		Question:       f.QualityTask.Question,
		ExpectedAnswer: expected,
		Seed:           opts.Seed,
		Temperature:    opts.Temperature,
		Prompt:         baselinePrompt,
	})
	if err != nil {
		return QualityReport{}, fmt.Errorf("baseline model call: %w", err)
	}
	compressedResp, err := opts.Runner.Complete(ctx, ModelRequest{
		FixtureName:    f.Name,
		Variant:        "compressed",
		Model:          opts.Model,
		ContentType:    contentType,
		Question:       f.QualityTask.Question,
		ExpectedAnswer: expected,
		Seed:           opts.Seed,
		Temperature:    opts.Temperature,
		Prompt:         compressedPrompt,
	})
	if err != nil {
		return QualityReport{}, fmt.Errorf("compressed model call: %w", err)
	}

	counter := tokens.Default()
	report.BaselineUsage, err = fillUsage(counter, baselinePrompt, baseline, opts.RequireUsage)
	if err != nil {
		return QualityReport{}, fmt.Errorf("baseline usage: %w", err)
	}
	report.CompressedUsage, err = fillUsage(counter, compressedPrompt, compressedResp, opts.RequireUsage)
	if err != nil {
		return QualityReport{}, fmt.Errorf("compressed usage: %w", err)
	}

	baseSubject := Subject{Input: input, Output: baseline.Output, ContentType: contentType, PassedThrough: true}
	compSubject := Subject{Input: input, Output: compressedResp.Output, Ratio: compressionRatio, ContentType: contentType, PassedThrough: passedThrough}
	grader := opts.Grader
	if grader == nil {
		grader = LocalGrader{}
	}
	report.BaselineFailures, err = gradeFailures(ctx, grader, graders, baseSubject)
	if err != nil {
		return QualityReport{}, fmt.Errorf("baseline grading: %w", err)
	}
	report.CompressedFailures, err = gradeFailures(ctx, grader, graders, compSubject)
	if err != nil {
		return QualityReport{}, fmt.Errorf("compressed grading: %w", err)
	}
	report.BaselinePassed = len(report.BaselineFailures) == 0
	report.CompressedPassed = len(report.CompressedFailures) == 0
	if report.BaselinePassed && report.CompressedPassed {
		report.Retention = 1
	}
	return report, nil
}

func fillUsage(counter tokens.Counter, prompt []byte, resp ModelResponse, requireRunnerUsage bool) (ModelUsage, error) {
	usage := resp.Usage
	if usage.InputTokens > 0 && usage.OutputTokens > 0 {
		usage.Source = "runner"
		return usage, nil
	}
	if requireRunnerUsage {
		return ModelUsage{}, fmt.Errorf("runner did not return input/output token usage")
	}
	if usage.InputTokens == 0 {
		usage.InputTokens = counter.Count(prompt)
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = counter.Count(resp.Output)
	}
	usage.Source = "local_tokenizer"
	return usage, nil
}

func gradeFailures(ctx context.Context, runner AnswerGrader, graders []Grader, subject Subject) ([]string, error) {
	var failures []string
	for _, g := range graders {
		v, err := runner.Grade(ctx, g, subject)
		if err != nil {
			return nil, err
		}
		if !v.Passed {
			failures = append(failures, v.Reason)
		}
	}
	return failures, nil
}

func qualityGraders(f Fixture) []Grader {
	if len(f.QualityTask.Graders) > 0 {
		return append([]Grader(nil), f.QualityTask.Graders...)
	}
	if len(f.QualityGraders) > 0 {
		return append([]Grader(nil), f.QualityGraders...)
	}
	return nil
}

func summarizeQuality(fixtures []FixtureReport, opts QualityOptions) *QualitySummary {
	floor := opts.Floor
	if floor == 0 {
		floor = 0.99
	}
	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "deterministic-local"
	}
	s := &QualitySummary{Mode: mode, Model: opts.Model, Floor: floor, TasksTotal: len(fixtures)}
	if len(fixtures) == 0 {
		return s
	}
	var baselinePass, compressedPass int
	seenGraders := map[string]bool{}
	for _, f := range fixtures {
		if f.Quality == nil {
			continue
		}
		q := f.Quality
		for _, name := range q.Graders {
			if !seenGraders[name] {
				seenGraders[name] = true
				s.GraderTypes = append(s.GraderTypes, name)
			}
		}
		if q.BaselinePassed {
			baselinePass++
		} else {
			s.BaselineFailures++
		}
		if q.CompressedPassed {
			compressedPass++
			s.TasksPassed++
		} else {
			s.CompressedFailures++
		}
		s.BaselineInputTokens += q.BaselineUsage.InputTokens
		s.BaselineOutputTokens += q.BaselineUsage.OutputTokens
		s.CompressedInputTokens += q.CompressedUsage.InputTokens
		s.CompressedOutputTokens += q.CompressedUsage.OutputTokens
		s.BaselineCostUSD += q.BaselineUsage.CostUSD
		s.CompressedCostUSD += q.CompressedUsage.CostUSD
		if s.UsageSource == "" {
			s.UsageSource = q.CompressedUsage.Source
		} else if s.UsageSource != q.CompressedUsage.Source {
			s.UsageSource = "mixed"
		}
	}
	total := float64(len(fixtures))
	s.BaselinePassRate = float64(baselinePass) / total
	s.CompressedPassRate = float64(compressedPass) / total
	if baselinePass > 0 {
		s.Retention = float64(compressedPass) / float64(baselinePass)
		s.BaselineCpCTUSD = s.BaselineCostUSD / float64(baselinePass)
	}
	if compressedPass > 0 {
		s.CompressedCpCTUSD = s.CompressedCostUSD / float64(compressedPass)
	}
	if s.BaselineCostUSD > 0 && s.CompressedCostUSD < s.BaselineCostUSD {
		s.CostSavingsRatio = (s.BaselineCostUSD - s.CompressedCostUSD) / s.BaselineCostUSD
	}
	s.Passed = s.BaselineFailures == 0 && s.Retention >= floor
	return s
}

func qualityPrompt(question string, context []byte) []byte {
	return []byte("Use the context to answer the question.\n\nContext:\n" + string(context) + "\n\nQuestion:\n" + strings.TrimSpace(question) + "\n\nAnswer only.")
}

func expectedAnswer(graders []Grader) string {
	for _, g := range graders {
		if g.Type == "exact_match" {
			return graderString(g)
		}
	}
	return ""
}

// AnswerKeyModel is an explicit offline smoke runner. It never calls a provider;
// it returns the fixture's expected answer and labels usage as local.
type AnswerKeyModel struct{}

func (AnswerKeyModel) Complete(_ context.Context, req ModelRequest) (ModelResponse, error) {
	if strings.TrimSpace(req.ExpectedAnswer) == "" {
		return ModelResponse{}, fmt.Errorf("answer-key runner requires expected answer")
	}
	return ModelResponse{Output: []byte(req.ExpectedAnswer)}, nil
}
