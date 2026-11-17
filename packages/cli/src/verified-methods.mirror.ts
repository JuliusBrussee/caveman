// Pinned mirror of the Caveman Cloud verified-method registry. DO NOT EDIT.
// Where that Go registry (which signs and ingests billable receipts) is present,
// the receipts runtime test fails if this list drifts from it.
export const VERIFIED_SAVINGS_METHODS = [
  "provider_causal_cache",
  "provider_causal_cache_bedrock",
  "provider_counted_baseline_delta",
] as const;
