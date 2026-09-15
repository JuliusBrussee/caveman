package store

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// learn_reconcile.go compares what learn measured against what the provider
// actually billed.
//
// Every other number here is bounded by what the transcripts on this machine
// happen to contain. That bound is invisible until you hold it next to an
// invoice — and the GAP is usually the most valuable finding available: it is
// where a forgotten script, a CI agent, a second machine, or an untracked tool
// is spending. A user who learns that learn accounts for 78% of their bill has
// learned something no detector could tell them.
//
// It is the only local path to a figure anchored in real money, and it is still
// not `verified`: an export is ground truth about the bill, not about causation,
// and the coverage ratio it produces makes no claim that anything was saved.
//
// Parsing is fail-closed by design. Provider export schemas change without
// notice, and a reconciliation built on a misread column is worse than none: it
// would state a confident wrong gap. Any ambiguity returns an error naming what
// could not be read.

const reconcileSchema = "caveman.learn.reconcile.v1"

// LearnReconcileRow is one model's measured-vs-billed comparison.
type LearnReconcileRow struct {
	Model          string  `json:"model"`
	BilledTokens   int64   `json:"billed_tokens"`
	MeasuredTokens int64   `json:"measured_tokens"`
	CoveragePct    float64 `json:"coverage_pct"`
}

// LearnReconcile is the reconciliation report.
type LearnReconcile struct {
	Schema string `json:"schema"`
	Basis  string `json:"basis"`
	Source string `json:"source"`
	// BilledTokens is read from the export; MeasuredTokens is what the scan saw.
	BilledTokens   int64               `json:"billed_tokens"`
	MeasuredTokens int64               `json:"measured_tokens"`
	CoveragePct    float64             `json:"coverage_pct"`
	Unattributed   int64               `json:"unattributed_tokens"`
	Rows           []LearnReconcileRow `json:"models"`
	Caveats        []string            `json:"caveats"`
}

// reconcileColumns names the header variants each field is accepted under. A
// header that matches none of these is a parse failure, never a guess by
// position — column order is exactly the thing that changes silently.
var reconcileColumns = map[string][]string{
	"model": {"model", "model_id", "model name", "modelversion", "model_version"},
	"input": {
		"input_tokens", "input tokens", "n_context_tokens_total", "prompt_tokens",
		"uncached_input_tokens", "input",
	},
	"output": {"output_tokens", "output tokens", "n_generated_tokens_total", "completion_tokens", "output"},
	"cache_read": {
		"cache_read_input_tokens", "cache read input tokens", "cached_tokens",
		"input_cached_tokens", "cache_read_tokens",
	},
	"cache_write": {
		"cache_creation_input_tokens", "cache creation input tokens",
		"cache_write_tokens", "cache_creation_tokens",
	},
}

func normalizeHeader(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	// A UTF-8 BOM on the first header cell is common in provider exports.
	name = strings.TrimPrefix(name, "\ufeff")
	name = strings.ReplaceAll(name, "-", "_")
	return name
}

// mapReconcileHeader resolves the header row to column indexes. Model plus at
// least one token column is the minimum; anything less cannot be reconciled.
func mapReconcileHeader(header []string) (map[string]int, error) {
	found := map[string]int{}
	for index, raw := range header {
		normalized := normalizeHeader(raw)
		for field, aliases := range reconcileColumns {
			if _, taken := found[field]; taken {
				continue
			}
			for _, alias := range aliases {
				if normalized == normalizeHeader(alias) {
					found[field] = index
					break
				}
			}
		}
	}
	if _, ok := found["model"]; !ok {
		return nil, fmt.Errorf("usage export has no recognizable model column (saw: %s)", strings.Join(header, ", "))
	}
	tokenFields := 0
	for _, field := range []string{"input", "output", "cache_read", "cache_write"} {
		if _, ok := found[field]; ok {
			tokenFields++
		}
	}
	if tokenFields == 0 {
		return nil, fmt.Errorf("usage export has no recognizable token columns (saw: %s)", strings.Join(header, ", "))
	}
	return found, nil
}

