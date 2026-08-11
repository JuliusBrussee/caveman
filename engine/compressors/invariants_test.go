package compressors

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// elidedBytesFor returns the run size that makes summaryBudget yield exactly
// want, so budget-pressure tests state the budget they mean instead of a magic
// multiple of it.
func elidedBytesFor(want int) int {
	if want < invariantMinBudget {
		return 2 * want
	}
	return 4 * want
}

func fields(pairs ...string) []field {
	out := make([]field, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, field{name: pairs[i], value: pairs[i+1]})
	}
	return out
}

func TestSummarizeElidedStatesOnlyWhatHoldsEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units [][]field
		want  string
	}{
		{
			name: "constant field across every unit",
			units: [][]field{
				fields("state", "charged", "id", "1"),
				fields("state", "charged", "id", "2"),
				fields("state", "charged", "id", "3"),
			},
			want: "all state=charged; range id=1..3",
		},
		{
			name: "a field that varies non-numerically is enumerated in full",
			units: [][]field{
				fields("state", "charged", "region", "emea"),
				fields("state", "refunded", "region", "emea"),
				fields("state", "charged", "region", "emea"),
			},
			want: "all region=emea; state: charged×2 refunded×1",
		},
		{
			name: "units missing the field get their own absent bucket",
			units: [][]field{
				fields("state", "charged", "note", "retry"),
				fields("state", "charged"),
				fields("state", "charged"),
			},
			want: "all state=charged; note: retry×1 absent×2",
		},
		{
			name: "more distinct values than may be enumerated fall back to coverage",
			units: [][]field{
				fields("state", "a", "region", "emea"),
				fields("state", "b", "region", "emea"),
				fields("state", "c", "region", "emea"),
				fields("state", "d", "region", "emea"),
				fields("state", "e", "region", "emea"),
				fields("state", "f", "region", "emea"),
			},
			want: "all region=emea; state: 6 distinct, a..f",
		},
		{
			name: "a sparse identifier is covered by count and bounds",
			units: [][]field{
				fields("customer_id", "cust-2728", "currency", "EUR"),
				fields("customer_id", "cust-2765", "currency", "EUR"),
				fields("customer_id", "cust-2802", "currency", "EUR"),
				fields("customer_id", "cust-2839", "currency", "EUR"),
			},
			want: "all currency=EUR; customer_id: 4 distinct, cust-2728..cust-2839",
		},
		{
			name: "an exactly dense identifier says every value is present",
			units: [][]field{
				fields("delivery_id", "dlv-2000"),
				fields("delivery_id", "dlv-2001"),
				fields("delivery_id", "dlv-2002"),
				fields("delivery_id", "dlv-2003"),
			},
			want: "delivery_id: dlv-2000..dlv-2003 all 4 present",
		},
		{
			name: "coverage counts DISTINCT values, so a repeated id still goes dense",
			units: func() [][]field {
				var units [][]field
				for i := 0; i < 7; i++ {
					for _, st := range []string{"attempted", "delivered"} {
						units = append(units, fields("delivery_id", fmt.Sprintf("dlv-%04d", 2000+i), "status", st))
					}
				}
				return units
			}(),
			want: "delivery_id: dlv-2000..dlv-2006 all 7 present; status: attempted×7 delivered×7",
		},
		{
			name: "one gap and the dense claim is withheld, count and bounds stand",
			units: [][]field{
				fields("delivery_id", "dlv-2000"),
				fields("delivery_id", "dlv-2001"),
				fields("delivery_id", "dlv-2003"),
				fields("delivery_id", "dlv-2004"),
			},
			want: "delivery_id: 4 distinct, dlv-2000..dlv-2004",
		},
		{
			name: "mixed suffix widths are never called dense",
			units: [][]field{
				fields("seq", "n-8"),
				fields("seq", "n-9"),
				fields("seq", "n-10"),
			},
			want: "seq: 3 distinct, n-10..n-9",
		},
		{
			name: "two id spaces sharing a field are never called dense",
			units: [][]field{
				fields("ref", "a-001"),
				fields("ref", "a-002"),
				fields("ref", "b-003"),
			},
			want: "ref: 3 distinct, a-001..b-003",
		},
		{
			name: "values with no digits are never called dense",
			units: [][]field{
				fields("node", "alpha"),
				fields("node", "beta"),
				fields("node", "gamma"),
			},
			want: "node: 3 distinct, alpha..gamma",
		},
		{
			name: "units missing the field keep the counted form",
			units: [][]field{
				fields("delivery_id", "dlv-2000", "svc", "hooks"),
				fields("delivery_id", "dlv-2001", "svc", "hooks"),
				fields("delivery_id", "dlv-2002", "svc", "hooks"),
				fields("svc", "hooks"),
			},
			want: "all svc=hooks; delivery_id: 3 distinct, dlv-2000..dlv-2002, absent×1",
		},
		{
			name: "five distinct values including absent is the limit and still enumerates",
			units: [][]field{
				fields("state", "a"), fields("state", "a"),
				fields("state", "b"), fields("state", "b"),
				fields("state", "c"), fields("state", "c"),
				fields("state", "d"), fields("state", "d"),
				fields("other", "x"), fields("other", "x"),
			},
			want: "state: a×2 b×2 c×2 d×2 absent×2; other: x×2 absent×8",
		},
		{
			name: "free-text values are not enumerated",
			units: [][]field{
				fields("msg", "connection reset by peer", "region", "emea"),
				fields("msg", "upstream closed the stream", "region", "emea"),
			},
			want: "all region=emea",
		},
		{
			name: "a literal absent value would be indistinguishable from the bucket",
			units: [][]field{
				fields("state", "absent"),
				fields("state", "charged"),
			},
			want: "",
		},
		{
			name: "range uses the original extreme value strings, never a reformat",
			units: [][]field{
				fields("amount", "5.00"),
				fields("amount", "199.90"),
				fields("amount", "12.30"),
			},
			want: "range amount=5.00..199.90",
		},
		{
			name: "negative and exponent values order numerically",
			units: [][]field{
				fields("delta", "-3"),
				fields("delta", "1e2"),
				fields("delta", "0"),
			},
			want: "range delta=-3..1e2",
		},
		{
			name: "credential-shaped names are withheld",
			units: [][]field{
				fields("api_key", "abc", "state", "charged"),
				fields("api_key", "def", "state", "charged"),
			},
			want: "all state=charged",
		},
		{
			name:  "a single unit is not a class",
			units: [][]field{fields("state", "charged")},
			want:  "",
		},
		{
			name: "a unit with no extractable structure disables the summary",
			units: [][]field{
				fields("state", "charged"),
				nil,
				fields("state", "charged"),
			},
			want: "",
		},
		{
			name: "a numeric field that varies is ranged, not enumerated",
			units: [][]field{
				fields("code", "200"),
				fields("code", "500"),
				fields("code", "200"),
			},
			want: "range code=200..500",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeElided(tc.units, 1<<20); got != tc.want {
				t.Fatalf("summarizeElided = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSummarizeElidedRespectsItsBudget(t *testing.T) {
	wide := func() []field {
		var f []field
		for i := 0; i < 12; i++ {
			f = append(f, field{name: fmt.Sprintf("field_%02d", i), value: strings.Repeat("v", 30)})
		}
		return f
	}
	units := [][]field{wide(), wide(), wide()}

	got := summarizeElided(units, 1<<20)
	if got == "" {
		t.Fatal("a wide but perfectly constant run should still say something")
	}
	if len(got) > invariantMaxBytes {
		t.Fatalf("summary is %d bytes, over the %d cap: %q", len(got), invariantMaxBytes, got)
	}
	// Every entry that did survive the cap must still be a whole, true fact.
	for _, entry := range strings.Fields(strings.TrimPrefix(got, "all ")) {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, "field_") || value != strings.Repeat("v", 30) {
			t.Fatalf("cap truncated a fact into %q", entry)
		}
	}

	// However tight the run, the summary may never cost more than half the bytes
	// it replaced — the marker always has to shrink what it stands in for.
	for _, elided := range []int{20, 40, 80, 200, 4000} {
		got := summarizeElided([][]field{
			fields("state", "charged", "region", "emea", "tier", "gold"),
			fields("state", "charged", "region", "emea", "tier", "gold"),
		}, elided)
		if 2*len(got) > elided {
			t.Fatalf("a %d-byte run bought a %d-byte summary %q", elided, len(got), got)
		}
	}
}

// TestIdentifierEnumerationsAreShedFirst pins the budget priority. An enumeration
// averaging two units or fewer per bucket barely describes a class, so it goes
// before the range and the constants do.
func TestIdentifierEnumerationsAreShedFirst(t *testing.T) {
	var units [][]field
	for i := 0; i < 8; i++ {
		state := "fulfilled"
		if i >= 6 {
			state = "shipped"
		}
		units = append(units, fields(
			"status", state,
			"batch", fmt.Sprintf("b%d", i/2), // 4 buckets over 8 units: weak
		))
	}
	full := summarizeElided(units, 1<<20)
	if !strings.Contains(full, "status: fulfilled×6 shipped×2") || !strings.Contains(full, "batch:") {
		t.Fatalf("both enumerations should render unconstrained, got %q", full)
	}
	tight := summarizeElided(units, elidedBytesFor(len("status: fulfilled×6 shipped×2")))
	if tight != "status: fulfilled×6 shipped×2" {
		t.Fatalf("the weak enumeration must be shed before the strong one, got %q", tight)
	}
}

// TestBudgetShedsRangesBeforeEnumerations pins the shedding order. An enumeration
// answers whether anything in state X was elided; a range only bounds a column.
func TestBudgetShedsRangesBeforeEnumerations(t *testing.T) {
	var units [][]field
	for i := 0; i < 6; i++ {
		state := "fulfilled"
		if i > 3 {
			state = "shipped"
		}
		units = append(units, fields(
			"amount", fmt.Sprintf("%d.00", 100+i),
			"latency", fmt.Sprintf("%d", 10+i),
			"status", state,
		))
	}
	full := summarizeElided(units, 1<<20)
	if !strings.Contains(full, "status: fulfilled×4 shipped×2") || !strings.Contains(full, "range ") {
		t.Fatalf("expected both an enumeration and ranges unconstrained, got %q", full)
	}
	// A budget too tight for everything must keep the enumeration.
	tight := summarizeElided(units, elidedBytesFor(len("status: fulfilled×4 shipped×2")))
	if tight != "status: fulfilled×4 shipped×2" {
		t.Fatalf("under pressure the enumeration must be what survives, got %q", tight)
	}
}

// TestEnumeratedCountsCoverEveryElidedUnit is the honesty gate for enumeration:
// the counts must account for the whole run, or an agent reading them would
// conclude something about units the marker never saw.
func TestEnumeratedCountsCoverEveryElidedUnit(t *testing.T) {
	units := [][]field{
		fields("status", "fulfilled"), fields("status", "fulfilled"), fields("status", "fulfilled"),
		fields("status", "shipped"), fields("status", "shipped"),
		fields("status", "processing"),
		fields("other", "x"), fields("other", "x"),
	}
	summary := summarizeElided(units, 1<<20)
	for _, section := range strings.Split(summary, "; ") {
		name, body, ok := strings.Cut(section, ": ")
		if !ok {
			t.Fatalf("unexpected section %q", section)
		}
		total := 0
		for _, bucket := range strings.Fields(body) {
			value, count, ok := strings.Cut(bucket, "×")
			if !ok {
				t.Fatalf("bucket %q is not value×count", bucket)
			}
			n, err := strconv.Atoi(count)
			if err != nil {
				t.Fatalf("bucket %q has a non-numeric count", bucket)
			}
			// Re-derive the count from the units themselves.
			actual := 0
			for _, u := range units {
				present := false
				for _, f := range u {
					if f.name != name {
						continue
					}
					present = true
					if f.value == value {
						actual++
					}
				}
				if !present && value == invariantEnumAbsent {
					actual++
				}
			}
			if actual != n {
				t.Fatalf("%s: claimed %s×%d, actually %d", name, value, n, actual)
			}
			total += n
		}
		if total != len(units) {
			t.Fatalf("%s: buckets sum to %d, but %d units were elided", name, total, len(units))
		}
	}
}

func TestLogfmtFieldsExtraction(t *testing.T) {
	got := logfmtFields([]byte(`ts=2026-08-08T09:00:00Z level=INFO msg="settled ok" empty= trailing=1`))
	want := []field{
		{"ts", "2026-08-08T09:00:00Z"},
		{"level", "INFO"},
		{"msg", "settled ok"},
		{"trailing", "1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("field %d = %v, want %v", i, got[i], want[i])
		}
	}
	if logfmtFields([]byte("processing item 12 of 900")) != nil {
		t.Fatal("prose must yield no fields")
	}
}

func TestRowFieldsSkipsUnnamedAndEmptyCells(t *testing.T) {
	got := rowFields([]string{"id", "", "state"}, []string{"7", "x", " charged "})
	want := []field{{"id", "7"}, {"state", "charged"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestObjectFieldsTakesScalarsInSortedOrder(t *testing.T) {
	var v any
	dec := json.NewDecoder(strings.NewReader(`{"state":"charged","amount":5.50,"ok":true,"meta":{"a":1},"tags":[1],"nil":null}`))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	want := []field{{"amount", "5.50"}, {"ok", "true"}, {"state", "charged"}}
	if got := objectFields(v); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if objectFields([]any{1, 2}) != nil {
		t.Fatal("a non-object element yields no fields")
	}
}

// --- end-to-end through the compressors ---

func ledgerCSV(rows int) []byte {
	var b strings.Builder
	b.WriteString("order_id,state,amount,currency\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "ord-%04d,charged,%d.50,EUR\n", i, 5+i%190)
	}
	return []byte(b.String())
}

func settleLog(lines int) []byte {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "ts=2026-08-08T09:%02d:%02dZ level=INFO service=ledger status=200 latency_ms=%d\n",
			(i/60)%60, i%60, 20+i%80)
	}
	return []byte(b.String())
}

func TestTabularMarkerCarriesRowInvariants(t *testing.T) {
	out, ok := NewTabular().Compress(ledgerCSV(400))
	if !ok {
		t.Fatal("ledger CSV must compress")
	}
	text := string(out)
	if !strings.Contains(text, "rows elided (caveman): all state=charged currency=EUR") {
		t.Fatalf("marker did not state the row class invariants:\n%s", text)
	}
	if !strings.Contains(text, "range amount=") {
		t.Fatalf("marker did not state the numeric range:\n%s", text)
	}
	if !strings.Contains(text, elisionNotePrefix) {
		t.Fatalf("an elided table must carry the contract row:\n%s", text)
	}
	if strings.Count(text, elisionNotePrefix) != 1 {
		t.Fatalf("the contract row must appear exactly once:\n%s", text)
	}
}

func TestLogMarkerCarriesLineInvariants(t *testing.T) {
	out, ok := NewLog().Compress(settleLog(600))
	if !ok {
		t.Fatal("settle log must compress")
	}
	text := string(out)
	if !strings.Contains(text, "lines elided (caveman): all level=INFO service=ledger status=200") {
		t.Fatalf("marker did not state the line class invariants:\n%s", text)
	}
	if !strings.Contains(text, "range latency_ms=20..99") {
		t.Fatalf("marker did not state the exact observed range:\n%s", text)
	}
	if strings.Count(text, elisionNotePrefix) != 1 {
		t.Fatalf("the contract line must appear exactly once:\n%s", text)
	}
}

func TestJSONMarkerCarriesElementInvariants(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"rows":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"state":"charged","currency":"EUR"}`, i)
	}
	b.WriteString(`]}`)

	out, ok := NewJSON().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("row array must compress")
	}
	var shaped map[string]any
	if err := json.Unmarshal(out, &shaped); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	marker := map[string]any{}
	for _, element := range shaped["rows"].([]any) {
		if m, isObject := element.(map[string]any); isObject {
			if _, elided := m[ElidedKey]; elided {
				marker = m
			}
		}
	}
	if got := marker[ElidedInvariantsKey]; got != "all currency=EUR state=charged; range id=10..197" {
		t.Fatalf("marker invariants = %v", got)
	}
	if _, ok := marker[ElidedNoteKey]; !ok {
		t.Fatalf("the first elision marker must carry the contract note: %v", marker)
	}
	if n := strings.Count(string(out), ElidedNoteKey); n != 1 {
		t.Fatalf("the contract note must appear exactly once, got %d", n)
	}
}

// TestUnstructuredRunsKeepTheBareMarker is the fallback contract: a payload the
// extractors cannot read must produce exactly the marker this compressor emitted
// before invariants existed, byte for byte.
func TestUnstructuredRunsKeepTheBareMarker(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "processing item %d of 400\n", i)
	}
	out, ok := NewLog().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("repetitive prose log must still compress")
	}
	text := string(out)
	if !strings.Contains(text, "lines elided (caveman) …") {
		t.Fatalf("expected the bare marker:\n%s", text)
	}
	if strings.Contains(text, "elided (caveman):") {
		t.Fatalf("prose has no extractable fields, so nothing may be claimed:\n%s", text)
	}
}

func TestSmallElisionsDoNotBuyTheContractLine(t *testing.T) {
	out, ok := NewLog().Compress(settleLog(12))
	if !ok {
		t.Fatal("expected compression")
	}
	if strings.Contains(string(out), elisionNotePrefix) {
		t.Fatalf("a 12-line payload cannot afford the contract line:\n%s", out)
	}
}

// TestEnrichedMarkersAreIdempotent guards the prefix cache: a compressed block
// must re-serialize identically on every later turn, so re-compressing enriched
// output must return the same bytes rather than annotate the annotation.
func TestEnrichedMarkersAreIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c     Compressor
		input []byte
	}{
		{"log", NewLog(), settleLog(600)},
		{"tabular", NewTabular(), ledgerCSV(400)},
		{"json", NewJSON(), func() []byte {
			var b strings.Builder
			b.WriteString(`{"rows":[`)
			for i := 0; i < 200; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"id":%d,"state":"charged","currency":"EUR"}`, i)
			}
			b.WriteString(`]}`)
			return []byte(b.String())
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			once, ok := tc.c.Compress(tc.input)
			if !ok {
				t.Fatal("expected compression")
			}
			twice, ok := tc.c.Compress(once)
			if ok && string(twice) != string(once) {
				t.Fatalf("second pass changed the payload:\nfirst:\n%s\nsecond:\n%s", once, twice)
			}
		})
	}
}

