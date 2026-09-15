package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

// learn_pricing.go prices scanned history. It is the one place local learn may
// state a currency, and it may do so for exactly one reason: the token counts
// it multiplies are PROVIDER-COUNTED (read out of the transcript's own usage
// block, not estimated), and the rates come from the same dated, snapshot-pinned
// catalog the gateway bills with.
//
// The distinction that keeps this honest:
//
//   - SPEND is arithmetic. "These 4.1M cache-read tokens on this model cost $X
//     at published rates." No counterfactual, no causality, no projection.
//   - SAVINGS is a claim. It needs a before, an after, and an attribution
//     method — see learn_attribution.go. Savings dollars carry their rung.
//
// Neither is ever `verified`; that basis stays reserved for gateway-observed
// requests. Nothing here projects a window figure forward to a month.
const (
	spendBasis    = "provider_counted_x_published_rate"
	spendCurrency = "USD"
	// A model the catalog does not carry is NEVER priced by borrowing a
	// sibling's rate. Its tokens are reported separately so the headline stays
	// a floor rather than a guess.
	spendUnpricedPrefix = "unpriced:"
)

// modelDateSuffix matches the trailing snapshot date on provider model ids
// (claude-opus-5-20260401). Stripping it is the only alias step allowed: it
// resolves the SAME model, unlike a family fallback, which would price one
// model at another's rate.
var modelDateSuffix = regexp.MustCompile(`-\d{8}$`)

type spendModelKey struct {
	Provider string
	Model    string
}

type spendBuckets struct {
	Fresh      int64
	CacheRead  int64
	CacheWrite int64
	Output     int64
	Turns      int64
}

// spendAccumulator sums disjoint provider-counted billing buckets per model.
// It only ever sees turns whose source stated its buckets (BillingUsagePresent);
// an unstated bucket contributes nothing rather than a zero.
type spendAccumulator struct {
	byModel map[spendModelKey]*spendBuckets
}

func (a *spendAccumulator) observe(event turnEvent) {
	if !event.BillingUsagePresent {
		return
	}
	if a.byModel == nil {
		a.byModel = map[spendModelKey]*spendBuckets{}
	}
	key := spendModelKey{Provider: event.ProviderKey, Model: strings.TrimSpace(event.Model)}
	b := a.byModel[key]
	if b == nil {
		b = &spendBuckets{}
		a.byModel[key] = b
	}
	b.Fresh += int64(max(0, event.InputFreshTokens))
	b.CacheRead += int64(max(0, event.CacheReadInputTokens))
	b.CacheWrite += int64(max(0, event.CacheCreationInputTokens))
	b.Output += int64(max(0, event.OutputTokens))
	b.Turns++
}

func (a *spendAccumulator) merge(other spendAccumulator) {
	if len(other.byModel) == 0 {
		return
	}
	if a.byModel == nil {
		a.byModel = map[spendModelKey]*spendBuckets{}
	}
	for key, src := range other.byModel {
		dst := a.byModel[key]
		if dst == nil {
			dst = &spendBuckets{}
			a.byModel[key] = dst
		}
		dst.Fresh += src.Fresh
		dst.CacheRead += src.CacheRead
		dst.CacheWrite += src.CacheWrite
		dst.Output += src.Output
		dst.Turns += src.Turns
	}
}

// LearnSpendComponent is one billable bucket's tokens and cost. Components are
// disjoint and sum to the headline.
type LearnSpendComponent struct {
	Key    string  `json:"key"` // fresh_input | cache_read | cache_write | output
	Tokens int64   `json:"tokens"`
	USD    float64 `json:"usd"`
	// SharePct is the component's share of priced spend, 0-100.
	SharePct float64 `json:"share_pct"`
}

// LearnSpendModel is one model's priced contribution.
type LearnSpendModel struct {
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	CatalogVersion string  `json:"catalog_version"`
	Turns          int64   `json:"turns"`
	Tokens         int64   `json:"tokens"`
	USD            float64 `json:"usd"`
}

// LearnSpendUnpriced names a model whose tokens were measured but which the
// catalog does not carry. Its tokens are excluded from the headline; showing
// them separately keeps the headline an honest floor.
type LearnSpendUnpriced struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Tokens   int64  `json:"tokens"`
	Reason   string `json:"reason"`
}

