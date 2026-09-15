package store

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

// StatsReportOptions selects one bounded historical window. Zero Days means all
// recorded history. Filters are exact identities, never model-family aliases.
type StatsReportOptions struct {
	Days                             int
	Provider, Model, Agent, AuthMode string
	Now                              time.Time
}

type StatsWindow struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Days     int    `json:"days"`
	Lifetime bool   `json:"lifetime"`
}

// StatsMetrics keeps token measurements and money estimates separate. A nil
// money field means no eligible priced observation, including for empty stores.
// SavedTokens is signed: request overhead must remain visible as a regression.
type StatsMetrics struct {
	Requests                  int64    `json:"requests"`
	SuccessfulRequests        int64    `json:"successful_requests"`
	FailedRequests            int64    `json:"failed_requests"`
	CompleteUsageRequests     int64    `json:"complete_usage_requests"`
	PartialUsageRequests      int64    `json:"partial_usage_requests"`
	InputTokens               int64    `json:"input_tokens"`
	OutputTokens              int64    `json:"output_tokens"`
	CacheReadTokens           int64    `json:"cache_read_tokens"`
	CacheWriteTokens          int64    `json:"cache_write_tokens"`
	CacheWrite1hTokens        int64    `json:"cache_write_1h_tokens"`
	ReasoningTokens           int64    `json:"reasoning_tokens"`
	MeasuredRequests          int64    `json:"measured_requests"`
	UnmeasuredRequests        int64    `json:"unmeasured_requests"`
	BeforeTokens              int64    `json:"before_tokens"`
	AfterTokens               int64    `json:"after_tokens"`
	SavedTokens               int64    `json:"saved_tokens"`
	ExpandedRequests          int64    `json:"expanded_requests"`
	LegacyRequests            int64    `json:"legacy_requests"`
	LegacySavedTokens         int64    `json:"legacy_saved_tokens"`
	ObserveWouldSaveTokens    int64    `json:"observe_would_save_tokens"`
	PricedRequests            int64    `json:"priced_requests"`
	UnpricedRequests          int64    `json:"unpriced_requests"`
	APISpendRequests          int64    `json:"api_spend_requests"`
	APISavingsRequests        int64    `json:"api_savings_requests"`
	EquivalentSpendRequests   int64    `json:"equivalent_spend_requests"`
	EquivalentSavingsRequests int64    `json:"equivalent_savings_requests"`
	UnknownAuthRequests       int64    `json:"unknown_auth_requests"`
	APISpendUSD               *float64 `json:"api_spend_usd"`
	APISavingsUSD             *float64 `json:"api_savings_usd"`
	APIEquivalentSpendUSD     *float64 `json:"api_equivalent_spend_usd"`
	APIEquivalentSavingsUSD   *float64 `json:"api_equivalent_savings_usd"`
}

