package main

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// A receipt reads session usage only after the rows still being finished are
// written: the flush lands the session's last row before the sum.
func TestUsageAfterFlushWritesPendingRowsFirst(t *testing.T) {
	spend, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	usage := usageAfterFlush{spend: spend, flush: func() {
		spend.Record(gateway.RequestRecord{Timestamp: "2026-10-07 00:00:00.000", RequestID: "last", SessionID: "s-1",
			SessionCorrelationBasis: "explicit_header", Provider: "anthropic", StatusCode: 200, InputTokens: 10, OutputTokens: 2,
			TokenUsageBasis: "provider_complete", AuthMode: "payg", OptimizationIDs: []string{}})
	}}
	got, err := usage.SessionUsage("s-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 1 || got.InputTokens != 10 {
		t.Fatalf("session usage %+v, want the flushed row", got)
	}
}