// LearnSpend is the priced view of scanned history. Present only when at least
// one scanned turn carried provider-counted billing buckets AND its model is in
// the catalog. Never projected; never `verified`.
type LearnSpend struct {
	Basis          string `json:"basis"`
	Currency       string `json:"currency"`
	CatalogVersion string `json:"catalog_version"`
	WindowDays     int    `json:"window_days"`
	// USD is spend over the scanned window only. It is a floor: unpriced models
	// and sources without usage blocks contribute zero.
	USD        float64               `json:"usd"`
	Tokens     int64                 `json:"tokens"`
	Components []LearnSpendComponent `json:"components"`
	Models     []LearnSpendModel     `json:"models"`
	Unpriced   []LearnSpendUnpriced  `json:"unpriced,omitempty"`
	// EffectiveInputUSDPerMTok is what a million input tokens ACTUALLY cost this
	// user, after their real cache mix. It is the rate every input-side sink is
	// priced at, and the honest alternative to pricing everything at list.
	EffectiveInputUSDPerMTok float64 `json:"effective_input_usd_per_mtok"`
	// EffectiveInputMultiplier is that rate divided by the all-fresh rate: 1.0
	// means nothing was cached, ~0.1 means almost everything was a cache hit.
	// This is the single most actionable cost number in the report.
	EffectiveInputMultiplier float64  `json:"effective_input_multiplier"`
	Caveats                  []string `json:"caveats"`
}

// resolveModelPrice looks up a transcript model id, allowing exactly one alias
// step: stripping a trailing snapshot date, which names the same model. Any
// other miss is unpriced — never a sibling model's rate.
func resolveModelPrice(provider, model string) (cost.Price, string, bool) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return cost.Price{}, spendUnpricedPrefix + "no-model-id", false
	}
	price, version := catalog.Price(provider, model)
	if !strings.HasPrefix(version, spendUnpricedPrefix) && cost.ValidPrice(price) {
		return price, version, true
	}
	if base := modelDateSuffix.ReplaceAllString(model, ""); base != model {
		price, version = catalog.Price(provider, base)
		if !strings.HasPrefix(version, spendUnpricedPrefix) && cost.ValidPrice(price) {
			return price, version, true
		}
	}
	return cost.Price{}, spendUnpricedPrefix + provider + "/" + model, false
}

// buildLearnSpend prices the accumulated buckets. Returns nil when nothing
// priceable was measured — an empty spend card is worse than none.
func buildLearnSpend(acc spendAccumulator, windowDays int) *LearnSpend {
	if len(acc.byModel) == 0 {
		return nil
	}
	keys := make([]spendModelKey, 0, len(acc.byModel))
	for key := range acc.byModel {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Provider != keys[j].Provider {
			return keys[i].Provider < keys[j].Provider
		}
		return keys[i].Model < keys[j].Model
	})

	spend := &LearnSpend{
		Basis: spendBasis, Currency: spendCurrency, WindowDays: windowDays,
	}
	var fresh, cacheRead, cacheWrite, output int64
	var freshUSD, cacheReadUSD, cacheWriteUSD, outputUSD float64
	// allFreshInputUSD prices every input token as if none had been cached; it
	// is the denominator of the effective multiplier.
	var allFreshInputUSD float64
	versions := map[string]bool{}

	for _, key := range keys {
		b := acc.byModel[key]
		price, version, ok := resolveModelPrice(key.Provider, key.Model)
		if !ok {
			total := b.Fresh + b.CacheRead + b.CacheWrite + b.Output
			if total > 0 {
				spend.Unpriced = append(spend.Unpriced, LearnSpendUnpriced{
					Provider: key.Provider, Model: key.Model, Tokens: total,
					Reason: "no catalog row for this model; tokens excluded from the priced total",
				})
			}
			continue
		}
		versions[version] = true
		mFresh := cost.EstimateUSD(price, cost.Usage{InputTokens: int(b.Fresh)})
		mRead := cost.EstimateUSD(price, cost.Usage{CachedInputTokens: int(b.CacheRead)})
		mWrite := cost.EstimateUSD(price, cost.Usage{CacheCreationTokens: int(b.CacheWrite)})
		mOut := cost.EstimateUSD(price, cost.Usage{OutputTokens: int(b.Output)})

		fresh += b.Fresh
		cacheRead += b.CacheRead
		cacheWrite += b.CacheWrite
		output += b.Output
		freshUSD += mFresh
		cacheReadUSD += mRead
		cacheWriteUSD += mWrite
		outputUSD += mOut
		allFreshInputUSD += cost.EstimateUSD(price, cost.Usage{InputTokens: int(b.Fresh + b.CacheRead + b.CacheWrite)})

		spend.Models = append(spend.Models, LearnSpendModel{
			Provider: key.Provider, Model: key.Model, CatalogVersion: version,
			Turns: b.Turns, Tokens: b.Fresh + b.CacheRead + b.CacheWrite + b.Output,
			USD: cost.RoundUSD(mFresh + mRead + mWrite + mOut),
		})
	}

	total := cost.RoundUSD(freshUSD + cacheReadUSD + cacheWriteUSD + outputUSD)
	// Nothing priced AND nothing to disclose means there is no card to show.
	// Nothing priced but tokens excluded is a REAL finding — the user is
	// running a model this build cannot price — and must not vanish.
	if total <= 0 && len(spend.Models) == 0 && len(spend.Unpriced) == 0 {
		return nil
	}
	spend.USD = total
	spend.Tokens = fresh + cacheRead + cacheWrite + output
	spend.CatalogVersion = joinCatalogVersions(versions)
	spend.Components = spendComponents(total, []LearnSpendComponent{
		{Key: "fresh_input", Tokens: fresh, USD: cost.RoundUSD(freshUSD)},
		{Key: "cache_read", Tokens: cacheRead, USD: cost.RoundUSD(cacheReadUSD)},
		{Key: "cache_write", Tokens: cacheWrite, USD: cost.RoundUSD(cacheWriteUSD)},
		{Key: "output", Tokens: output, USD: cost.RoundUSD(outputUSD)},
	})

	inputTokens := fresh + cacheRead + cacheWrite
	inputUSD := freshUSD + cacheReadUSD + cacheWriteUSD
	if inputTokens > 0 && inputUSD > 0 {
		spend.EffectiveInputUSDPerMTok = roundRate(inputUSD * 1_000_000 / float64(inputTokens))
		if allFreshInputUSD > 0 {
			spend.EffectiveInputMultiplier = roundMultiplier(inputUSD / allFreshInputUSD)
		}
	}

	sort.Slice(spend.Models, func(i, j int) bool { return spend.Models[i].USD > spend.Models[j].USD })
	sort.Slice(spend.Unpriced, func(i, j int) bool { return spend.Unpriced[i].Tokens > spend.Unpriced[j].Tokens })

	spend.Caveats = append(spend.Caveats,
		"Spend is provider-counted tokens priced at published list rates from the dated catalog. It is arithmetic over the scanned window, not an invoice, and it is never projected forward.",
		"If this traffic ran on a subscription plan (Claude Max, ChatGPT Plus, Gemini Advanced) its marginal cost is zero; the figure is then the API-equivalent value of the tokens, not money spent.",
	)
	if len(spend.Unpriced) > 0 {
		spend.Caveats = append(spend.Caveats,
			"Some scanned models are not in the catalog; their tokens are listed separately and excluded, so the total is a floor.")
	}
	return spend
}