// TestStatedInvariantsAreLiterallyTrue re-derives every claim a marker makes
// against the rows the marker actually replaced. This is the honesty gate: a
// stated invariant that does not hold for every elided unit is a correctness bug.
func TestStatedInvariantsAreLiterallyTrue(t *testing.T) {
	header := []string{"order_id", "state", "amount", "currency"}
	run := [][]string{
		{"ord-1", "charged", "5.00", "EUR"},
		{"ord-2", "charged", "199.90", "EUR"},
		{"ord-3", "charged", "12.30", "EUR"},
	}
	for len(run) < 60 { // enough elided bytes to afford a summary at all
		run = append(run, []string{fmt.Sprintf("ord-%d", len(run)), "charged", "50.00", "EUR"})
	}
	summary := summarizeRowRun(header, run)
	if summary == "" {
		t.Fatal("expected a summary")
	}
	column := func(name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		t.Fatalf("%q names a column that does not exist", name)
		return -1
	}
	distinctIn := func(col int) map[string]bool {
		values := map[string]bool{}
		for _, row := range run {
			values[row[col]] = true
		}
		return values
	}

	checkedCoverage := false
	for _, section := range strings.Split(summary, "; ") {
		// A coverage section is `name: …`, and its claims are about the DISTINCT
		// values of that column. Re-derive both forms from the run itself.
		if name, body, isNamed := strings.Cut(section, ": "); isNamed && !strings.Contains(name, " ") {
			if bounds, count, dense := strings.Cut(body, " all "); dense && strings.HasSuffix(count, " present") {
				values := distinctIn(column(name))
				low, high, _ := strings.Cut(bounds, "..")
				want := strings.TrimSuffix(count, " present")
				if got := strconv.Itoa(len(values)); got != want {
					t.Fatalf("%s claims %s present, run holds %s distinct", name, want, got)
				}
				if !values[low] || !values[high] {
					t.Fatalf("%s names a bound the run does not hold: %q", name, bounds)
				}
				lowN, _ := strconv.Atoi(low[strings.LastIndex(low, "-")+1:])
				highN, _ := strconv.Atoi(high[strings.LastIndex(high, "-")+1:])
				if highN-lowN+1 != len(values) {
					t.Fatalf("%s claims dense span %s, but %d values cover %d integers",
						name, bounds, len(values), highN-lowN+1)
				}
				checkedCoverage = true
				continue
			}
			if count, bounds, counted := strings.Cut(body, " distinct, "); counted {
				values := distinctIn(column(name))
				if got := strconv.Itoa(len(values)); got != count {
					t.Fatalf("%s claims %s distinct, run holds %s", name, count, got)
				}
				low, high, _ := strings.Cut(bounds, "..")
				if !values[low] || !values[high] {
					t.Fatalf("%s names a bound the run does not hold: %q", name, bounds)
				}
				checkedCoverage = true
				continue
			}
		}
		label, body, _ := strings.Cut(section, " ")
		for _, entry := range strings.Fields(body) {
			name, value, _ := strings.Cut(entry, "=")
			col := column(name)
			switch label {
			case "all":
				for _, row := range run {
					if row[col] != value {
						t.Fatalf("claimed %s for every row, but a row has %q", entry, row[col])
					}
				}
			case "range":
				low, high, _ := strings.Cut(value, "..")
				lowSeen, highSeen := false, false
				for _, row := range run {
					if row[col] == low {
						lowSeen = true
					}
					if row[col] == high {
						highSeen = true
					}
				}
				if !lowSeen || !highSeen {
					t.Fatalf("range %s names a bound no row carries", entry)
				}
			default:
				t.Fatalf("unknown summary section %q", label)
			}
		}
	}
	if !checkedCoverage {
		t.Fatal("this run has an identifier column, so a coverage claim should have been checked")
	}
}

