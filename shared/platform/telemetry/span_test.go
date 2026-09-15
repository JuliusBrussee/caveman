package telemetry_test

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/telemetry"
)

func TestSanitizeAttributesDropsPlatformAndContentData(t *testing.T) {
	got, err := telemetry.SanitizeAttributes(map[string]string{
		"service.name":                      "checkout-agent",
		"gen_ai.provider.name":              "openai",
		"cave.reserved.causality_depth":     "0",
		"gen_ai.input.messages":             `[{"role":"user","content":"secret"}]`,
		"openinference.span.kind":           "LLM",
		"input.value":                       "private prompt",
		"http.request.header.authorization": "Bearer secret",
		"metadata.customer_segment":         "enterprise",
	})
	if err != nil {
		t.Fatalf("SanitizeAttributes: %v", err)
	}
	for _, key := range []string{
		"cave.reserved.causality_depth",
		"gen_ai.input.messages",
		"input.value",
		"http.request.header.authorization",
	} {
		if _, ok := got[key]; ok {
			t.Fatalf("sensitive key %q survived", key)
		}
	}
	for key, want := range map[string]string{
		"service.name":              "checkout-agent",
		"gen_ai.provider.name":      "openai",
		"openinference.span.kind":   "LLM",
		"metadata.customer_segment": "enterprise",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

func TestSanitizeAttributesFailsClosedOnBounds(t *testing.T) {
	_, err := telemetry.SanitizeAttributes(map[string]string{
		"safe": strings.Repeat("x", telemetry.MaxAttributeValueBytes+1),
	})
	if err == nil {
		t.Fatal("oversized attribute accepted")
	}
}

func TestApplyProvenanceOverwritesUntrustedValues(t *testing.T) {
	span := telemetry.Span{
		SourceKind:           "forged",
		SourceSystem:         "forged",
		IngestionID:          "forged",
		NormalizationVersion: 999,
		ContentMode:          "forged",
	}
	telemetry.ApplyProvenance(&span, telemetry.Provenance{
		SourceKind:           telemetry.SourceKindFileImport,
		SourceSystem:         telemetry.SourceSystemLangfuse,
		IngestionID:          "018f0000-0000-7000-8000-000000000001",
		NormalizationVersion: telemetry.NormalizationVersion,
		ContentMode:          telemetry.ContentModeMetadataOnly,
	})
	if span.SourceKind != telemetry.SourceKindFileImport || span.SourceSystem != telemetry.SourceSystemLangfuse {
		t.Fatalf("provenance not overwritten: %+v", span)
	}
	if span.NormalizationVersion != telemetry.NormalizationVersion || span.ContentMode != telemetry.ContentModeMetadataOnly {
		t.Fatalf("normalization provenance mismatch: %+v", span)
	}
}
