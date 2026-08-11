package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ordersPage is the payload shape that produced the wrong answer: pretty-printed
// JSON records where one order carries `"status": "unfulfilled"` and the next
// record's `order_id` line follows it immediately in the source text.
func ordersPage(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"orders\": [\n")
	for i := 0; i < 12; i++ {
		status := "fulfilled"
		switch {
		case i == 3:
			status = "unfulfilled"
		case i == 7:
			status = "processing"
		}
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "    {\n      \"order_id\": \"ord-%04d\",\n      \"amount\": \"%d.50\",\n      \"status\": \"%s\"\n    }",
			1040+i, 100+i*37, status)
	}
	b.WriteString("\n  ],\n  \"page\": 2\n}\n")
	return []byte(b.String())
}

// TestQueryViewNeverOrphansAJSONField covers wrong-record attribution when a
// answer. A match on a field line must yield that field's WHOLE record, so no
// status can be read against a neighbouring record's id.
func TestQueryViewNeverOrphansAJSONField(t *testing.T) {
	view, ok := narrowToQuery(ordersPage(t), "unfulfilled orders with order_id, amount, status")
	if !ok {
		t.Fatal("an orders page must narrow")
	}
	text := string(view)

	// Every returned record must parse on its own — that is what "whole unit"
	// means, and it is exactly what line-plucking could not promise.
	records := 0
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || block == nonAdjacentMarker {
			continue
		}
		var order map[string]any
		if err := json.Unmarshal([]byte(block), &order); err != nil {
			t.Fatalf("a returned block is not a complete JSON record: %q", block)
		}
		for _, key := range []string{"order_id", "amount", "status"} {
			if _, present := order[key]; !present {
				t.Fatalf("record is missing %s, so it was returned in pieces: %q", key, block)
			}
		}
		records++
	}
	if records == 0 {
		t.Fatal("no records came back")
	}

	// The specific misreading: the only `unfulfilled` in the view must sit in the
	// same record as ord-1043, and no other id may follow it before its record
	// closes.
	index := strings.Index(text, `"status": "unfulfilled"`)
	if index < 0 {
		t.Fatal("the unfulfilled order should rank for this query")
	}
	record := text[strings.LastIndex(text[:index], "{"):]
	record = record[:strings.Index(record, "}")+1]
	if !strings.Contains(record, "ord-1043") {
		t.Fatalf("the unfulfilled status landed in the wrong record: %q", record)
	}
	if strings.Count(record, `"order_id"`) != 1 {
		t.Fatalf("a record carries more than one id: %q", record)
	}
}

// A field named text/content is ordinary tool data, not permission to detach its
// value from the record that identifies it.
func TestQueryViewKeepsTextBearingJSONRecordsWhole(t *testing.T) {
	content := []byte(`{"items":[{"id":"row-1","text":"delivery failed"},{"id":"row-2","text":"delivery ok"}]}`)
	view, ok := narrowToQuery(content, "failed")
	if !ok {
		t.Fatal("text-bearing records must narrow")
	}
	var matched map[string]any
	for _, block := range strings.Split(string(view), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" || block == nonAdjacentMarker {
			continue
		}
		if err := json.Unmarshal([]byte(block), &matched); err != nil {
			t.Fatalf("text field was orphaned from its record: %q", block)
		}
	}
	if matched["id"] != "row-1" || matched["text"] != "delivery failed" {
		t.Fatalf("query returned wrong or incomplete record: %#v", matched)
	}
}

// A top-level messages array is not proof that JSON came from a provider. Tool
// output can use the same shape, so message ids must remain attached to content.
func TestQueryViewKeepsToolMessageContentWithItsID(t *testing.T) {
	content := []byte(`{"messages":[{"id":"msg-1","role":"customer","content":"refund failed"},{"id":"msg-2","role":"customer","content":"refund complete"}]}`)
	view, ok := narrowToQuery(content, "failed")
	if !ok {
		t.Fatal("message-shaped tool records must narrow")
	}
	text := string(view)
	if !strings.Contains(text, `"id": "msg-1"`) || !strings.Contains(text, `"content": "refund failed"`) {
		t.Fatalf("message content was detached from its id: %q", text)
	}
	if strings.Contains(text, `"id": "msg-2"`) {
		t.Fatalf("irrelevant message unexpectedly ranked: %q", text)
	}
}

