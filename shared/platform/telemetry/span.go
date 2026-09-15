// Package telemetry owns the stable, source-neutral records written to Caveman's
// observability tables. Wire formats and vendor adapters normalize into these
// records; tenant scope and provenance are always stamped by trusted server code.
package telemetry

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	NormalizationVersion uint16 = 1

	SourceKindGateway       = "gateway"
	SourceKindLiveOTLP      = "live_otlp"
	SourceKindFileImport    = "file_import"
	SourceKindConnectorSync = "connector_sync"
	SourceKindLegacyUnknown = "legacy_unknown"

	SourceSystemCaveman    = "caveman"
	SourceSystemOTel       = "otel"
	SourceSystemLangWatch  = "langwatch"
	SourceSystemLangSmith  = "langsmith"
	SourceSystemBraintrust = "braintrust"
	SourceSystemPostHog    = "posthog"
	SourceSystemLangfuse   = "langfuse"
	SourceSystemHelicone   = "helicone"
	SourceSystemPhoenix    = "phoenix"
	SourceSystemWeave      = "weave"
	SourceSystemGeneric    = "generic"
	SourceSystemUnknown    = "unknown"

	ContentModeMetadataOnly        = "metadata_only"
	ContentModeConsentedPayloadRef = "consented_payload_refs"

	MaxAttributes          = 128
	MaxAttributeKeyBytes   = 256
	MaxAttributeValueBytes = 4096
	MaxAttributesBytes     = 64 << 10
	MaxEventsBytes         = 64 << 10
)

// Span maps 1-to-1 to caveman.spans. Optional provenance fields use omitempty
// so mixed-version writers can roll out before the additive ClickHouse migration.
type Span struct {
	// ServerOwnedOTLP is an in-process admission seal. JSON cannot set it.
	ServerOwnedOTLP          bool              `json:"-"`
	Timestamp                string            `json:"timestamp"`
	EndTimestamp             string            `json:"end_timestamp"`
	TraceID                  string            `json:"trace_id"`
	SpanID                   string            `json:"span_id"`
	ParentSpanID             string            `json:"parent_span_id"`
	SessionID                string            `json:"session_id,omitempty"`
	OrganizationID           string            `json:"organization_id"`
	ProjectID                string            `json:"project_id"`
	AgentSlug                string            `json:"agent_slug"`
	WorkflowSlug             string            `json:"workflow_slug"`
	IngestSource             string            `json:"ingest_source,omitempty"`
	OTelSpanKind             string            `json:"otel_span_kind,omitempty"`
	HTTPClientClassification string            `json:"http_client_classification,omitempty"`
	HTTPRequestResendState   string            `json:"http_request_resend_state,omitempty"`
	HTTPRequestResendCount   uint64            `json:"http_request_resend_count,omitempty"`
	SpanType                 string            `json:"span_type"`
	SpanName                 string            `json:"span_name"`
	Status                   string            `json:"status"`
	DurationMS               uint64            `json:"duration_ms"`
	Provider                 string            `json:"provider"`
	Model                    string            `json:"model"`
	ToolName                 string            `json:"tool_name"`
	InputBytes               uint64            `json:"input_bytes"`
	OutputBytes              uint64            `json:"output_bytes"`
	InputTokens              uint64            `json:"input_tokens"`
	OutputTokens             uint64            `json:"output_tokens"`
	CachedInputTokens        uint64            `json:"cached_input_tokens"`
	TotalCostUSD             float64           `json:"total_cost_usd"`
	ArtifactID               string            `json:"artifact_id"`
	Attributes               map[string]string `json:"attributes"`
	EventsJSON               string            `json:"events_json"`
	SourceKind               string            `json:"source_kind,omitempty"`
	SourceSystem             string            `json:"source_system,omitempty"`
	IngestionID              string            `json:"ingestion_id,omitempty"`
	NormalizationVersion     uint16            `json:"normalization_version,omitempty"`
	ContentMode              string            `json:"content_mode,omitempty"`
}

type Provenance struct {
	SourceKind           string
	SourceSystem         string
	IngestionID          string
	NormalizationVersion uint16
	ContentMode          string
}

// GenAIFields is the metadata-only projection Caveman uses across both current
// OpenTelemetry GenAI semantic conventions and OpenInference. Content-bearing
// attributes stay out of this projection and are stripped before persistence.
type GenAIFields struct {
	Provider          string
	Model             string
	ToolName          string
	SpanType          string
	InputTokens       uint64
	OutputTokens      uint64
	CachedInputTokens uint64
	TotalCostUSD      float64
}