func spendComponents(total float64, comps []LearnSpendComponent) []LearnSpendComponent {
	out := make([]LearnSpendComponent, 0, len(comps))
	for _, c := range comps {
		if c.Tokens <= 0 && c.USD <= 0 {
			continue
		}
		if total > 0 {
			c.SharePct = roundPct(c.USD * 100 / total)
		}
		out = append(out, c)
	}
	return out
}

func joinCatalogVersions(versions map[string]bool) string {
	if len(versions) == 0 {
		return ""
	}
	list := make([]string, 0, len(versions))
	for v := range versions {
		list = append(list, v)
	}
	sort.Strings(list)
	if len(list) == 1 {
		return list[0]
	}
	return "mixed:" + strings.Join(list, ",")
}

// priceInputTokens converts a token quantity into USD at the user's own
// measured effective input rate. Every sink in the report is input-side
// (context re-sent to the model), so one rate covers them, and using the
// MEASURED rate rather than list price is what stops a heavily-cached user's
// findings from being inflated ~10x.
func (s *LearnSpend) priceInputTokens(tokens int64) float64 {
	if s == nil || tokens <= 0 || s.EffectiveInputUSDPerMTok <= 0 {
		return 0
	}
	return cost.RoundUSD(float64(tokens) * s.EffectiveInputUSDPerMTok / 1_000_000)
}

func roundRate(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return float64(int64(v*10_000+0.5)) / 10_000
}

func roundPct(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func roundMultiplier(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int64(v*1000+0.5)) / 1000
}

// effectiveInputSummary renders the multiplier as a one-line human verdict.
// Thresholds are deliberately coarse: this is a headline, not a grade.
func effectiveInputSummary(multiplier float64) string {
	switch {
	case multiplier <= 0:
		return ""
	case multiplier < 0.25:
		return fmt.Sprintf("input costs %.2fx list — cache is doing its job", multiplier)
	case multiplier < 0.6:
		return fmt.Sprintf("input costs %.2fx list — partial cache reuse", multiplier)
	default:
		return fmt.Sprintf("input costs %.2fx list — little or no cache reuse", multiplier)
	}
}

// priceLearnSinks attaches spend to every ranked sink. It prices at the user's
// own effective input rate because every sink in the report is input-side
// context; using list price would inflate a well-cached user's findings by up
// to 10x. Sinks keep their token fields unchanged — the dollar figure is
// additive, and a sink with no priceable window simply carries none.
func priceLearnSinks(sinks []Sink, spend *LearnSpend, windowDays float64) {
	if spend == nil || spend.EffectiveInputUSDPerMTok <= 0 {
		return
	}
	for i := range sinks {
		if sinks[i].TokensPerDayRate > 0 {
			sinks[i].SpendUSDPerDay = spend.priceInputTokens(sinks[i].TokensPerDayRate)
		}
		if sinks[i].TokensObserved > 0 {
			sinks[i].SpendUSDObserved = spend.priceInputTokens(sinks[i].TokensObserved)
		}
	}
}