// --- coverage shed priority ---

// TestCoverageShedPriority pins where each coverage form sits in the budget
// queue. A DENSE coverage settles membership outright and outlives even the
// constants; a bounded one only rules values out and goes before them. Both
// outlive the range, and the class enumeration outlives everything.
func TestCoverageShedPriority(t *testing.T) {
	build := func(dense bool) [][]field {
		var units [][]field
		id := 40000
		for i := 0; i < 12; i++ {
			state := "charged"
			if i >= 9 {
				state = "refunded"
			}
			if !dense && i == 4 {
				id++ // one gap, so the coverage can only state bounds
			}
			units = append(units, fields(
				"state", state,
				"currency", "EUR",
				"amount", fmt.Sprintf("%d.00", 100+i),
				"txn_id", fmt.Sprintf("txn-%05d", id),
			))
			id++
		}
		return units
	}
	const (
		enum      = "state: charged×9 refunded×3"
		denseCov  = "txn_id: txn-40000..txn-40011 all 12 present"
		boundsCov = "txn_id: 12 distinct, txn-40000..txn-40012"
	)

	t.Run("dense outlives the constants", func(t *testing.T) {
		units := build(true)
		full := summarizeElided(units, 1<<20)
		for _, want := range []string{"all currency=EUR", enum, denseCov, "range amount="} {
			if !strings.Contains(full, want) {
				t.Fatalf("unconstrained summary is missing %q: %q", want, full)
			}
		}
		want := "all currency=EUR; " + enum + "; " + denseCov
		if got := summarizeElided(units, elidedBytesFor(len(want))); got != want {
			t.Fatalf("the range must be shed first, got %q", got)
		}
		want = enum + "; " + denseCov
		if got := summarizeElided(units, elidedBytesFor(len(want))); got != want {
			t.Fatalf("a dense coverage must outlive the constant, got %q", got)
		}
		if got := summarizeElided(units, elidedBytesFor(len(enum))); got != enum {
			t.Fatalf("the class enumeration must be the last survivor, got %q", got)
		}
	})

	t.Run("bounded goes before the constants", func(t *testing.T) {
		units := build(false)
		full := summarizeElided(units, 1<<20)
		if !strings.Contains(full, boundsCov) {
			t.Fatalf("a gapped run must still state its bounds: %q", full)
		}
		if strings.Contains(full, "present") {
			t.Fatalf("a gapped run must never claim density: %q", full)
		}
		want := "all currency=EUR; " + enum
		if got := summarizeElided(units, elidedBytesFor(len(want))); got != want {
			t.Fatalf("a bounded coverage must be shed before the constant, got %q", got)
		}
	})
}

