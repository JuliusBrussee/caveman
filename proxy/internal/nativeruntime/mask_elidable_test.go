package nativeruntime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// Mask what cannot be summarized in place; elide what can.
//
// These pin the boundary between the two mechanisms. Before it existed they
// collided: the shipped default (compress -> native policy "safe" -> profile
// "full-safe" -> masking on) swallowed structured tool output into a 4-line
// pointer stub before the elision engine ever saw it, so the agent got no rows
// and no invariants — and after recovering, the whole original entered context
// anyway. Measured 2026-08-08: inventory-mismatch and webhook-delivery-gaps 0/6
// with 27-97 recovery calls, while rate-limit-forensics scored 3/3 at ~35%
// cheaper purely because its pages sat under the mask threshold.

func maskRuntime(t *testing.T) *Runtime {
	t.Helper()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store)
}

// maskDecision runs a tool output through afterTool exactly as the hook does and
// reports whether the output was replaced by a pointer.
func maskDecision(t *testing.T, runtime *Runtime, output []byte) (bool, Response) {
	t.Helper()
	response, err := runtime.afterTool(Request{
		Session: Session{ID: "session-mask"},
		Tool:    &Tool{Name: "taskdata_fetch", Output: output},
	}, Response{}, true)
	if err != nil {
		t.Fatal(err)
	}
	return response.OutputReplacement != "", response
}

func jsonAPIPage(records int) []byte {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < records; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sku":"SKU-%05d","title":"Part SKU-%05d rev %d","active":true,"supplier":"acme_parts"}`,
			60000+i, 60000+i, i%7)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func csvExport(rows int) []byte {
	var b strings.Builder
	b.WriteString("sku,on_hand,reserved,supplier,line_status\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "SKU-%05d,%d,%d,acme_parts,ok\n", 60000+i, 10+i%90, i%40)
	}
	return []byte(b.String())
}

func ndjsonEvents(events int) []byte {
	var b strings.Builder
	for i := 0; i < events; i++ {
		fmt.Fprintf(&b, `{"at":"2026-08-06T07:%02d:00Z","delivery_id":"wh-%04d","status":"delivered"}`+"\n", i%60, 5000+i)
	}
	return []byte(b.String())
}

func prosebBlob(size int) []byte {
	paragraph := "The migration proceeded without incident, though several operators noted that " +
		"the staging window overlapped with the quarterly audit and that nobody had told them. " +
		"A follow-up was recorded and then, as follow-ups do, forgotten by everyone concerned. "
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(paragraph)
	}
	return []byte(b.String())
}

// TestStructuredToolOutputIsNotMasked: the classes with a field grammar reach the
// elision engine intact, however large they are.
func TestStructuredToolOutputIsNotMasked(t *testing.T) {
	runtime := maskRuntime(t)
	for _, tc := range []struct {
		name    string
		output  []byte
		wantsAs string
	}{
		{"json api page", jsonAPIPage(60), engine.TypeJSON},
		{"csv export", csvExport(120), engine.TypeTabular},
		{"ndjson event stream", ndjsonEvents(120), engine.TypeLog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := engine.New(nil, nil).Detect(tc.output); got != tc.wantsAs {
				t.Fatalf("fixture classifies as %q, not %q — the case no longer tests what it says", got, tc.wantsAs)
			}
			if len(tc.output) < 1024 {
				t.Fatalf("fixture is only %d bytes; it must be well over the mask threshold", len(tc.output))
			}
			masked, response := maskDecision(t, runtime, tc.output)
			if masked {
				t.Errorf("%s was masked into %q — the elision engine never sees it, so its rows and invariants are lost",
					tc.name, response.OutputReplacement)
			}
			// Capture still happens: recovery stays available, only the mask is skipped.
			if response.RecoveryRef == "" {
				t.Errorf("%s was not captured; skipping the mask must not skip capture", tc.name)
			}
		})
	}
}

// TestUnsummarizableToolOutputIsStillMasked: masking keeps its original job.
func TestUnsummarizableToolOutputIsStillMasked(t *testing.T) {
	runtime := maskRuntime(t)
	blob := prosebBlob(50 << 10)
	if got := engine.New(nil, nil).Detect(blob); got != engine.TypeText {
		t.Fatalf("fixture classifies as %q, not text", got)
	}
	masked, response := maskDecision(t, runtime, blob)
	if !masked {
		t.Fatal("a 50KB prose blob has no field grammar and must still be masked")
	}
	if !strings.Contains(response.OutputReplacement, "caveman_retrieve") {
		t.Errorf("the mask must still name its recovery verb: %q", response.OutputReplacement)
	}
}

// TestMaskThresholdUnchangedForMaskableClasses: the size rule for what still
// masks is exactly what it was.
func TestMaskThresholdUnchangedForMaskableClasses(t *testing.T) {
	runtime := maskRuntime(t)
	if masked, _ := maskDecision(t, runtime, prosebBlob(120)); masked {
		t.Error("a tiny prose output must not be masked")
	}
	if masked, _ := maskDecision(t, runtime, prosebBlob(4096)); !masked {
		t.Error("a prose output well over the threshold must be masked")
	}
}

// TestUnavailableClassifierFallsBackToMasking is the fail-safe direction: without
// a classifier the runtime cannot know what elision would do, so it masks —
// bounded context, never an unbounded pass-through.
func TestUnavailableClassifierFallsBackToMasking(t *testing.T) {
	runtime := maskRuntime(t)
	runtime.detect = nil
	masked, _ := maskDecision(t, runtime, jsonAPIPage(60))
	if !masked {
		t.Fatal("with no classifier the runtime must fall back to today's masking behaviour")
	}
}

// TestMaskAndElisionCompose is the end-to-end intent: a JSON page passes afterTool
// untouched, and the engine then compresses it into rows plus stated invariants —
// the two mechanisms cooperating instead of one destroying the other's input.
func TestMaskAndElisionCompose(t *testing.T) {
	runtime := maskRuntime(t)
	page := jsonAPIPage(60)

	masked, _ := maskDecision(t, runtime, page)
	if masked {
		t.Fatal("precondition: the JSON page must reach the elision path unmasked")
	}

	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	result, err := engine.New(store, nil).Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	compressed := string(result.Output)

	if result.TokensAfter >= result.TokensBefore {
		t.Fatalf("the page did not compress: %d -> %d", result.TokensBefore, result.TokensAfter)
	}
	if !strings.Contains(compressed, "__caveman_invariants__") {
		t.Fatalf("the compressed page states no invariants, so masking it would have cost nothing:\n%s", compressed)
	}
	// The facts an agent needs are present without any recovery call at all.
	if !strings.Contains(compressed, "supplier=acme_parts") || !strings.Contains(compressed, "all ") {
		t.Errorf("expected the constant field to be stated:\n%s", compressed)
	}
	if !strings.Contains(compressed, "sku: SKU-") || !strings.Contains(compressed, " present") {
		t.Errorf("expected dense sku coverage, the membership fact:\n%s", compressed)
	}
	if !strings.Contains(compressed, "SKU-60000") {
		t.Errorf("expected real rows to survive:\n%s", compressed)
	}
	if result.RecoveryHandle == "" {
		t.Error("expected a recovery handle beside the rows")
	}
}
