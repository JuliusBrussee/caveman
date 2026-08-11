package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// The live path, end to end: a real toolwork page goes through the real engine,
// the handle it emits goes back through the real caveman_retrieve tool handler
// built by EngineTools (session and all), and what comes out has to be the raw
// original rows.
//
// This exists at this level because the unit tests all passed while the bench
// burned: on 2026-08-08 the wrapped agent on inventory-mismatch and
// webhook-delivery-gaps reported "every taskdata_fetch call returns only an
// opaque ccr:// pointer instead of actual page content, and caveman_retrieve on
// that pointer never returns raw data either — it just returns a new pointer to
// another pointer, indefinitely", made 27-97 recovery calls, and timed out
// without writing an answer. Nothing below the tool boundary could see that.

// pointerShapes are every form of "a reference instead of the data" that has ever
// been handed to an agent by this stack. None may appear in recovered content.
var pointerShapes = []*regexp.Regexp{
	regexp.MustCompile(`ccr://`),
	regexp.MustCompile(`<<ccr:`),
	regexp.MustCompile(`(?m)^full: `),
	regexp.MustCompile(`__caveman_elided__`),
	regexp.MustCompile(`elided \(caveman\)`),
}

func corpusPage(t *testing.T, name string) []byte {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read corpus page: %v", err)
	}
	return page
}

// liveEngine is the real engine over a real (in-memory) CCR store — the same
// pairing the wrap runtime uses.
func liveEngine(t *testing.T) *engine.Engine {
	t.Helper()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return engine.New(store, nil)
}

// callRetrieveTool drives the caveman_retrieve tool exactly as the MCP server
// does: through the handler EngineTools built, with its session attached.
func callRetrieveTool(t *testing.T, tools []Tool, handle, query string) string {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != ToolRetrieve {
			continue
		}
		args, err := json.Marshal(map[string]string{"recovery_handle": handle, "query": query})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(tool.Handler(args))
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("tool result is not an MCP envelope: %v (%s)", err, raw)
		}
		if envelope.IsError {
			t.Fatalf("retrieve failed: %s", raw)
		}
		var b strings.Builder
		for _, part := range envelope.Content {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	t.Fatalf("%s is not registered", ToolRetrieve)
	return ""
}

func assertNoPointers(t *testing.T, what, content string) {
	t.Helper()
	for _, shape := range pointerShapes {
		if loc := shape.FindStringIndex(content); loc != nil {
			t.Errorf("%s: recovered content still carries a pointer/marker %q at byte %d — the agent asked for data and got a reference:\n%s",
				what, shape.String(), loc[0], excerpt(content, loc[0]))
		}
	}
}

func excerpt(content string, at int) string {
	start := max(0, at-120)
	end := min(len(content), at+200)
	return content[start:end]
}

