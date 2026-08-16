package store

// learn_types.go defines the public `caveman.learn.v1` contract emitted by
// `caveman learn report --json` and rendered into the static HTML report. It is the
// local setup-profiler analogue of the cloud Cave Plan: a Cave Score plus a flat,
// ranked list of token sinks. Everything here is `inferred` — learn never verifies.

const (
	learnSchema = "caveman.learn.v1"
	learnBasis  = "inferred"

	// Sink classes drive presentation and what the editing skill may touch.
	classReducible        = "reducible"         // mechanical, net-token-negative fix exists
	classBehavioral       = "behavioral"        // measured habit; fix is a behavior change
	classLoadBearing      = "load_bearing"      // measured config the user needs; named for honesty
	classRecurringContext = "recurring_context" // heavy content re-established across sessions; fix = cavemem_offload

	// Framing keeps the honesty boundary explicit per sink.
	framingForward    = "forward_rate" // current rate going forward; never "wasted N"
	framingHistorical = "historical"   // measured at-the-time from real sessions

	// Cave Score component keys (always present so the score is never a black box).
	scoreKeyConfigTax = "config_tax"
	scoreKeyDumbzone  = "dumbzone"
	scoreKeyDeadLoad  = "dead_load"
	scoreKeySubagent  = "subagent_pressure"

	// Retro families. Each is a distinct measurement method; they never blend and
	// a family with nothing measured is omitted rather than emitted as a zero.
	retroFamilyToolOutputs    = "tool_outputs"
	retroFamilyRepeatedBlocks = "repeated_blocks"

	// Retro observed-token sources. `session_usage` is provider-counted; the
	// estimate is only legal when no scanned session carried provider usage.
	retroSourceSessionUsage = "session_usage"
)

// LearnPlan is the top-level report.
type LearnPlan struct {
	Schema           string         `json:"schema"`
	Basis            string         `json:"basis"`
	Window           LearnWindow    `json:"window"`
	CaveScore        CaveScore      `json:"cave_score"`
	SessionsScanned  int            `json:"sessions_scanned"`
	SessionsBySource map[string]int `json:"sessions_by_source"`
	Sinks            []Sink         `json:"sinks"`
	Caveats          []string       `json:"caveats"`
	// Retro is present only when the retrospective pass ran (`--retro`) AND it
	// had something honest to report. Additive optional field: the schema stays
	// `caveman.learn.v1`.
	Retro *LearnRetro `json:"retro,omitempty"`
	// ContextDepth is present only when at least one scanned session carried
	// usage data. Additive optional field: the schema stays `caveman.learn.v1`.
	ContextDepth *LearnContextDepth `json:"context_depth,omitempty"`
	// WrapMeasured is present only when the proxy recorded wrap activity in the
	// window. Additive optional field: the schema stays `caveman.learn.v1`.
	WrapMeasured *LearnWrapMeasured `json:"wrap_measured,omitempty"`
}

// LearnWrapMeasured sums what the proxy itself recorded inside the window:
// tokens the wrap actually cut on requests it compressed, plus what observe
// mode measured it would have cut without transforming. Counts are the
// engine's o200k estimates summed over recorded rows — token volume only,
// never a dollar, and never blended with the transcript-replay Retro block
// (the two measure different requests by different methods).
type LearnWrapMeasured struct {
	Basis string `json:"basis"` // compression token count basis, e.g. estimated_engine_o200k
	// WindowDays names the interval the sums cover; 0 means the since
	// expression did not bound it and the sums are all-time.
	WindowDays int   `json:"window_days"`
	Requests   int64 `json:"requests"`
	CompressedRequests int64  `json:"compressed_requests"`
	TokensBefore       int64  `json:"tokens_before"`
	TokensAfter        int64  `json:"tokens_after"`
	TokensSaved        int64  `json:"tokens_saved"`
	// WouldSaveTokens is observe-mode's report-only measurement on rows that
	// were never transformed; it is disjoint from TokensSaved by construction
	// (a row books one or the other, never both).
	WouldSaveTokens int64 `json:"would_save_tokens"`
}

// LearnContextDepth summarizes how deep scanned sessions ran into their model's
// context window: each session's PEAK context share, bucketed in tens. Window
// sizes are assumed per-provider defaults (the dumbzone caveat applies), and
// every number is transcript arithmetic, never a projection.
type LearnContextDepth struct {
	Sessions  int   `json:"sessions"`
	Over30Pct int   `json:"over_30_pct"`
	Over50Pct int   `json:"over_50_pct"`
	Buckets   []int `json:"buckets"` // 10 buckets: peak share 0-10%, 10-20%, ... 90-100%+
}

