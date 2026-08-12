package store

// TrialPlan is the public local proof-loop report emitted by
// `caveman trial report --json` and rendered into the static HTML report.
type TrialPlan struct {
	Schema         string          `json:"schema"`
	TrialID        string          `json:"trial_id"`
	Basis          string          `json:"basis"`
	Window         TrialWindow     `json:"window"`
	Headline       TrialHeadline   `json:"headline"`
	Origins        []UsageOrigin   `json:"origins"`
	QuotaSnapshots []QuotaSnapshot `json:"quota_snapshots"`
	Moves          []TrialMove     `json:"moves"`
	Learnings      []Learning      `json:"learnings"`
	Caveats        []string        `json:"caveats"`
}

type TrialWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type TrialHeadline struct {
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	// EstimatedSavingsUSD is the legacy wire name for the sum of explicit move
	// counterfactuals. Content-size replay and heuristic candidates contribute 0.
	EstimatedSavingsUSD float64 `json:"estimated_savings_usd"`
}

type UsageOrigin struct {
	SourceKind   string  `json:"source_kind"`
	AgentSlug    string  `json:"agent_slug"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Basis        string  `json:"basis"`
}

type QuotaSnapshot struct {
	Provider string  `json:"provider"`
	PlanType string  `json:"plan_type"`
	Window   string  `json:"window"`
	UsedPct  float64 `json:"used_pct"`
	ResetsAt string  `json:"resets_at"`
	Basis    string  `json:"basis"`
}

type TrialMove struct {
	OptimizerID string `json:"optimizer_id"`
	Title       string `json:"title"`
	SafetyClass string `json:"safety_class"`
	Status      string `json:"status"`
	// SavingsUSDBase stays zero when the local run has no provider-counted,
	// outcome-evaluated counterfactual. A tokenizer delta is not dollar savings.
	SavingsUSDBase float64 `json:"savings_usd_base"`
	Confidence     string  `json:"confidence"`
}

type Learning struct {
	ID              string `json:"id"`
	Text            string `json:"text"`
	SourceKind      string `json:"source_kind"`
	Confidence      string `json:"confidence"`
	StoredInCavemem bool   `json:"stored_in_cavemem"`
}

type UsageEvent struct {
	SourceKind               string
	SourcePath               string
	EventID                  string
	Timestamp                string
	AgentSlug                string
	Provider                 string
	Model                    string
	Requests                 int64
	InputTokens              int64
	OutputTokens             int64
	CachedInputTokens        int64
	CacheCreationInputTokens int64
	ReasoningTokens          int64
	TotalCostUSD             float64
	Basis                    string
	MetadataJSON             string
}

type QuotaEvent struct {
	Provider     string
	PlanType     string
	Window       string
	UsedPct      float64
	ResetsAt     string
	Basis        string
	SourceKind   string
	ObservedAt   string
	MetadataJSON string
}
