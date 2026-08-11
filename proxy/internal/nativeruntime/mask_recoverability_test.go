package nativeruntime

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// TestMaskedToolOutputTellsTheAgentHowToRecoverIt pins the contract of the mask:
// it replaces a whole tool output, so it must carry BOTH a reference that
// resolves and the verb that resolves it.
//
// On 2026-08-08 it carried only `full: ccr://<id>`. The agent's own words:
// "every taskdata_fetch call returns only an opaque ccr:// pointer instead of
// actual page content, and caveman_retrieve on that pointer never returns raw
// data either — it just returns a new pointer to another pointer, indefinitely".
// 27-97 recovery calls on inventory-mismatch and webhook-delivery-gaps, no
// answer written, both 0/6.
func TestMaskedToolOutputTellsTheAgentHowToRecoverIt(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runtime := New(store)
	const session = "session-mask"

	page := strings.Repeat(`{"sku":"SKU-60000","title":"Part SKU-60000 rev 0","active":false}`+"\n", 40)
	response, err := runtime.afterTool(Request{
		Session: Session{ID: session},
		Tool:    &Tool{Name: "taskdata_fetch", Output: []byte(page)},
	}, Response{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if response.OutputReplacement == "" {
		t.Fatalf("a %d-byte tool output should have been masked", len(page))
	}
	if !strings.Contains(response.OutputReplacement, "caveman_retrieve") {
		t.Errorf("the mask names no recovery tool, so the reference is a dead end:\n%s", response.OutputReplacement)
	}
	if !strings.Contains(response.OutputReplacement, response.RecoveryRef) {
		t.Errorf("the mask does not carry its own recovery reference:\n%s", response.OutputReplacement)
	}

	// And the reference it prints must actually resolve to the raw bytes.
	id := strings.TrimPrefix(response.RecoveryRef, "ccr://")
	object, err := runtime.store.GetObject(id)
	if err != nil {
		t.Fatalf("the reference the agent is shown does not resolve: %v", err)
	}
	if string(object.Data) != page {
		t.Fatalf("the stored object is not the raw tool output (%d bytes vs %d)", len(object.Data), len(page))
	}
}
