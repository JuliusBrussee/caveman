package compressors

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const sampleAXTree = `[
  {
    "nodeId": "1",
    "role": {"type": "role", "value": "RootWebArea"},
    "name": {"type": "computedString", "value": "Caveman Console"},
    "backendDOMNodeId": 1,
    "childIds": ["2", "3", "5"]
  },
  {
    "nodeId": "2",
    "ignored": true,
    "role": {"type": "role", "value": "generic"},
    "name": {"type": "computedString", "value": "noise wrapper"},
    "childIds": ["4"]
  },
  {
    "nodeId": "4",
    "role": {"type": "role", "value": "button"},
    "name": {"type": "computedString", "value": "Save settings"},
    "backendDOMNodeId": 42,
    "properties": [
      {"name": "disabled", "value": {"type": "boolean", "value": false}},
      {"name": "focused", "value": {"type": "boolean", "value": true}}
    ]
  },
  {
    "nodeId": "3",
    "role": {"type": "role", "value": "generic"},
    "name": {"type": "computedString", "value": ""},
    "childIds": ["6"]
  },
  {
    "nodeId": "6",
    "role": {"type": "role", "value": "textbox"},
    "name": {"type": "computedString", "value": "Email address"},
    "value": {"type": "string", "value": "ops@example.com"},
    "backendDOMNodeId": 43,
    "properties": [
      {"name": "editable", "value": {"type": "token", "value": "plaintext"}}
    ]
  },
  {
    "nodeId": "5",
    "role": {"type": "role", "value": "custom-widget"},
    "name": {"type": "computedString", "value": "Mystery"}
  }
]`