// --- small classes are not elided at all ---

// TestTinyClassesAreNotElided: a one- or two-unit marker plus its recovery handle
// costs about what the units cost, and it reads as a hole. A singleton elision
// marker can provoke a retrieve for one row. Below invariantMinElideUnits the
// units are emitted verbatim unless
// collapsing both summarizes and halves their bytes.
func TestTinyClassesAreNotElided(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"orders":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		status, hold := "fulfilled", ""
		if i%7 == 3 {
			status, hold = "unfulfilled", `,"hold_reason":"timeout"`
		}
		fmt.Fprintf(&b, `{"order_id":"ord-%03d","status":"%s","currency":"EUR","amount":"%d.50"%s}`, i, status, 10+i*7, hold)
	}
	b.WriteString(`]}`)

	out, ok := NewJSON().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected compression")
	}
	var shaped map[string]any
	if err := json.Unmarshal(out, &shaped); err != nil {
		t.Fatal(err)
	}
	for _, element := range shaped["orders"].([]any) {
		m, isObject := element.(map[string]any)
		if !isObject {
			continue
		}
		count, elided := m[ElidedKey]
		if !elided {
			continue
		}
		if n := int(count.(float64)); n < invariantMinElideUnits {
			if _, described := m[ElidedInvariantsKey]; !described {
				t.Errorf("a %d-element marker carries no facts at all: %v — it should not have been elided", n, m)
			}
		}
	}
}