// TestRetrieveReturnsRawRowsForInventoryCatalog is the inventory-mismatch shape:
// a JSON catalog whose records compress to a marker.
func TestRetrieveReturnsRawRowsForInventoryCatalog(t *testing.T) {
	eng := liveEngine(t)
	tools := EngineTools(eng, nil)
	page := corpusPage(t, "inventory_catalog_page.json")

	result, err := eng.Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecoveryHandle == "" {
		t.Fatal("a compressed page must carry a recovery handle")
	}

	recovered := callRetrieveTool(t, tools, result.RecoveryHandle, "")
	assertNoPointers(t, "inventory catalog full recovery", recovered)

	// Every SKU that the compressed view dropped must come back verbatim.
	var original struct {
		Items []struct {
			SKU   string `json:"sku"`
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(page, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.Items) < 10 {
		t.Fatalf("corpus page has only %d items; it is not the shape this pins", len(original.Items))
	}
	missing := 0
	for _, item := range original.Items {
		if !strings.Contains(recovered, item.SKU) || !strings.Contains(recovered, item.Title) {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d catalog records did not come back verbatim", missing, len(original.Items))
	}
}

// TestRetrieveReturnsRawRowsForWebhookEvents is the webhook-delivery-gaps shape.
func TestRetrieveReturnsRawRowsForWebhookEvents(t *testing.T) {
	eng := liveEngine(t)
	tools := EngineTools(eng, nil)
	page := corpusPage(t, "webhook_delivery_events_page.json")

	result, err := eng.Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	recovered := callRetrieveTool(t, tools, result.RecoveryHandle, "")
	assertNoPointers(t, "webhook events full recovery", recovered)

	var original struct {
		Events []struct {
			DeliveryID string `json:"delivery_id"`
			Status     string `json:"status"`
		} `json:"events"`
	}
	if err := json.Unmarshal(page, &original); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range original.Events {
		seen[event.DeliveryID] = true
	}
	missing := 0
	for id := range seen {
		if !strings.Contains(recovered, id) {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d delivery ids did not come back", missing, len(seen))
	}
}

// TestRetrieveReturnsRawRowsForStockCSV is the tabular half of inventory-mismatch.
func TestRetrieveReturnsRawRowsForStockCSV(t *testing.T) {
	eng := liveEngine(t)
	tools := EngineTools(eng, nil)
	page := corpusPage(t, "inventory_stock_page.csv")

	result, err := eng.Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	recovered := callRetrieveTool(t, tools, result.RecoveryHandle, "")
	assertNoPointers(t, "stock CSV full recovery", recovered)

	for _, line := range strings.Split(strings.TrimSpace(string(page)), "\n") {
		if !strings.Contains(recovered, strings.TrimSpace(line)) {
			t.Fatalf("row did not come back verbatim: %q", line)
		}
	}
}

// TestQueryFilteredRetrieveReturnsRawRecords covers the query path the storming
// agents actually used — narrowed views must still be raw records, never
// pointers, and every record they contain must be whole.
func TestQueryFilteredRetrieveReturnsRawRecords(t *testing.T) {
	eng := liveEngine(t)
	tools := EngineTools(eng, nil)

	for _, tc := range []struct{ page, query, needle string }{
		{"inventory_catalog_page.json", "inactive sku status_reason supplier_contract_ended", "SKU-"},
		{"webhook_delivery_events_page.json", "delivered delivery_id status", "wh-"},
		{"inventory_stock_page.csv", "sku on_hand reserved supplier", "SKU-"},
	} {
		t.Run(tc.page, func(t *testing.T) {
			page := corpusPage(t, tc.page)
			result, err := eng.Compress(page, engine.Options{Mode: engine.ModeCompress})
			if err != nil {
				t.Fatal(err)
			}
			recovered := callRetrieveTool(t, tools, result.RecoveryHandle, tc.query)
			assertNoPointers(t, tc.page+" query recovery", recovered)
			if !strings.Contains(recovered, tc.needle) {
				t.Fatalf("query recovery returned no %s records at all: %q", tc.needle, recovered)
			}
			if strings.TrimSpace(recovered) == "" {
				t.Fatal("query recovery returned nothing")
			}
		})
	}
}

// TestRetrieveHandleFormsAllResolve pins the handle vocabulary. An agent copies
// whatever it sees in the compressed view, and every form it can see must work —
// a form that does not resolve is what turns one recovery into a storm.
func TestRetrieveHandleFormsAllResolve(t *testing.T) {
	eng := liveEngine(t)
	page := corpusPage(t, "inventory_catalog_page.json")
	result, err := eng.Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	bare := result.RecoveryHandle

	for _, form := range []string{
		bare,
		"<<ccr:" + bare + ">>",
		"ccr:" + bare,
		"ccr://" + bare,
	} {
		t.Run(form, func(t *testing.T) {
			// A fresh session per form: this is about handle parsing, not anti-storm.
			recovered := callRetrieveTool(t, EngineTools(eng, nil), form, "")
			assertNoPointers(t, "handle form "+form, recovered)
			if !strings.Contains(recovered, "SKU-60000") {
				t.Fatalf("handle form %q did not resolve to the original: %q", form, excerpt(recovered, 0))
			}
		})
	}
}

// TestAntiStormPayoutIsTheRawOriginal guards the K-threshold path specifically:
// the one big payout must be the raw stored original, not the compressed side and
// not another pointer.
func TestAntiStormPayoutIsTheRawOriginal(t *testing.T) {
	eng := liveEngine(t)
	tools := EngineTools(eng, nil)
	page := corpusPage(t, "inventory_catalog_page.json")
	result, err := eng.Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}

	var last string
	for i := 0; i <= retrieveStormThreshold+2; i++ {
		last = callRetrieveTool(t, tools, result.RecoveryHandle, "query variant "+strings.Repeat("x", i+1))
		assertNoPointers(t, "retrieve "+strings.Repeat("x", i+1), last)
	}
	if !strings.Contains(last, "COMPLETE stored original") {
		t.Fatalf("past the threshold the payout note must appear: %q", excerpt(last, 0))
	}
	if !strings.Contains(last, "SKU-60000") || !strings.Contains(last, "supplier_contract_ended") {
		t.Fatalf("the payout is not the raw original: %q", excerpt(last, 0))
	}
}

// TestRetrieveResolvesNativeRuntimeObjectPointers is the root-cause pin.
//
// The CCR store holds two id spaces. Compression mints blob handles (`ccr_…`);
// the proxy's native runtime masks a large tool output down to
// `[type]\nstatus: current\nsource: …\nfull: ccr://<objectID>` and mints a typed
// OBJECT id. On 2026-08-08 that mask is what inventory-mismatch and
// webhook-delivery-gaps actually saw — the page never reached the model at all —
// and caveman_retrieve knew only the blob space, so every recovery answered
// cave_unknown_handle. The failing tool result was then masked into a fresh
// pointer, which is the "pointer to another pointer, indefinitely" the agent
// reported before timing out.
//
// This drives the exact bytes the mask produces, through the real tool.
func TestRetrieveResolvesNativeRuntimeObjectPointers(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eng := engine.New(store, nil)

	for _, name := range []string{"inventory_catalog_page.json", "webhook_delivery_events_page.json", "inventory_stock_page.csv"} {
		t.Run(name, func(t *testing.T) {
			page := corpusPage(t, name)
			// Exactly what nativeruntime.afterTool stores for a masked tool result.
			objectID, err := store.PutObject(ccr.Object{
				Type:               ccr.ObjectCommandResult,
				Source:             "taskdata_fetch",
				SessionID:          "session-1",
				TransformVersion:   "native-runtime-v1",
				Currentness:        ccr.Current,
				Lifecycle:          ccr.Warm,
				OriginalByteLength: len(page),
				StoredByteLength:   len(page),
				Data:               page,
			})
			if err != nil {
				t.Fatal(err)
			}

			// And exactly what the agent copies out of the masked tool result.
			for _, form := range []string{"ccr://" + objectID, objectID} {
				recovered := callRetrieveTool(t, EngineTools(eng, nil), form, "")
				assertNoPointers(t, "object pointer "+form, recovered)
				if recovered != string(page) {
					t.Fatalf("%s did not return the raw stored tool output (got %d bytes, want %d)",
						form, len(recovered), len(page))
				}
			}
		})
	}
}
