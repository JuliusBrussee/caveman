package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
)

func TestSessionStatsCountCallsWithoutSharedStoreContamination(t *testing.T) {
	eng := realEngine(t)
	original := `{"items":[` + strings.Repeat(`{"k":"v","n":1},`, 50) + `{"k":"v","n":1}]}`
	prior, err := eng.Compress([]byte(original), engine.Options{Mode: engine.ModeCompress})
	if err != nil || prior.RecoveryHandle == "" {
		t.Fatalf("seed prior process recovery: %+v err=%v", prior, err)
	}
	first := EngineTools(eng, nil)
	second := EngineTools(eng, nil)
	assertSessionStats(t, first, 0, 0, 0)

	args := json.RawMessage(`{"input":` + jsonStr(original) + `}`)
	for i := 0; i < 2; i++ {
		result := callAccountingTool(t, first, ToolCompress, args)
		if result.IsError {
			t.Fatalf("compress: %+v", result)
		}
	}
	// Another server writes a new recovery to the same store while this session
	// remains alive. A database delta would incorrectly count that other call.
	other := strings.ReplaceAll(original, `"v"`, `"other-process"`)
	callAccountingTool(t, second, ToolCompress, json.RawMessage(`{"input":`+jsonStr(other)+`}`))
	assertSessionStats(t, first, 2, 2*prior.TokensBefore, 2*prior.TokensAfter)
	assertSessionStats(t, EngineTools(eng, nil), 0, 0, 0)

	// Pass-through calls count even though the recovery store has no new row.
	unchanged := callAccountingTool(t, first, ToolCompress, json.RawMessage(`{"input":"small record"}`))
	var pass compressPayload
	if unchanged.IsError || json.Unmarshal([]byte(unchanged.Content[0].Text), &pass) != nil || pass.RecoveryHandle != nil {
		t.Fatalf("expected accounted pass-through: %+v", unchanged)
	}
	assertSessionStats(t, first, 3, 2*prior.TokensBefore+pass.TokensBefore, 2*prior.TokensAfter+pass.TokensAfter)
	stored, err := eng.Stats()
	if err != nil || stored.Totals.Count != 2 {
		t.Fatalf("engine must retain its distinct-payload lifetime stats: %+v err=%v", stored, err)
	}
}

func TestSessionStatsCountSafeFallbackButNotRejectedResults(t *testing.T) {
	for _, validFallback := range []bool{true, false} {
		t.Run(map[bool]string{true: "accounted pass-through", false: "rejected unsafe result"}[validFallback], func(t *testing.T) {
			eng := mockEngine{compress: func(in []byte, _ engine.Options) (engine.Result, error) {
				if !validFallback {
					return engine.Result{}, errors.New("recovery write failed")
				}
				return engine.Result{Output: in, TokensBefore: 7, TokensAfter: 7, Basis: engine.BasisInferred}, errors.New("recovery write failed")
			}}
			tools := EngineTools(eng, nil)
			result := callAccountingTool(t, tools, ToolCompress, json.RawMessage(`{"input":"unchanged bytes"}`))
			if result.IsError == validFallback {
				t.Fatalf("unexpected compression result: %+v", result)
			}
			if validFallback {
				assertSessionStats(t, tools, 1, 7, 7)
			} else {
				assertSessionStats(t, tools, 0, 0, 0)
			}
			callAccountingTool(t, tools, ToolCompress, json.RawMessage(`{"input":17}`))
			if validFallback {
				assertSessionStats(t, tools, 1, 7, 7)
			} else {
				assertSessionStats(t, tools, 0, 0, 0)
			}
		})
	}
}

func callAccountingTool(t *testing.T, tools []Tool, name string, args json.RawMessage) ToolResult {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Handler(args)
		}
	}
	t.Fatalf("missing tool %s", name)
	return ToolResult{}
}

func assertSessionStats(t *testing.T, tools []Tool, requests, before, after int) {
	t.Helper()
	result := callAccountingTool(t, tools, ToolStats, json.RawMessage(`{}`))
	var got statsPayload
	if result.IsError || json.Unmarshal([]byte(result.Content[0].Text), &got) != nil {
		t.Fatalf("decode stats: %+v", result)
	}
	wantRatio := 0.0
	if before > 0 {
		wantRatio = float64(before-after) / float64(before)
	}
	if got.Requests != requests || got.TokensBefore != before || got.TokensAfter != after || got.Ratio != wantRatio || got.Basis != engine.BasisInferred || got.Scope != "session" {
		t.Fatalf("stats=%+v, want requests=%d before=%d after=%d ratio=%v inferred session", got, requests, before, after, wantRatio)
	}
}
