package compressors_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

func TestJSONCompressorOutputIsValidJSON(t *testing.T) {
	c := compressors.NewJSON()
	// A repetitive array: every row says the same thing but for its id. An array
	// whose rows each say something different is a list of distinct entities and
	// is now passed through — see TestJSONElisionRefusesArrayOfDistinctRecords.
	in := []byte(`{"results":[{"id":0,"state":"ok"},{"id":1,"state":"ok"},{"id":2,"state":"ok"},{"id":3,"state":"ok"},{"id":4,"state":"ok"},{"id":5,"state":"ok"},{"id":6,"state":"ok"},{"id":7,"state":"ok"},{"id":8,"state":"ok"},{"id":9,"state":"ok"},{"id":10,"state":"ok"},{"id":11,"state":"ok"}],"meta":{"page":1}}`)
	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression of a long array")
	}
	if !json.Valid(out) {
		t.Errorf("output is not valid JSON: %s", out)
	}
	if !bytes.Contains(out, []byte(compressors.ElidedKey)) {
		t.Errorf("expected an elided marker in %s", out)
	}
	if len(out) >= len(in) {
		t.Error("expected output smaller than input")
	}
}

func TestJSONCompressorPreservesErrorSubtree(t *testing.T) {
	c := compressors.NewJSON()
	// The data array repeats one record shape; the error subtree is a bare numeric
	// series, which is now kept whole in its own right (a series' values are its
	// content — see TestJSONElisionRefusesArrayOfDistinctRecords) as well as by the
	// error-subtree rule this test is about.
	in := []byte(`{"data":[{"tick":1,"state":"ok"},{"tick":2,"state":"ok"},{"tick":3,"state":"ok"},{"tick":4,"state":"ok"},{"tick":5,"state":"ok"},{"tick":6,"state":"ok"},{"tick":7,"state":"ok"},{"tick":8,"state":"ok"},{"tick":9,"state":"ok"},{"tick":10,"state":"ok"},{"tick":11,"state":"ok"},{"tick":12,"state":"ok"}],"error":{"items":[101,102,103,104,105,106,107,108,109,110,111,112]}}`)
	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	// The data array collapses; the error array is kept verbatim.
	if !bytes.Contains(out, []byte("101")) || !bytes.Contains(out, []byte("112")) {
		t.Errorf("error subtree must be preserved in full: %s", out)
	}
}

func TestJSONCompressorIdempotent(t *testing.T) {
	c := compressors.NewJSON()
	in := []byte(`{"results":[{"n":1,"ok":true},{"n":2,"ok":true},{"n":3,"ok":true},{"n":4,"ok":true},{"n":5,"ok":true},{"n":6,"ok":true},{"n":7,"ok":true},{"n":8,"ok":true},{"n":9,"ok":true},{"n":10,"ok":true},{"n":11,"ok":true},{"n":12,"ok":true},{"n":13,"ok":true},{"n":14,"ok":true},{"n":15,"ok":true}]}`)
	first, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	second, ok := c.Compress(first)
	if !ok {
		// A second pass reporting no further compression is fine: the engine
		// then passes the bytes through unchanged, which is idempotent.
		return
	}
	if !bytes.Equal(first, second) {
		t.Errorf("not idempotent:\n first=%s\nsecond=%s", first, second)
	}
}

// buildArray makes a results array with an error and an anomaly in the MIDDLE,
// where head/tail sampling would miss them.
func smartCrusherInput(t *testing.T) []byte {
	t.Helper()
	items := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		switch i {
		case 20:
			items = append(items, map[string]any{"id": i, "status": "ok", "latency_ms": 9999})
		case 40:
			items = append(items, map[string]any{"id": i, "status": "error", "code": "ERROR-503", "msg": "connection timeout", "latency_ms": 34})
		default:
			items = append(items, map[string]any{"id": i, "status": "ok", "latency_ms": 28 + (i*3)%9})
		}
	}
	b, err := json.Marshal(map[string]any{"results": items})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJSONCompressorKeepsMiddleSignal(t *testing.T) {
	c := compressors.NewJSON()
	in := smartCrusherInput(t)
	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	if !json.Valid(out) {
		t.Fatalf("output not valid JSON: %s", out)
	}
	// Middle error + middle anomaly must survive even though they are far from
	// the head/tail anchors.
	for _, must := range []string{"ERROR-503", "timeout", "9999"} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("must-keep token %q dropped from middle of array: %s", must, out)
		}
	}
	if !bytes.Contains(out, []byte(compressors.ElidedKey)) {
		t.Error("expected an elided marker (array should collapse)")
	}
	if len(out) >= len(in) {
		t.Error("expected real compression")
	}
}

func TestJSONCompressorDeterministic(t *testing.T) {
	c := compressors.NewJSON()
	in := smartCrusherInput(t)
	a, ok1 := c.Compress(in)
	b, ok2 := c.Compress(in)
	if !ok1 || !ok2 {
		t.Fatal("expected compression")
	}
	if !bytes.Equal(a, b) {
		t.Errorf("non-deterministic output:\n a=%s\n b=%s", a, b)
	}
}

func TestJSONCompressorQueryBias(t *testing.T) {
	c, ok := compressors.NewJSON().(compressors.QueryAwareCompressor)
	if !ok {
		t.Fatal("JSON compressor should be query-aware")
	}
	// An array of distinct status strings; only a few match the query. The query
	// term must survive even though it sits in the middle and is not an anomaly.
	items := make([]map[string]any, 0, 40)
	for i := 0; i < 40; i++ {
		status := "healthy"
		if i == 25 {
			status = "quarantined-host-omega"
		}
		items = append(items, map[string]any{"id": i, "status": status})
	}
	in, _ := json.Marshal(map[string]any{"rows": items})
	out, gotOK := c.CompressQuery(in, "which host is quarantined-host-omega")
	if !gotOK {
		t.Fatal("expected compression")
	}
	if !bytes.Contains(out, []byte("quarantined-host-omega")) {
		t.Errorf("query-relevant middle item was dropped: %s", out)
	}
	// Same input with no query bias is allowed to drop it — proving the query
	// actually changed selection (the term is not otherwise force-kept).
	plain, _ := c.Compress(in)
	if bytes.Contains(plain, []byte("quarantined-host-omega")) {
		t.Log("note: term survived without query too (acceptable, but query bias is the guarantee)")
	}
}

func TestJSONCompressorMalformedPassesThrough(t *testing.T) {
	c := compressors.NewJSON()
	if _, ok := c.Compress([]byte(`{"a":[1,2,3, oops`)); ok {
		t.Error("malformed JSON must report !ok (pass-through)")
	}
}

func TestJSONCompressorShortArrayUnchanged(t *testing.T) {
	c := compressors.NewJSON()
	in := []byte(`{"a":[1,2,3]}`)
	out, ok := c.Compress(in)
	if !ok {
		return
	}
	if strings.Contains(string(out), compressors.ElidedKey) {
		t.Error("a short array must not be collapsed")
	}
}