// ExtractGenAIFields keeps live OTLP and file import normalization identical.
// Current OTel GenAI keys win when both convention families are present.
func ExtractGenAIFields(attrs, resourceAttrs map[string]string, spanName string) GenAIFields {
	provider := firstValue(attrs, "gen_ai.provider.name", "gen_ai.system", "llm.provider", "llm.system")
	if provider == "" {
		provider = firstValue(resourceAttrs, "gen_ai.provider.name", "gen_ai.system", "llm.provider", "llm.system")
	}
	model := firstValue(attrs, "gen_ai.response.model", "gen_ai.request.model", "llm.model_name")
	if model == "" {
		model = firstValue(resourceAttrs, "gen_ai.response.model", "gen_ai.request.model", "llm.model_name")
	}
	spanType := firstValue(attrs, "gen_ai.operation.name")
	if spanType == "" {
		spanType = strings.ToLower(firstValue(attrs, "openinference.span.kind"))
	}
	if spanType == "" {
		spanType = spanName
	}

	inputTokens := firstUint(attrs, "gen_ai.usage.input_tokens", "llm.token_count.prompt")
	outputTokens := firstUint(attrs, "gen_ai.usage.output_tokens", "llm.token_count.completion")
	cachedInputTokens := firstUint(attrs,
		"gen_ai.usage.cache_read.input_tokens",
		"gen_ai.usage.cached_tokens",
		"llm.token_count.prompt_details.cache_read",
	)
	if cachedInputTokens > inputTokens {
		cachedInputTokens = 0
	}

	return GenAIFields{
		Provider:          provider,
		Model:             model,
		ToolName:          firstValue(attrs, "gen_ai.tool.name", "tool.name"),
		SpanType:          spanType,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		CachedInputTokens: cachedInputTokens,
		TotalCostUSD:      firstNonNegativeFloat(attrs, "gen_ai.usage.cost_usd", "cave.cost_usd", "llm.cost.total"),
	}
}

func firstValue(attrs map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(attrs[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstUint(attrs map[string]string, keys ...string) uint64 {
	for _, key := range keys {
		value, ok := attrs[key]
		if !ok {
			continue
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func firstNonNegativeFloat(attrs map[string]string, keys ...string) float64 {
	for _, key := range keys {
		value, ok := attrs[key]
		if !ok {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil && parsed >= 0 && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed
		}
	}
	return 0
}

func ApplyProvenance(span *Span, provenance Provenance) {
	span.SourceKind = provenance.SourceKind
	span.SourceSystem = provenance.SourceSystem
	span.IngestionID = provenance.IngestionID
	span.NormalizationVersion = provenance.NormalizationVersion
	span.ContentMode = provenance.ContentMode
}

var contentAttributePrefixes = []string{
	"gen_ai.input.messages",
	"gen_ai.output.messages",
	"gen_ai.prompt",
	"gen_ai.completion",
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
	"llm.input_messages",
	"llm.output_messages",
	"input.value",
	"output.value",
	"tool.parameters",
	"tool.output",
	"retrieval.documents",
	"langfuse.input",
	"langfuse.output",
	"helicone.request.messages",
	"helicone.response.body",
	"posthog.ai.input",
	"posthog.ai.output",
}

// AttributeAllowed is shared by OTLP and import adapters. False means value is
// content, credential material, or platform-owned state and must never enter the
// metadata-only ClickHouse map.
func AttributeAllowed(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" || strings.HasPrefix(lower, "cave.reserved.") {
		return false
	}
	for _, prefix := range contentAttributePrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+".") {
			return false
		}
	}
	for _, part := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == '/'
	}) {
		switch part {
		case "authorization", "apikey", "api", "secret", "password", "passwd":
			return false
		}
	}
	if strings.Contains(lower, "api_key") || strings.Contains(lower, "access_token") || strings.Contains(lower, "refresh_token") {
		return false
	}
	return true
}

// SanitizeAttributes returns a deterministic copy. Sensitive keys are removed;
// malformed or oversized metadata fails closed so callers can reject the record.
func SanitizeAttributes(attrs map[string]string) (map[string]string, error) {
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		if AttributeAllowed(key) {
			keys = append(keys, key)
		}
	}
	if len(keys) > MaxAttributes {
		return nil, fmt.Errorf("telemetry attributes exceed %d keys", MaxAttributes)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	total := 0
	for _, key := range keys {
		value := attrs[key]
		if len(key) > MaxAttributeKeyBytes {
			return nil, fmt.Errorf("telemetry attribute key exceeds %d bytes", MaxAttributeKeyBytes)
		}
		if len(value) > MaxAttributeValueBytes {
			return nil, fmt.Errorf("telemetry attribute %q exceeds %d bytes", key, MaxAttributeValueBytes)
		}
		total += len(key) + len(value)
		if total > MaxAttributesBytes {
			return nil, fmt.Errorf("telemetry attributes exceed %d bytes", MaxAttributesBytes)
		}
		out[key] = value
	}
	return out, nil
}
