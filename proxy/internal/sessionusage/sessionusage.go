// Package sessionusage defines content-blind local provider-usage evidence shared
// by spend store and native session receipts without coupling either package.
package sessionusage

type Snapshot struct {
	Status                      string  `json:"status"`
	SessionID                   string  `json:"session_id"`
	CorrelationBasis            string  `json:"correlation_basis"`
	Requests                    int64   `json:"requests"`
	ProviderCompleteRequests    int64   `json:"provider_complete_requests"`
	InputTokens                 int64   `json:"input_tokens"`
	OutputTokens                int64   `json:"output_tokens"`
	CachedInputTokens           int64   `json:"cached_input_tokens"`
	CacheCreationInputTokens    int64   `json:"cache_creation_input_tokens"`
	ReasoningTokens             int64   `json:"reasoning_tokens"`
	CatalogListPriceSubtotalUSD float64 `json:"catalog_list_price_subtotal_usd"`
	InferredSavingsUSD          float64 `json:"inferred_savings_usd"`
	CompressionTokensBefore     int64   `json:"compression_tokens_before"`
	CompressionTokensAfter      int64   `json:"compression_tokens_after"`
	CompressionTokenCountBasis  string  `json:"compression_token_count_basis"`
	TokenUsageCoverage          string  `json:"token_usage_coverage"`
	CostBasis                   string  `json:"cost_basis"`
	SavingsBasis                string  `json:"savings_basis"`
}

type Reader interface {
	SessionUsage(sessionID string) (Snapshot, error)
}
