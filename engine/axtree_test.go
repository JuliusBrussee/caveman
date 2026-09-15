package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

const engineAXTreeFixture = `[
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

func TestAXTreeEngineForcedOnlyCCRRoundTrip(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	eng := New(store, nil)
	if got := eng.Detect([]byte(engineAXTreeFixture)); got != TypeJSON {
		t.Fatalf("AX tree must stay forced-only; Detect = %q, want json", got)
	}
	res, err := eng.Compress([]byte(engineAXTreeFixture), Options{Mode: ModeCompress, Type: TypeA11y})
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if res.RecoveryHandle == "" || res.Ratio <= 0 || res.Basis != BasisInferred || res.Method != TypeA11y {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.LosslessToModel == nil || *res.LosslessToModel {
		t.Fatalf("a11y view must be labeled lossy-to-model: %+v", res)
	}
	original, err := eng.Retrieve(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !bytes.Equal(original, []byte(engineAXTreeFixture)) {
		t.Fatal("CCR did not recover byte-exact original AX tree")
	}
	meta, err := eng.RetrieveMetadata(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve metadata: %v", err)
	}
	var uidMeta struct {
		UIDs map[string]struct {
			BackendDOMNodeID int    `json:"backendDOMNodeId"`
			NodeID           string `json:"nodeId"`
		} `json:"uids"`
	}
	if err := json.Unmarshal(meta, &uidMeta); err != nil {
		t.Fatalf("decode uid map metadata: %v", err)
	}
	if got := uidMeta.UIDs["u16"].BackendDOMNodeID; got != 42 {
		t.Fatalf("uid map did not preserve action target: got backend id %d", got)
	}
}

func TestAXTreeNoCCRPassesThrough(t *testing.T) {
	eng := New(nil, nil)
	res, err := eng.Compress([]byte(engineAXTreeFixture), Options{Mode: ModeCompress, Type: TypeA11y})
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if !bytes.Equal(res.Output, []byte(engineAXTreeFixture)) || res.RecoveryHandle != "" || res.Ratio != 0 {
		t.Fatalf("S4 compressor without CCR must pass through, got %+v", res)
	}
}

func TestAXTreeCapturedCDPFixtureReductionAndExactRecovery(t *testing.T) {
	// Captured from Chrome headless via Accessibility.getFullAXTree against a
	// local data: page. Keep this as real CDP shape coverage; high-noise curation
	// is covered separately in compressors/testdata/axtree_high_noise.json.
	input, err := os.ReadFile("compressors/testdata/axtree_cdp_local_page.json")
	if err != nil {
		t.Fatal(err)
	}
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	eng := New(store, nil)
	res, err := eng.Compress(input, Options{Mode: ModeCompress, Type: TypeA11y})
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if res.Basis != BasisInferred || res.Ratio <= 0 || res.RecoveryHandle == "" {
		t.Fatalf("fixture should report inferred reduction with recovery: %+v", res)
	}
	original, err := eng.Retrieve(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !bytes.Equal(original, input) {
		t.Fatal("fixture recovery was not byte-exact")
	}
	meta, err := eng.RetrieveMetadata(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve metadata: %v", err)
	}
	var uidMeta struct {
		UIDs map[string]struct {
			BackendDOMNodeID int `json:"backendDOMNodeId"`
		} `json:"uids"`
	}
	if err := json.Unmarshal(meta, &uidMeta); err != nil {
		t.Fatalf("decode uid metadata: %v", err)
	}
	if uidMeta.UIDs["ua"].BackendDOMNodeID != 10 {
		t.Fatalf("fixture uid map missing captured button target: %+v", uidMeta.UIDs["ua"])
	}
}
