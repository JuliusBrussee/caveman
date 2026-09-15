package anthropic

// COGS regression guard for the gateway's per-request CPU cost.
//
// Why this exists: the pricing model in docs/strategy/PRICING_V2.md §1.3 is built
// on a measured claim — the byte-safe transform costs ~10 ns per byte of request
// body, which prices a million agentic requests at roughly EUR 0.13 of Scaleway
// compute. That claim is load-bearing for "the gateway hop is free" (§4.1), so it
// needs a guard: if the transform path ever regresses into super-linear behaviour
// or picks up a per-request allocation storm, the pricing conclusion changes and
// someone should notice here rather than on an invoice.
//
// Read ns/op against body size. The invariant to watch is MB/s staying roughly
// FLAT across the four sizes (linear CPU in body bytes). A collapse at the large
// sizes means the cost model in §1.3 is stale and PRICING_V2.md §1.3 + §3's margin
// table must be re-derived.
//
// Measured 2026-08-02 on Apple M3 Pro, Go 1.26.5 (the numbers quoted in the spec):
//
//	body_4KB_actual_8KB       92,473 ns/op    92.55 MB/s     323 allocs/op
//	body_20KB_actual_16KB    174,549 ns/op    96.31 MB/s     393 allocs/op
//	body_100KB_actual_56KB   540,047 ns/op   107.53 MB/s     719 allocs/op
//	body_400KB_actual_207KB1,951,058 ns/op   109.07 MB/s   1,926 allocs/op
//
// Run: go test ./proxy/providers/anthropic/ -run='^$' -bench=BenchmarkTransformCOGS
//
// Note this measures the transform only. Per §1.3 the gateway's dominant marginal
// cost is memory-seconds held during streaming, not CPU — that is a concurrency
// property and is not measurable here.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// buildCOGSBody builds a realistic Anthropic /v1/messages body of approximately
// the requested size: a system prompt, an 8-tool catalog, and a multi-turn
// message history — the shape an agentic caller actually sends.
func buildCOGSBody(approxKB int) string {
	filler := strings.Repeat("The agent must verify each claim against the ledger before reporting. ", 30)
	tools := make([]map[string]any, 0, 8)
	for i := 0; i < 8; i++ {
		tools = append(tools, map[string]any{
			"name":        fmt.Sprintf("tool_%d", i),
			"description": filler[:400],
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
			},
		})
	}
	turns := approxKB / 2
	if turns < 2 {
		turns = 2
	}
	msgs := make([]map[string]any, 0, turns)
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]any{"role": role, "content": filler[:1000]})
	}
	b, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 4096,
		"system":     []any{map[string]any{"type": "text", "text": filler}},
		"tools":      tools,
		"messages":   msgs,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func BenchmarkTransformCOGS(b *testing.B) {
	a := New("http://upstream").(Adapter)
	policy := providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{OptimizerID: true},
	}
	for _, kb := range []int{4, 20, 100, 400} {
		body := buildCOGSBody(kb)
		b.Run(fmt.Sprintf("body_%dKB_actual_%dKB", kb, len(body)/1024), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				res, err := a.ApplyProviderNativeTransforms(
					context.Background(),
					strings.NewReader(body),
					providers.RequestMetadata{Provider: "anthropic"},
					policy,
				)
				if err != nil {
					b.Fatalf("transform error: %v", err)
				}
				if len(res.Body) == 0 {
					b.Fatal("transform returned an empty body")
				}
			}
		})
	}
}
