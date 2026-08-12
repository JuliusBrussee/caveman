// Pinned mirror of cloud/internal/verifiedmethod.Methods(). DO NOT EDIT.
// The receipt runtime contract test fails if this list drifts from the Go
// registry that signs and ingests billable receipts.
export const VERIFIED_SAVINGS_METHODS = [
  "provider_causal_cache",
  "provider_causal_cache_bedrock",
  "provider_counted_baseline_delta",
] as const;