// Mixed provider-style content shapes stay as whole message records. Array-form
// content and its sibling facts may not disappear through a string-only path.
func TestQueryViewKeepsMixedMessageContentShapesWhole(t *testing.T) {
	content := []byte(`{"messages":[{"id":"msg-1","role":"user","content":"refund failed"},{"id":"msg-2","role":"user","content":[{"type":"text","text":"image evidence"}]}]}`)
	view, ok := narrowToQuery(content, "refund image evidence")
	if !ok {
		t.Fatal("mixed message content must narrow")
	}
	text := string(view)
	for _, want := range []string{`"id": "msg-1"`, `"content": "refund failed"`, `"id": "msg-2"`, `"text": "image evidence"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("mixed message fact %s disappeared: %q", want, text)
		}
	}
}

// Records from different JSON arrays are not neighbours even when each record
// is the only hit in its array. A visible gap must separate them.
func TestQueryViewMarksSeparateJSONArraysAsNonAdjacent(t *testing.T) {
	content := []byte(`{"errors":[{"id":"err-1","state":"target"}],"warnings":[{"id":"warn-1","state":"target"}]}`)
	view, ok := narrowToQuery(content, "target")
	if !ok {
		t.Fatal("separate record arrays must narrow")
	}
	text := string(view)
	start := strings.Index(text, `"id": "err-1"`)
	end := strings.Index(text, `"id": "warn-1"`)
	if start < 0 || end < 0 || start >= end {
		t.Fatalf("both array records must be present in deterministic source order: %q", text)
	}
	if !strings.Contains(text[start:end], nonAdjacentMarker) {
		t.Fatalf("records from separate arrays were rendered as adjacent: %q", text)
	}
}

// Even when every record matches, extracting them drops the JSON envelope and
// scalar metadata. Leading/trailing markers make that omission explicit.
func TestQueryViewMarksOmittedJSONEnvelope(t *testing.T) {
	content := []byte(`{"system":"policy facts stay outside record units","page":2,"items":[{"id":"row-1","state":"target"},{"id":"row-2","state":"target"}],"total":2}`)
	view, ok := narrowToQuery(content, "target")
	if !ok {
		t.Fatal("record envelope must narrow")
	}
	text := string(view)
	if !strings.HasPrefix(text, nonAdjacentMarker) || !strings.HasSuffix(text, nonAdjacentMarker) {
		t.Fatalf("omitted JSON envelope was not marked at both ends: %q", text)
	}
}

// TestQueryViewMarksEveryGap is the other half: whole units are not enough if two
// of them touch when they did not touch in the original.
func TestQueryViewMarksEveryGap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		subject := "heartbeat ok"
		if i == 2 || i == 30 {
			subject = "delivery failed for endpoint alpha"
		}
		fmt.Fprintf(&b, "ts=t%02d event=%d %s\n", i, i, subject)
	}
	view, ok := narrowToQuery([]byte(b.String()), "delivery failed endpoint alpha")
	if !ok {
		t.Fatal("a log must narrow")
	}
	text := string(view)
	if !strings.Contains(text, "event=2 ") || !strings.Contains(text, "event=30 ") {
		t.Fatalf("both matches should be returned: %q", text)
	}
	between := text[strings.Index(text, "event=2 "):strings.Index(text, "event=30 ")]
	if !strings.Contains(between, nonAdjacentMarker) {
		t.Fatalf("two non-adjacent lines were returned touching: %q", text)
	}
	if !strings.HasPrefix(text, nonAdjacentMarker) {
		t.Fatalf("a view that starts short of the content must say so: %q", text)
	}
	if !strings.HasSuffix(text, nonAdjacentMarker) {
		t.Fatalf("a view that stops short of the content must say so: %q", text)
	}
}

// TestQueryViewKeepsCSVRowsWhole covers the tabular path: whole records sliced out
// of the original bytes, and the header exactly once so the columns can be read.
func TestQueryViewKeepsCSVRowsWhole(t *testing.T) {
	var b strings.Builder
	b.WriteString("txn_id,order_id,state,amount,note\n")
	for i := 0; i < 40; i++ {
		state, note := "charged", "ok"
		if i == 5 || i == 25 {
			state, note = "refunded", "customer dispute, chargeback"
		}
		fmt.Fprintf(&b, "txn-%05d,ord-%04d,%s,%d.03,%q\n", 40000+i, 1000+i, state, 10+i*7, note)
	}
	view, ok := narrowToQuery([]byte(b.String()), "refunded chargeback dispute")
	if !ok {
		t.Fatal("a CSV must narrow")
	}
	text := string(view)
	lines := strings.Split(text, "\n")
	if lines[0] != "txn_id,order_id,state,amount,note" {
		t.Fatalf("the header must head the view: %q", lines[0])
	}
	if strings.Count(text, "txn_id,order_id,state") != 1 {
		t.Fatalf("the header must appear exactly once: %q", text)
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" || line == nonAdjacentMarker {
			continue
		}
		// A whole row carries every column; a plucked fragment would not.
		if got := strings.Count(line, ","); got < 4 {
			t.Fatalf("a returned row is not whole (%d separators): %q", got, line)
		}
		if !strings.HasPrefix(line, "txn-") {
			t.Fatalf("a returned row does not start at a record boundary: %q", line)
		}
	}
	if !strings.Contains(text, nonAdjacentMarker) {
		t.Fatalf("the two matching rows are 20 apart and must be separated: %q", text)
	}
}

// TestQueryViewFallsBackToTheWholeOriginal is the safety valve: JSON whose records
// cannot be identified is never line-split, because a line of JSON is a fragment.
// Over-returning is safe; a fragment is not.
func TestQueryViewFallsBackToTheWholeOriginal(t *testing.T) {
	// A deeply nested config object — no array of records anywhere in it.
	nested := []byte(`{
  "service": {
    "name": "ledger",
    "limits": { "rps": 400, "burst": 900 },
    "status": "unfulfilled orders are retried"
  }
}`)
	if _, ok := narrowToQuery(nested, "unfulfilled status"); ok {
		t.Fatal("record-less JSON must fall back to the full original, not be line-split")
	}

	units, prelude := retrievalUnits(nested)
	if len(units) != 0 || prelude != "" {
		t.Fatalf("record-less JSON must yield no units, got %d: %q", len(units), units)
	}
}

// TestQueryViewKeepsNDJSONLinesWhole covers the one-JSON-object-per-line shape:
// event per line. Each line is already a whole unit, so the fix there is that
// non-adjacent events may never be returned touching.
func TestQueryViewKeepsNDJSONLinesWhole(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		status := "attempted"
		if i%2 == 0 {
			status = "delivered"
		}
		fmt.Fprintf(&b, `{"delivery_id":"dlv-%04d","endpoint":"https://hooks.example/%d","status":"%s"}`+"\n",
			2000+i, i%3, status)
	}
	view, ok := narrowToQuery([]byte(b.String()), "delivered dlv-2018 dlv-2034")
	if !ok {
		t.Fatal("an NDJSON stream must narrow")
	}
	for _, line := range strings.Split(string(view), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == nonAdjacentMarker {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("an NDJSON event came back in pieces: %q", line)
		}
		if _, ok := event["delivery_id"]; !ok {
			t.Fatalf("event is missing its id: %q", line)
		}
	}
}

// TestEmptyQueryIsStillByteExact guards the contract the whole recovery path rests
// on: without a query, nothing about this file may touch the bytes.
func TestEmptyQueryIsStillByteExact(t *testing.T) {
	page := ordersPage(t)
	if narrowed, ok := narrowToQuery(page, "   "); ok {
		t.Fatalf("a blank query must not narrow: %q", narrowed)
	}
}