func reconcileCell(record []string, columns map[string]int, field string) int64 {
	index, ok := columns[field]
	if !ok || index >= len(record) {
		return 0
	}
	raw := strings.TrimSpace(strings.ReplaceAll(record[index], ",", ""))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		return 0
	}
	return int64(value)
}

// parseUsageExport reads a provider usage CSV into billed tokens per model.
func parseUsageExport(path string) (map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read usage export: %w", err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("usage export has no header row: %w", err)
	}
	columns, err := mapReconcileHeader(header)
	if err != nil {
		return nil, err
	}
	billed := map[string]int64{}
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("usage export row: %w", err)
		}
		modelIndex := columns["model"]
		if modelIndex >= len(record) {
			continue
		}
		model := strings.TrimSpace(record[modelIndex])
		if model == "" {
			continue
		}
		total := reconcileCell(record, columns, "input") +
			reconcileCell(record, columns, "output") +
			reconcileCell(record, columns, "cache_read") +
			reconcileCell(record, columns, "cache_write")
		if total <= 0 {
			continue
		}
		billed[model] += total
	}
	if len(billed) == 0 {
		return nil, fmt.Errorf("usage export contained no priced rows")
	}
	return billed, nil
}

// BuildLearnReconcile compares an export against the scanned window.
func (s *Store) BuildLearnReconcile(cwd, exportPath string, sources []string, sinceExpr string) (LearnReconcile, error) {
	billed, err := parseUsageExport(exportPath)
	if err != nil {
		return LearnReconcile{}, err
	}
	plan, err := s.BuildLearnPlan(cwd, sources, sinceExpr)
	if err != nil {
		return LearnReconcile{}, err
	}
	return buildLearnReconcile(billed, plan.Spend, exportPath), nil
}

func buildLearnReconcile(billed map[string]int64, spend *LearnSpend, source string) LearnReconcile {
	report := LearnReconcile{
		Schema: reconcileSchema, Basis: learnBasis, Source: source, Rows: []LearnReconcileRow{},
	}
	measured := map[string]int64{}
	if spend != nil {
		for _, model := range spend.Models {
			measured[model.Model] += model.Tokens
		}
		for _, unpriced := range spend.Unpriced {
			measured[unpriced.Model] += unpriced.Tokens
		}
	}
	models := make([]string, 0, len(billed))
	for model := range billed {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		row := LearnReconcileRow{Model: model, BilledTokens: billed[model]}
		// Match the export's model id, then the dated-alias form, and nothing
		// looser: attributing one model's traffic to another would invent
		// coverage that does not exist.
		row.MeasuredTokens = measured[model]
		if row.MeasuredTokens == 0 {
			base := modelDateSuffix.ReplaceAllString(model, "")
			row.MeasuredTokens = measured[base]
		}
		if row.BilledTokens > 0 {
			row.CoveragePct = roundPct(float64(min64(row.MeasuredTokens, row.BilledTokens)) * 100 / float64(row.BilledTokens))
		}
		report.BilledTokens += row.BilledTokens
		report.MeasuredTokens += row.MeasuredTokens
		report.Rows = append(report.Rows, row)
	}
	if report.BilledTokens > 0 {
		covered := min64(report.MeasuredTokens, report.BilledTokens)
		report.CoveragePct = roundPct(float64(covered) * 100 / float64(report.BilledTokens))
		report.Unattributed = report.BilledTokens - covered
	}
	report.Caveats = append(report.Caveats,
		"Billed tokens come from the provider export; measured tokens come from transcripts on this machine. The gap is traffic learn cannot see — other machines, CI agents, scripts, or tools that keep no transcript.",
		"Coverage is a token comparison, not a savings claim, and it does not promote any figure to verified.",
	)
	if report.MeasuredTokens > report.BilledTokens {
		report.Caveats = append(report.Caveats,
			"Measured tokens exceed billed tokens for at least one model. The export window and the scan window probably differ; re-run with --since matching the export before reading the coverage figure.")
	}
	return report
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