func TestAXTreeCuratesIgnoredAndGenericNodes(t *testing.T) {
	out, ok := NewAXTree().Compress([]byte(sampleAXTree))
	if !ok {
		t.Fatal("expected valid AX tree to compress")
	}
	text := string(out)
	for _, want := range []string{
		`page "Caveman Console"`,
		`[u16] button "Save settings" {focused}`,
		`[u17] textbox "Email address" = "ops@example.com" {editable}`,
		`custom-widget "Mystery"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compressed snapshot missing %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "noise wrapper") {
		t.Fatalf("ignored node leaked into compressed snapshot:\n%s", text)
	}
	if strings.Contains(text, "generic") {
		t.Fatalf("empty structural generic node should collapse:\n%s", text)
	}
	if strings.Contains(text, `] custom-widget`) {
		t.Fatalf("node with no backend DOM id got fabricated uid:\n%s", text)
	}
	if strings.Contains(text, "disabled") {
		t.Fatalf("false disabled state should not spend output tokens:\n%s", text)
	}
}

func TestAXTreeMalformedFailsClosed(t *testing.T) {
	c := NewAXTree()
	for _, input := range [][]byte{
		[]byte(`{not json`),
		[]byte(`[]`),
		[]byte(`{"nodes":[]}`),
		[]byte(`[{"nodeId":"1","foo":"bar"}]`),
		[]byte(`[{"nodeId":"1","role":{"value":"button"}},{"nodeId":"1","role":{"value":"textbox"}}]`),
	} {
		if out, ok := c.Compress(input); ok || out != nil {
			t.Fatalf("malformed/empty tree should fail closed, got ok=%v out=%q", ok, out)
		}
	}
}

// TestAXTreeDanglingChildBecomesLeaf pins the  fix: an <iframe> node
// whose childId points at a child-document root absent from this frame's
// getFullAXTree response must NOT reject the whole tree. Before the fix,
// validAXTree returned false here and the engine passed the raw AX JSON
// through; after the fix the frame-visible nodes still curate into a usable uid
// map, with the iframe boundary treated as a leaf.
func TestAXTreeDanglingChildBecomesLeaf(t *testing.T) {
	input := `[
	  {"nodeId":"1","role":{"value":"RootWebArea"},"name":{"value":"Host Page"},"backendDOMNodeId":1,"childIds":["2","3"]},
	  {"nodeId":"2","role":{"value":"button"},"name":{"value":"Save settings"},"backendDOMNodeId":10},
	  {"nodeId":"3","role":{"value":"Iframe"},"name":{"value":"Embedded report"},"backendDOMNodeId":11,"childIds":["100"]}
	]`
	out, meta, ok := axTreeCompressor{}.CompressWithMetadata([]byte(input), "")
	if !ok {
		t.Fatal("iframe tree with a cross-frame childId must still compress, not reject")
	}
	text := string(out)
	if !strings.Contains(text, `[ua] button "Save settings"`) {
		t.Fatalf("frame-visible button lost its uid handle:\n%s", text)
	}
	if !strings.Contains(text, `[ub] iframe "Embedded report"`) {
		t.Fatalf("iframe boundary node should survive as a leaf:\n%s", text)
	}
	// The curated view must be the compact record shape, never the raw AX JSON.
	if strings.Contains(text, `"childIds"`) || strings.Contains(text, `"nodeId"`) {
		t.Fatalf("raw AX JSON leaked into curated output:\n%s", text)
	}
	var uidMeta struct {
		UIDs map[string]axUIDTarget `json:"uids"`
	}
	if err := json.Unmarshal(meta.RecoveryMetadata, &uidMeta); err != nil {
		t.Fatalf("decode uid metadata: %v", err)
	}
	if uidMeta.UIDs["ua"].BackendDOMNodeID != 10 {
		t.Fatalf("uid map lost the button target: %+v", uidMeta.UIDs["ua"])
	}
}

func TestAXTreeUIDsAreUniqueAcrossFramesAndDuplicates(t *testing.T) {
	input := `[
	  {"nodeId":"1","role":{"value":"RootWebArea"},"childIds":["2","3"]},
	  {"nodeId":"2","role":{"value":"button"},"name":{"value":"First"},"backendDOMNodeId":42,"frameId":"frame-a"},
	  {"nodeId":"3","role":{"value":"button"},"name":{"value":"Second"},"backendDOMNodeId":42,"frameId":"frame-a"}
	]`
	out, ok := NewAXTree().Compress([]byte(input))
	if !ok {
		t.Fatal("expected valid AX tree to compress")
	}
	seen := map[string]bool{}
	for _, uid := range compactAXUIDs(string(out)) {
		if uid != "" {
			if seen[uid] {
				t.Fatalf("duplicate uid %q in output:\n%s", uid, out)
			}
			seen[uid] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected two actionable uid handles, got %d in %s", len(seen), out)
	}
}

func TestAXTreeUnknownStateTokenIsNotInvented(t *testing.T) {
	input := `[
	  {"nodeId":"1","role":{"value":"RootWebArea"},"childIds":["2"]},
	  {"nodeId":"2","role":{"value":"button"},"name":{"value":"Run"},"backendDOMNodeId":42,
	    "properties":[{"name":"disabled","value":{"type":"token","value":"maybe-later"}}]}
	]`
	out, ok := NewAXTree().Compress([]byte(input))
	if !ok {
		t.Fatal("expected valid AX tree to compress")
	}
	if strings.Contains(string(out), "disabled") {
		t.Fatalf("unknown disabled token was fabricated as true:\n%s", out)
	}
}

func TestAXTreeHighNoiseFixtureReducesIgnoredNodesAndTokens(t *testing.T) {
	input, err := os.ReadFile("testdata/axtree_high_noise.json")
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok := parseAXTree(input)
	if !ok {
		t.Fatal("fixture must be a valid AX tree")
	}
	ignored := 0
	for _, n := range nodes {
		if n.Ignored {
			ignored++
		}
	}
	if ignored*2 < len(nodes) {
		t.Fatalf("fixture should be high-noise, ignored=%d total=%d", ignored, len(nodes))
	}
	out, ok := NewAXTree().Compress(input)
	if !ok {
		t.Fatal("expected valid fixture to compress")
	}
	text := string(out)
	for _, gone := range []string{"layout noise", "css wrapper", "spacer", "generic", "InlineTextBox", "StaticText"} {
		if strings.Contains(text, gone) {
			t.Fatalf("ignored/generic noise leaked into output: %q in\n%s", gone, text)
		}
	}
	for _, want := range []string{"button", "textbox", "heading", "link"} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixture output missing semantic node %s:\n%s", want, text)
		}
	}
	counter := tokens.Default()
	before, after := counter.Count(input), counter.Count(out)
	if after >= before {
		t.Fatalf("expected meaningful token reduction, before=%d after=%d out=%s", before, after, out)
	}
}

func TestAXTreeCapturedFixtureCompactGoldenAndTokenBudget(t *testing.T) {
	input, err := os.ReadFile("testdata/axtree_cdp_local_page.json")
	if err != nil {
		t.Fatal(err)
	}
	out, ok := NewAXTree().Compress(input)
	if !ok {
		t.Fatal("captured CDP fixture must compress")
	}
	want := `page "Caveman AX Fixture" {focused}
  main
    [ua] button "Save settings"
    [u1] textbox "Email address" = "ops@example.com" {editable}
    heading "Operations Overview"
    [uf] link "View trace detail"`
	if string(out) != want {
		t.Fatalf("compact AX golden drifted:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	counter := tokens.Default()
	before, after := counter.Count(input), counter.Count(out)
	if after > 64 || after*50 > before {
		t.Fatalf("compact view exceeded budget: raw=%d compact=%d", before, after)
	}
	t.Logf("captured AX tokens raw=%d compact=%d saved=%.2f%%", before, after, 100*float64(before-after)/float64(before))
}

func TestAXTreeQueryKeepsBestMatchesAncestorsAndVisibleUIDsOnly(t *testing.T) {
	input, err := os.ReadFile("testdata/axtree_cdp_local_page.json")
	if err != nil {
		t.Fatal(err)
	}
	out, meta, ok := axTreeCompressor{}.CompressWithMetadata(input, "save settings")
	if !ok {
		t.Fatal("query-focused AX fixture must compress")
	}
	want := `page "Caveman AX Fixture" {focused}
  main
    [ua] button "Save settings"`
	if string(out) != want {
		t.Fatalf("query-focused view drifted:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	var decoded struct {
		UIDs map[string]axUIDTarget `json:"uids"`
	}
	if err := json.Unmarshal(meta.RecoveryMetadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.UIDs) != 1 || decoded.UIDs["ua"].BackendDOMNodeID != 10 {
		t.Fatalf("query metadata exposed hidden targets: %+v", decoded.UIDs)
	}
}

func TestAXTreeQueryKeepsWholeRowForLineItemMatches(t *testing.T) {
	input := []byte(`[
	  {"nodeId": "1", "role": {"type": "role", "value": "RootWebArea"}, "name": {"type": "computedString", "value": "Orders"}, "backendDOMNodeId": 1, "childIds": ["2"]},
	  {"nodeId": "2", "role": {"type": "role", "value": "table"}, "name": {"type": "computedString", "value": "Orders awaiting review"}, "backendDOMNodeId": 2, "childIds": ["3", "9"]},
	  {"nodeId": "3", "role": {"type": "role", "value": "row"}, "name": {"type": "computedString", "value": ""}, "backendDOMNodeId": 3, "childIds": ["4", "5", "6", "7"]},
	  {"nodeId": "4", "role": {"type": "role", "value": "cell"}, "name": {"type": "computedString", "value": "ORD-0173"}, "backendDOMNodeId": 4},
	  {"nodeId": "5", "role": {"type": "role", "value": "cell"}, "name": {"type": "computedString", "value": "Customer 173"}, "backendDOMNodeId": 5},
	  {"nodeId": "6", "role": {"type": "role", "value": "cell"}, "name": {"type": "computedString", "value": "EUR 193.00"}, "backendDOMNodeId": 6},
	  {"nodeId": "7", "role": {"type": "role", "value": "cell"}, "name": {"type": "computedString", "value": ""}, "backendDOMNodeId": 7, "childIds": ["8"]},
	  {"nodeId": "8", "role": {"type": "role", "value": "button"}, "name": {"type": "computedString", "value": "Review ORD-0173"}, "backendDOMNodeId": 8},
	  {"nodeId": "9", "role": {"type": "role", "value": "row"}, "name": {"type": "computedString", "value": ""}, "backendDOMNodeId": 9, "childIds": ["10", "11"]},
	  {"nodeId": "10", "role": {"type": "role", "value": "cell"}, "name": {"type": "computedString", "value": "ORD-0001"}, "backendDOMNodeId": 10},
	  {"nodeId": "11", "role": {"type": "role", "value": "cell"}, "name": {"type": "computedString", "value": "Customer 1"}, "backendDOMNodeId": 11}
	]`)
	out, _, ok := axTreeCompressor{}.CompressWithMetadata(input, "ORD-0173")
	if !ok {
		t.Fatal("row-match fixture must compress")
	}
	view := string(out)
	for _, sibling := range []string{"Customer 173", "EUR 193.00", "Review ORD-0173"} {
		if !strings.Contains(view, sibling) {
			t.Fatalf("matched row lost sibling cell %q:\n%s", sibling, view)
		}
	}
	if strings.Contains(view, "ORD-0001") {
		t.Fatalf("unmatched row leaked into focused view:\n%s", view)
	}
}

func TestAXTreeQueryMissIsExplicitAndHasNoGuessableTargets(t *testing.T) {
	input, err := os.ReadFile("testdata/axtree_cdp_local_page.json")
	if err != nil {
		t.Fatal(err)
	}
	out, meta, ok := axTreeCompressor{}.CompressWithMetadata(input, "definitely absent control")
	if !ok {
		t.Fatal("query miss should produce an explicit compact view")
	}
	if !strings.Contains(string(out), `note "no accessible match"`) {
		t.Fatalf("query miss was ambiguous:\n%s", out)
	}
	var decoded struct {
		UIDs map[string]axUIDTarget `json:"uids"`
	}
	if err := json.Unmarshal(meta.RecoveryMetadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.UIDs) != 0 {
		t.Fatalf("query miss leaked hidden uid targets: %+v", decoded.UIDs)
	}
}

func FuzzAXTreeDeterministicAndFailClosed(f *testing.F) {
	f.Add([]byte(sampleAXTree), "")
	f.Add([]byte(`{"nodes":[{"nodeId":"1","role":{"value":"button"},"name":{"value":"Save"},"backendDOMNodeId":9}]}`), "save")
	f.Add([]byte(`[{"nodeId":"1","role":{"value":"button"},"childIds":["1"]}]`), "")
	f.Add([]byte(`not json`), "button")

	f.Fuzz(func(t *testing.T, input []byte, query string) {
		if len(input) > 1<<20 || len(query) > 4096 {
			t.Skip()
		}
		first, firstMeta, firstOK := (axTreeCompressor{}).CompressWithMetadata(input, query)
		second, secondMeta, secondOK := (axTreeCompressor{}).CompressWithMetadata(input, query)
		if firstOK != secondOK || string(first) != string(second) || string(firstMeta.RecoveryMetadata) != string(secondMeta.RecoveryMetadata) {
			t.Fatal("AX compression must be deterministic")
		}
		if !firstOK {
			return
		}
		var decoded struct {
			UIDs map[string]axUIDTarget `json:"uids"`
		}
		if err := json.Unmarshal(firstMeta.RecoveryMetadata, &decoded); err != nil {
			t.Fatalf("successful compression returned invalid metadata: %v", err)
		}
	})
}

func compactAXUIDs(snapshot string) []string {
	var out []string
	for _, line := range strings.Split(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		if end := strings.IndexByte(line, ']'); end > 1 {
			out = append(out, line[1:end])
		}
	}
	return out
}