func TestTinyCSVClassesAreNotElided(t *testing.T) {
	var b strings.Builder
	b.WriteString("txn_id,order_id,state,amount\n")
	for i := 0; i < 120; i++ {
		state := "charged"
		if i%23 == 7 {
			state = "refunded"
		}
		fmt.Fprintf(&b, "txn-%05d,ord-%04d,%s,%d.03\n", 40000+i, 1000+i, state, 10+i*7)
	}
	out, ok := NewTabular().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected compression")
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "rows elided (caveman)") && !strings.Contains(line, "rows elided (caveman):") {
			t.Errorf("a marker rendered with no invariants at all: %q", line)
		}
	}
}

func TestTinyLogClassesAreNotElided(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		if i%9 == 4 {
			fmt.Fprintf(&b, "ts=t%02d level=ERROR service=ledger status=500 detail=upstream\n", i)
			continue
		}
		fmt.Fprintf(&b, "ts=t%02d level=INFO service=ledger status=200 latency_ms=%d\n", i, 20+i)
	}
	out, ok := NewLog().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected compression")
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "lines elided (caveman)") && !strings.Contains(line, "lines elided (caveman):") {
			t.Errorf("a marker rendered with no invariants at all: %q", line)
		}
	}
}

// TestNDJSONLinesSummarizeAsEvents: a line that is a whole JSON object is read as
// one, not run through the logfmt matcher that cannot see `"k":"v"`. Without this
// the entire NDJSON event-stream class summarized to a bare count.
func TestNDJSONLinesSummarizeAsEvents(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		for _, status := range []string{"attempted", "delivered"} {
			fmt.Fprintf(&b, `{"delivery_id":"wh-%04d","endpoint":"https://hooks.example.com/billing","status":"%s"}`+"\n",
				5000+i, status)
		}
	}
	out, ok := NewLog().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected compression")
	}
	text := string(out)
	if !strings.Contains(text, "delivery_id: wh-") || !strings.Contains(text, " present") {
		t.Fatalf("an NDJSON event run must state its id coverage: %q", text)
	}
	if !strings.Contains(text, "status: ") {
		t.Fatalf("an NDJSON event run must state its status classes: %q", text)
	}
}