// LearnRetro is the retrospective "would-have-saved" block for the scanned
// window: what the sessions actually sent, and what the engine measures it
// would have cut. Every number is a sum over sessions actually read — nothing
// here is extrapolated to unscanned sessions, to a month, or to a dollar.
// Provider usage is counted once per API response (message id), never once per
// transcript line — Claude Code repeats one response's usage across its
// per-content-block lines.
//
// WouldCutTokens counts each unique tool-output cut once plus a conservative
// repeated-block lower bound (sum of occurrence sizes minus the largest
// normalized variant). WouldCutStreamTokens re-weights each actual later
// occurrence size by provider-counted turns of the same session — the
// like-for-like figure against TokensObserved, which also counts every re-send.
// Undated repeated blocks stay out of the stream figure because their first
// occurrence cannot be ordered honestly. Because the transcript cannot observe mid-session eviction,
// believed residency is capped by each turn's own provider-counted size
// (oldest blocks evicted first) and cleared at compaction or end of session.
// Both figures are transcript arithmetic, never projections, and both are
// token volume only: what a re-sent token costs depends on provider caching,
// so no dollar is ever derived from either.
type LearnRetro struct {
	Basis                string `json:"basis"`
	WindowDays           int    `json:"window_days"`
	SessionsTotal        int    `json:"sessions_total"`
	SessionsScanned      int    `json:"sessions_scanned"`
	TurnsObserved        int    `json:"turns_observed"`
	TokensObserved       int64  `json:"tokens_observed"`
	TokensObservedSource string `json:"tokens_observed_source"`
	WouldCutTokens       int64  `json:"would_cut_tokens"`
	// Always emitted (no omitempty): a measured zero must stay distinguishable
	// from a proxy that predates the field.
	WouldCutStreamTokens int64              `json:"would_cut_stream_tokens"`
	Families             []LearnRetroFamily `json:"families"`
	// ConfigPrefixTokensPerTurn is a forward per-turn RATE, never summed into
	// WouldCutTokens: trimming config is a different fix from wrap compression
	// and claiming both would double-count the same context.
	ConfigPrefixTokensPerTurn int      `json:"config_prefix_tokens_per_turn"`
	EngineUsed                bool     `json:"engine_used"`
	TimeBoxed                 bool     `json:"time_boxed"`
	Caveats                   []string `json:"caveats"`
}

// LearnRetroFamily is one measurement method's contribution to WouldCutTokens.
// Label names the fix that delivers it, because the two families are delivered
// by different mechanisms — the wrap compresses tool outputs, `caveman learn`
// offloads re-pasted context to cavemem — and a headline that hid the
// difference would credit the wrap with savings it cannot deliver.
type LearnRetroFamily struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Tokens int64  `json:"tokens"`
}

type LearnWindow struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Since string `json:"since"`
}

// CaveScore mirrors the cloud Cave Score mechanic (start at 100, subtract capped
// penalties) but scores local setup leanness. It is `inferred` and distinct from
// the cloud spend-based Cave Score; it never carries a currency.
type CaveScore struct {
	Score      int              `json:"score"`
	Basis      string           `json:"basis"`
	Scope      string           `json:"scope"`
	Components []ScoreComponent `json:"components"`
}

// ScoreComponent is always emitted, even when a component could not be measured
// (penalty 0, Measured=false) — fail toward transparency, never a guessed number.
type ScoreComponent struct {
	Key      string `json:"key"`
	Penalty  int    `json:"penalty"`
	Measured bool   `json:"measured"`
	Detail   string `json:"detail,omitempty"`
}

// Sink is one ranked token sink. Class A sinks are asserted as facts; Class B sinks
// carry softened suggestions with their evidence attached.
type Sink struct {
	SinkID           string         `json:"sink_id"`
	PracticeID       string         `json:"practice_id"`
	Title            string         `json:"title"`
	Class            string         `json:"class"`
	Basis            string         `json:"basis"`
	TokensPerTurn    int64          `json:"tokens_per_turn"`
	TokensPerDayRate int64          `json:"tokens_per_day_rate"`
	Evidence         map[string]any `json:"evidence"`
	Suggestion       string         `json:"suggestion"`
	Framing          string         `json:"framing"`
}

// ConfigSnapshot is one measured config-tax source, persisted to config_snapshots
// for evidence/export. Tokens are an `inferred` estimate.
type ConfigSnapshot struct {
	Scope        string
	Path         string
	Kind         string
	Lines        int
	Tokens       int
	ObservedAt   string
	MetadataJSON string
}