// StatsGroup is an additive cell. Joint dimensions let the dashboard filter
// dates and identities without joining incompatible marginal totals.
type StatsGroup struct {
	Day      string `json:"day,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Agent    string `json:"agent,omitempty"`
	AuthMode string `json:"auth_mode,omitempty"`
	StatsMetrics
}

type StatsPrice struct {
	Provider               string  `json:"provider"`
	Model                  string  `json:"model"`
	CatalogVersion         string  `json:"catalog_version"`
	InputPerMillion        float64 `json:"input_per_million"`
	OutputPerMillion       float64 `json:"output_per_million"`
	CacheReadPerMillion    float64 `json:"cache_read_per_million"`
	CacheWritePerMillion   float64 `json:"cache_write_per_million"`
	CacheWrite1hPerMillion float64 `json:"cache_write_1h_per_million"`
	ReasoningPerMillion    float64 `json:"reasoning_per_million"`
}

type StatsReceipt struct {
	ID                           string      `json:"id"`
	Timestamp                    string      `json:"timestamp"`
	Provider                     string      `json:"provider"`
	Model                        string      `json:"model"`
	Agent                        string      `json:"agent"`
	AuthMode                     string      `json:"auth_mode"`
	StatusCode                   int         `json:"status_code"`
	MeasurementStatus            string      `json:"measurement_status"`
	MeasurementBasis             string      `json:"measurement_basis"`
	SavingsBasis                 string      `json:"savings_basis"`
	TokenUsageBasis              string      `json:"token_usage_basis"`
	TokensBefore                 int64       `json:"tokens_before"`
	TokensAfter                  int64       `json:"tokens_after"`
	SavedTokens                  *int64      `json:"saved_tokens"`
	EstimatedInputDeltaUSD       *float64    `json:"estimated_input_delta_usd"`
	EffectiveInputRatePerMillion *float64    `json:"effective_input_rate_per_million"`
	Price                        *StatsPrice `json:"price"`
	RawRequestSHA256             string      `json:"raw_request_sha256,omitempty"`
	TransformedRequestSHA256     string      `json:"transformed_request_sha256,omitempty"`
	StatsMetrics
}

type StatsEvidence struct {
	Basis               string           `json:"basis"`
	TokenBasis          string           `json:"token_basis"`
	SpendBasis          string           `json:"spend_basis"`
	CatalogVersions     []string         `json:"catalog_versions"`
	MeasurementStatuses map[string]int64 `json:"measurement_statuses"`
	Caveats             []string         `json:"caveats"`
	ReceiptsLimit       int              `json:"receipts_limit"`
	ReceiptsTruncated   bool             `json:"receipts_truncated"`
}

type StatsReport struct {
	Schema      string         `json:"schema"`
	GeneratedAt string         `json:"generated_at"`
	Window      StatsWindow    `json:"window"`
	Totals      StatsMetrics   `json:"totals"`
	Groups      []StatsGroup   `json:"groups"`
	Daily       []StatsGroup   `json:"daily"`
	Providers   []StatsGroup   `json:"providers"`
	Models      []StatsGroup   `json:"models"`
	Agents      []StatsGroup   `json:"agents"`
	AuthModes   []StatsGroup   `json:"auth_modes"`
	Receipts    []StatsReceipt `json:"receipts"`
	Evidence    StatsEvidence  `json:"evidence"`
}

const statsReceiptLimit = 200

// The persistence boundary validates the additive ledger independently of the
// gateway. Unknown and incomplete observations must not turn into priced zeroes.
func sanitizeStatsMeasurement(rec *gateway.RequestRecord) {
	switch rec.RequestMeasurementStatus {
	case "measured", "failed", "unsupported_payload", "recovery_multiple_calls", "tokenizer_unavailable", "request_payload_too_large":
	case "":
	default:
		rec.RequestMeasurementStatus = "unsupported_payload"
	}
	if rec.RequestTokensBefore < 0 || rec.RequestTokensAfter < 0 || rec.RequestTokenBasis != "estimated_request_json_o200k_v1" {
		rec.RequestTokensBefore, rec.RequestTokensAfter = 0, 0
		rec.RequestTokenBasis = ""
		if rec.RequestMeasurementStatus == "measured" {
			rec.RequestMeasurementStatus = "unsupported_payload"
		}
	}
	if rec.StatusCode < 200 || rec.StatusCode >= 300 || rec.ErrorCode != "" {
		if rec.RequestMeasurementStatus != "" {
			rec.RequestMeasurementStatus = "failed"
		}
	}
	p := cost.Price{InputPerMillion: rec.PriceInputPerMillion, OutputPerMillion: rec.PriceOutputPerMillion, CacheReadPerMillion: rec.PriceCacheReadPerMillion, CacheWritePerMillion: rec.PriceCacheWritePerMillion, CacheWrite1hPerMillion: rec.PriceCacheWrite1hPerMillion, ReasoningPerMillion: rec.PriceReasoningPerMillion}
	if !cost.ValidPrice(p) || (p.InputPerMillion <= 0 && p.OutputPerMillion <= 0) || rec.PricingProvider == "" || rec.PricingModel == "" || rec.PricingCatalogVersion == "" || strings.HasPrefix(rec.PricingCatalogVersion, "unpriced:") || !statsPriceBucketsKnown(p, int64(rec.InputTokens), int64(rec.OutputTokens), int64(rec.CachedInputTokens), int64(rec.CacheCreationInputTokens), int64(rec.CacheCreation1hTokens), int64(rec.ReasoningTokens)) {
		rec.PricingKnown = false
	}
	if !rec.PricingKnown {
		rec.PriceInputPerMillion, rec.PriceOutputPerMillion, rec.PriceCacheReadPerMillion, rec.PriceCacheWritePerMillion, rec.PriceCacheWrite1hPerMillion, rec.PriceReasoningPerMillion = 0, 0, 0, 0, 0, 0
	}
	if rec.RequestEstimatedInputDeltaUSD != nil && (!rec.PricingKnown || rec.RequestMeasurementStatus != "measured" || rec.TokenUsageBasis != "provider_complete" || rec.RequestSavingsBasis != "estimated_tokens_x_observed_cache_mix" || !finiteStatsNumber(*rec.RequestEstimatedInputDeltaUSD)) {
		rec.RequestEstimatedInputDeltaUSD = nil
	}
	if rec.RequestEstimatedInputDeltaUSD != nil {
		delta := rec.RequestTokensBefore - rec.RequestTokensAfter
		if delta == 0 {
			zero := 0.0
			rec.RequestEstimatedInputDeltaUSD = &zero
		} else if rate, ok := statsEffectiveInputRate(p, int64(rec.InputTokens), int64(rec.CachedInputTokens), int64(rec.CacheCreationInputTokens), int64(rec.CacheCreation1hTokens)); ok {
			value := float64(delta) / 1_000_000 * rate
			if finiteStatsNumber(value) && math.Abs(value) <= math.MaxFloat64/10_000_000_000 {
				value = cost.RoundUSD(value)
				rec.RequestEstimatedInputDeltaUSD = &value
			} else {
				rec.RequestEstimatedInputDeltaUSD = nil
			}
		} else {
			rec.RequestEstimatedInputDeltaUSD = nil
		}
	}
}

// BuildStatsReport reads the request ledger only. Transcript imports are not
// unioned into this report: without an exact request join that would double count
// routed traffic and attach savings to unrelated agent usage.
func (s *Store) BuildStatsReport(opts StatsReportOptions) (StatsReport, error) {
	if opts.Days < 0 || opts.Days > 3660 {
		return StatsReport{}, fmt.Errorf("stats days must be between 0 and 3660")
	}
	now := opts.Now.UTC()
	if opts.Now.IsZero() {
		now = time.Now().UTC()
	}
	out := StatsReport{
		Schema: "caveman.stats.v1", GeneratedAt: now.Format(time.RFC3339),
		Window: StatsWindow{To: now.Format(time.RFC3339), Days: opts.Days, Lifetime: opts.Days == 0},
		Groups: []StatsGroup{}, Daily: []StatsGroup{}, Providers: []StatsGroup{}, Models: []StatsGroup{}, Agents: []StatsGroup{}, AuthModes: []StatsGroup{}, Receipts: []StatsReceipt{},
		Evidence: StatsEvidence{Basis: "inferred", TokenBasis: "estimated_request_json_o200k_v1", SpendBasis: "provider_counted_x_snapshotted_published_rate", CatalogVersions: []string{}, MeasurementStatuses: map[string]int64{}, ReceiptsLimit: statsReceiptLimit},
	}
	// Older ChatGPT rows used RFC3339 while the generic route used storeTSLayout.
	// Compare instants, not their differing separators or timezone offsets.
	where := []string{"julianday(ts) <= julianday(?)"}
	args := []any{now.Format(time.RFC3339Nano)}
	if opts.Days > 0 {
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1-opts.Days)
		out.Window.From = from.Format(time.RFC3339)
		where = append(where, "julianday(ts) >= julianday(?)")
		args = append(args, from.Format(time.RFC3339))
	}
	for _, filter := range []struct{ column, value string }{
		{"COALESCE(NULLIF(provider,''),'unknown')", opts.Provider},
		{"COALESCE(NULLIF(model,''),'unknown')", opts.Model},
		{"COALESCE(NULLIF(agent_slug,''),'unlabeled-agent')", opts.Agent},
		{"COALESCE(NULLIF(auth_mode,''),'unknown')", opts.AuthMode},
	} {
		if filter.value != "" {
			where = append(where, filter.column+" = ?")
			args = append(args, filter.value)
		}
	}
	rows, err := s.db.Query(`SELECT request_id, ts, COALESCE(provider,''), COALESCE(model,''), COALESCE(agent_slug,''), COALESCE(auth_mode,'unknown'),
		COALESCE(status_code,0), COALESCE(error_code,''), COALESCE(token_usage_basis,'unavailable'),
		COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cached_input_tokens,0), COALESCE(cache_creation_input_tokens,0), COALESCE(cache_creation_1h_tokens,0), COALESCE(reasoning_tokens,0),
		COALESCE(compression_tokens_before,0), COALESCE(compression_tokens_after,0), COALESCE(would_save_tokens,0),
		COALESCE(request_tokens_before,0), COALESCE(request_tokens_after,0), COALESCE(request_token_basis,''), COALESCE(request_measurement_status,''),
		request_estimated_input_delta_usd, COALESCE(request_savings_basis,''), COALESCE(pricing_known,0),
		COALESCE(pricing_provider,''), COALESCE(pricing_model,''), COALESCE(pricing_catalog_version,''),
		COALESCE(price_input_per_million,0), COALESCE(price_output_per_million,0), COALESCE(price_cache_read_per_million,0), COALESCE(price_cache_write_per_million,0), COALESCE(price_cache_write_1h_per_million,0), COALESCE(price_reasoning_per_million,0),
		COALESCE(raw_request_sha256,''), COALESCE(transformed_request_sha256,'')
		FROM requests WHERE `+strings.Join(where, " AND ")+` ORDER BY julianday(ts) DESC, id DESC`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	groups := map[string]*StatsGroup{}
	versions := map[string]bool{}
	for rows.Next() {
		var r StatsReceipt
		var errorCode string
		var input, output, read, write, write1h, reasoning, legacyBefore, legacyAfter, observe int64
		var delta sql.NullFloat64
		var known bool
		var price StatsPrice
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.Provider, &r.Model, &r.Agent, &r.AuthMode, &r.StatusCode, &errorCode, &r.TokenUsageBasis,
			&input, &output, &read, &write, &write1h, &reasoning, &legacyBefore, &legacyAfter, &observe,
			&r.TokensBefore, &r.TokensAfter, &r.MeasurementBasis, &r.MeasurementStatus, &delta, &r.SavingsBasis, &known,
			&price.Provider, &price.Model, &price.CatalogVersion, &price.InputPerMillion, &price.OutputPerMillion, &price.CacheReadPerMillion, &price.CacheWritePerMillion, &price.CacheWrite1hPerMillion, &price.ReasoningPerMillion,
			&r.RawRequestSHA256, &r.TransformedRequestSHA256); err != nil {
			return out, err
		}
		for _, layout := range []string{time.RFC3339Nano, storeTSLayout, time.DateTime} {
			if instant, err := time.Parse(layout, r.Timestamp); err == nil {
				r.Timestamp = instant.UTC().Format(time.RFC3339Nano)
				break
			}
		}
		if r.Provider == "" {
			r.Provider = "unknown"
		}
		if r.Model == "" {
			r.Model = "unknown"
		}
		if r.Agent == "" {
			r.Agent = "unlabeled-agent"
		}
		if r.AuthMode == "" {
			r.AuthMode = "unknown"
		}
		if r.MeasurementStatus == "" {
			r.MeasurementStatus = "legacy_unmeasured"
		}
		m := StatsMetrics{Requests: 1, ObserveWouldSaveTokens: max(observe, 0)}
		success := r.StatusCode >= 200 && r.StatusCode < 300 && errorCode == ""
		if success {
			m.SuccessfulRequests = 1
		} else {
			m.FailedRequests = 1
		}
		validUsage := input >= 0 && output >= 0 && read >= 0 && write >= 0 && write1h >= 0 && reasoning >= 0 && read <= input && write <= input-read && write1h <= write && reasoning <= output
		complete := r.TokenUsageBasis == "provider_complete" && validUsage
		if complete {
			m.CompleteUsageRequests = 1
		} else {
			m.PartialUsageRequests = 1
		}
		if validUsage {
			m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.CacheWrite1hTokens, m.ReasoningTokens = input, output, read, write, write1h, reasoning
		}
		measured := success && r.MeasurementStatus == "measured" && r.MeasurementBasis == "estimated_request_json_o200k_v1" && r.TokensBefore >= 0 && r.TokensAfter >= 0
		if measured {
			m.MeasuredRequests, m.BeforeTokens, m.AfterTokens = 1, r.TokensBefore, r.TokensAfter
			m.SavedTokens = r.TokensBefore - r.TokensAfter
			r.SavedTokens = &m.SavedTokens
			if m.SavedTokens < 0 {
				m.ExpandedRequests = 1
			}
		} else {
			m.UnmeasuredRequests = 1
			if r.MeasurementStatus == "legacy_unmeasured" && success && legacyBefore > legacyAfter && legacyAfter >= 0 {
				m.LegacyRequests, m.LegacySavedTokens = 1, legacyBefore-legacyAfter
			}
		}
		p := price.costPrice()
		priceKnown := known && price.Provider != "" && price.Model != "" && price.CatalogVersion != "" && !strings.HasPrefix(price.CatalogVersion, "unpriced:") && cost.ValidPrice(p) && (p.InputPerMillion > 0 || p.OutputPerMillion > 0) && statsPriceBucketsKnown(p, input, output, read, write, write1h, reasoning)
		if complete && priceKnown {
			m.PricedRequests = 1
			r.Price = &price
			versions[price.CatalogVersion] = true
		} else {
			m.UnpricedRequests = 1
		}
		api := providers.ListPriceEligible(r.Provider, r.AuthMode)
		equivalent := !api && (r.AuthMode == "oauth" || r.AuthMode == "subscription")
		if !api && !equivalent {
			m.UnknownAuthRequests = 1
		}
		if m.PricedRequests == 1 {
			if rate, ok := statsEffectiveInputRate(p, input, read, write, write1h); ok {
				r.EffectiveInputRatePerMillion = &rate
			}
			spend := cost.EstimateUSD(p, cost.Usage{InputTokens: int(input - read - write), OutputTokens: int(output - reasoning), CachedInputTokens: int(read), CacheCreationTokens: int(write - write1h), CacheCreation1hTokens: int(write1h), ReasoningTokens: int(reasoning)})
			if api {
				m.APISpendUSD, m.APISpendRequests = &spend, 1
			} else if equivalent {
				m.APIEquivalentSpendUSD, m.EquivalentSpendRequests = &spend, 1
			}
			if measured && delta.Valid && finiteStatsNumber(delta.Float64) && r.SavingsBasis != "" {
				v := delta.Float64
				r.EstimatedInputDeltaUSD = &v
				if api {
					m.APISavingsUSD, m.APISavingsRequests = &v, 1
				} else if equivalent {
					m.APIEquivalentSavingsUSD, m.EquivalentSavingsRequests = &v, 1
				}
			}
		}
		r.StatsMetrics = m
		out.Totals.add(m)
		out.Evidence.MeasurementStatuses[r.MeasurementStatus]++
		day := r.Timestamp
		if len(day) > 10 {
			day = day[:10]
		}
		key := strings.Join([]string{day, r.Provider, r.Model, r.Agent, r.AuthMode}, "\x00")
		g := groups[key]
		if g == nil {
			g = &StatsGroup{Day: day, Provider: r.Provider, Model: r.Model, Agent: r.Agent, AuthMode: r.AuthMode}
			groups[key] = g
		}
		g.add(m)
		if len(out.Receipts) < statsReceiptLimit {
			out.Receipts = append(out.Receipts, r)
		} else {
			out.Evidence.ReceiptsTruncated = true
		}
		if opts.Days == 0 {
			out.Window.From = r.Timestamp
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, g := range groups {
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		return strings.Join([]string{a.Day, a.Provider, a.Model, a.Agent, a.AuthMode}, "\x00") < strings.Join([]string{b.Day, b.Provider, b.Model, b.Agent, b.AuthMode}, "\x00")
	})
	out.Daily = statsRollup(out.Groups, "day")
	out.Providers = statsRollup(out.Groups, "provider")
	out.Models = statsRollup(out.Groups, "model")
	out.Agents = statsRollup(out.Groups, "agent")
	out.AuthModes = statsRollup(out.Groups, "auth")
	for v := range versions {
		out.Evidence.CatalogVersions = append(out.Evidence.CatalogVersions, v)
	}
	sort.Strings(out.Evidence.CatalogVersions)
	out.Evidence.Caveats = []string{
		"Request token deltas re-count normalized original and forwarded JSON with the same offline o200k tokenizer. They include request overhead but are estimates, not the provider's tokenizer counts or invoice savings.",
		"Savings values apply the observed request cache mix to the measured token delta. The cache state and output of an uncompressed run were not observed; these are inferred counterfactuals.",
		"API spend prices provider-counted disjoint usage buckets at the effective published rates saved with each request. It is a list-rate estimate, not an invoice.",
		"OAuth and subscription API-equivalent values are separate comparisons. They do not measure subscription fees saved, remaining quota, or extra messages.",
		"Only routed requests recorded in this database appear here. Native file reads, imported transcripts, output-style savings, and earlier uninstrumented traffic have no measured request counterfactual.",
		"Legacy segment reductions and observe-only potential are separate from net request savings. Failed, unsupported, and recovery-multiple-call measurements do not enter savings totals. Negative deltas remain negative.",
		"A null dollar value means unavailable; partially priced totals cover only the stated eligible requests. Historical prices are never silently replaced with today's catalog.",
	}
	return out, nil
}

func (p StatsPrice) costPrice() cost.Price {
	return cost.Price{InputPerMillion: p.InputPerMillion, OutputPerMillion: p.OutputPerMillion, CacheReadPerMillion: p.CacheReadPerMillion, CacheWritePerMillion: p.CacheWritePerMillion, CacheWrite1hPerMillion: p.CacheWrite1hPerMillion, ReasoningPerMillion: p.ReasoningPerMillion}
}

func finiteStatsNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// The catalog's numeric projection does not distinguish a null rate from an
// advertised zero. A positive observed bucket therefore needs a positive rate;
// unsupported/free pricing stays unavailable instead of becoming guessed $0.
func statsPriceBucketsKnown(p cost.Price, input, output, read, write, write1h, reasoning int64) bool {
	if input < 0 || output < 0 || read < 0 || write < 0 || write1h < 0 || reasoning < 0 || read > input || write > input-read || write1h > write || reasoning > output {
		return false
	}
	for _, bucket := range []struct {
		tokens int64
		rate   float64
	}{
		{input - read - write, p.InputPerMillion}, {output - reasoning, p.OutputPerMillion},
		{read, p.CacheReadPerMillion}, {write - write1h, p.CacheWritePerMillion},
		{write1h, p.CacheWrite1hPerMillion}, {reasoning, p.ReasoningPerMillion},
	} {
		if bucket.tokens > 0 && bucket.rate <= 0 {
			return false
		}
	}
	return true
}

func statsEffectiveInputRate(p cost.Price, input, read, write, write1h int64) (float64, bool) {
	if input <= 0 || read < 0 || write < 0 || write1h < 0 || read > input || write > input-read || write1h > write {
		return 0, false
	}
	rate := (float64(input-read-write)*p.InputPerMillion + float64(read)*p.CacheReadPerMillion + float64(write-write1h)*p.CacheWritePerMillion + float64(write1h)*p.CacheWrite1hPerMillion) / float64(input)
	return rate, finiteStatsNumber(rate)
}

func addStatsMoney(dst **float64, src *float64) {
	if src == nil {
		return
	}
	if *dst == nil {
		v := *src
		*dst = &v
	} else {
		**dst = cost.RoundUSD(**dst + *src)
	}
}

func (m *StatsMetrics) add(v StatsMetrics) {
	m.Requests += v.Requests
	m.SuccessfulRequests += v.SuccessfulRequests
	m.FailedRequests += v.FailedRequests
	m.CompleteUsageRequests += v.CompleteUsageRequests
	m.PartialUsageRequests += v.PartialUsageRequests
	m.InputTokens += v.InputTokens
	m.OutputTokens += v.OutputTokens
	m.CacheReadTokens += v.CacheReadTokens
	m.CacheWriteTokens += v.CacheWriteTokens
	m.CacheWrite1hTokens += v.CacheWrite1hTokens
	m.ReasoningTokens += v.ReasoningTokens
	m.MeasuredRequests += v.MeasuredRequests
	m.UnmeasuredRequests += v.UnmeasuredRequests
	m.BeforeTokens += v.BeforeTokens
	m.AfterTokens += v.AfterTokens
	m.SavedTokens += v.SavedTokens
	m.ExpandedRequests += v.ExpandedRequests
	m.LegacyRequests += v.LegacyRequests
	m.LegacySavedTokens += v.LegacySavedTokens
	m.ObserveWouldSaveTokens += v.ObserveWouldSaveTokens
	m.PricedRequests += v.PricedRequests
	m.UnpricedRequests += v.UnpricedRequests
	m.APISpendRequests += v.APISpendRequests
	m.APISavingsRequests += v.APISavingsRequests
	m.EquivalentSpendRequests += v.EquivalentSpendRequests
	m.EquivalentSavingsRequests += v.EquivalentSavingsRequests
	m.UnknownAuthRequests += v.UnknownAuthRequests
	addStatsMoney(&m.APISpendUSD, v.APISpendUSD)
	addStatsMoney(&m.APISavingsUSD, v.APISavingsUSD)
	addStatsMoney(&m.APIEquivalentSpendUSD, v.APIEquivalentSpendUSD)
	addStatsMoney(&m.APIEquivalentSavingsUSD, v.APIEquivalentSavingsUSD)
}

func statsRollup(groups []StatsGroup, dimension string) []StatsGroup {
	by := map[string]*StatsGroup{}
	for _, g := range groups {
		key := ""
		row := StatsGroup{}
		switch dimension {
		case "day":
			key = g.Day
			row.Day = g.Day
		case "provider":
			key = g.Provider
			row.Provider = g.Provider
		case "model":
			key = g.Provider + "\x00" + g.Model
			row.Provider = g.Provider
			row.Model = g.Model
		case "agent":
			key = g.Agent
			row.Agent = g.Agent
		case "auth":
			key = g.AuthMode
			row.AuthMode = g.AuthMode
		}
		if by[key] == nil {
			by[key] = &row
		}
		by[key].add(g.StatsMetrics)
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]StatsGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, *by[k])
	}
	return out
}
