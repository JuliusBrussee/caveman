package compressors

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Class-invariant elision summaries.
//
// keepNonRedundant (redundancy.go) keeps one representative of every resemblance
// class and lets the rest elide against it, so a marker's dropped units are all
// near-duplicates of something still visible. The count alone does not say WHAT
// they had in common, and an agent that cannot tell stops trusting the surviving
// view: a compressed ledger can hide a fact that the marker could have stated,
// sending the agent into repeated recovery calls. The marker therefore reports
// facts computed from the elided units instead of only reporting their count.
//
// So a marker carries facts COMPUTED from the units it is replacing, at the
// moment it replaces them:
//
//   - a field whose value is byte-identical across EVERY elided unit is stated as
//     `name=value`;
//   - a field that varies, over a small set of short values, is enumerated in
//     full with exact counts — `status: fulfilled×15 shipped×3 processing×2`,
//     where a unit missing the field lands in its own `absent×N` bucket, so the
//     counts always sum to the elided count;
//   - a field present in every unit whose values all parse as numbers is stated
//     as `name=min..max`, using the ORIGINAL value strings of the extreme units —
//     never a reformatted or rounded number, so the bound can never be wider than
//     what was actually there.
//
// The enumeration is the fact set-logic questions actually turn on. If a varying
// state field is omitted because it is neither constant nor numeric, the agent
// cannot learn whether relevant rows were elided and will recover them anyway.
// The marker must describe the varying field when it can do so completely.
//
// Enumeration is all-or-nothing per field: over five distinct values, or one
// value too long or too free-text to print, and the field is withheld entirely.
// A partial list ("includes fulfilled, shipped") reads as complete and would be
// the one thing this file may never do — imply a fact it did not verify.
//
// Nothing else is claimed. A unit whose text yields no fields at all disables the
// summary for its whole run, and the marker is emitted byte-identically to the
// pre-existing one. A summary that would exceed the budget sheds whole entries —
// identifier-ish enumerations first, then ranges, then constants, class
// enumerations last, because the class enumeration is what a set-logic question
// needs — and never truncates one.
//
// An enumeration in which EVERY bucket holds exactly one unit is not enumerated:
// that is a list of identifiers, not a class invariant. It consumes the budget
// while crowding out state and range facts. Enumerations whose buckets average
// two units or fewer are still printed but shed first.
//
// Such a field instead gets a COVERAGE entry — `order_id: 25 distinct,
// ord-1000..ord-1024` — which answers the one question the other facts cannot:
// membership. A coverage entry settles whether a candidate identifier appeared
// in the elided set without pretending that the bounds form a contiguous set.
//
// The count sits next to the bounds deliberately: `25 distinct, a..z` says
// twenty-five values lie somewhere in that span and claims nothing about the
// gaps. Bounds are the byte-wise smallest and largest of the ORIGINAL value
// strings — no numeric parsing, no ordering assumption, no contiguity.
//
// A run too small to describe is not elided at all. A one-unit marker plus its
// recovery handle costs about what the unit costs, and it reads as a hole. A
// singleton marker can provoke a retrieve for one row. Below
// invariantMinElideUnits a run is only collapsed when it both summarizes and
// still halves its own bytes; otherwise the units are emitted verbatim and the
// compressor claims nothing for them.
//
// The summary spans exactly the units of one marker. Because a fact must hold for
// every unit in that run, a run that happens to span several resemblance classes
// simply yields fewer facts — never a fact true of only one class.
//
// Everything here is order-preserving and map-iteration-free in its output path:
// a compressed block must re-serialize identically on every later turn or the
// provider prefix cache is busted.

// field is one name/value pair extracted from an elided unit, carried as an
// ordered slice rather than a map so the rendered summary is deterministic.
type field struct{ name, value string }

const (
	// invariantMaxBytes caps the whole summary. It is small on purpose: the
	// marker must never eat the reduction it is annotating.
	invariantMaxBytes = 160
	// invariantMaxShareDen bounds the summary to 1/N of the bytes it replaced, so
	// a short run never pays a long annotation. At 1/4 the annotated run is still
	// a 4x reduction; the earlier 1/8 was strict enough to starve exactly the
	// small anomalous classes an agent most needs described, leaving a four-order
	// class with room for one useless identifier enum and nothing else.
	invariantMaxShareDen = 4
	// invariantMinBudget is the floor under that share. A small class is the one
	// worth describing completely — it is the anomaly, not the bulk — so it gets
	// room for a fact or two even when a quarter of its bytes would not buy one.
	// Bounded below by half the run either way, so the marker always shrinks it.
	// Raised from 64 once coverage entries existed: at ~38 bytes each, 64 could
	// not hold a constant pair AND a coverage entry, so a four-row ledger class
	// had to choose between describing its rows and locating them.
	invariantMinBudget = 96
	// invariantMinElideUnits is the smallest run that may be collapsed on the
	// strength of the count alone. Below it, collapsing must earn its place.
	invariantMinElideUnits = 3
	// invariantMinUnits is the smallest run worth annotating. A single elided
	// unit has no class to describe.
	invariantMinUnits = 2
	// invariantMaxValueBytes rejects values too long to belong in a one-line
	// marker (a stack trace, an embedded document).
	invariantMaxValueBytes = 40
	// invariantMaxNameBytes rejects implausible field names.
	invariantMaxNameBytes = 32
	// invariantMaxFieldsPerUnit bounds extraction work per unit.
	invariantMaxFieldsPerUnit = 24
	// invariantMaxTrackedFields bounds how many distinct field names one run
	// accumulates, so a heterogeneous run cannot grow the aggregate without limit.
	// Names past the limit are ignored in document order.
	invariantMaxTrackedFields = 48
	// invariantMaxEnumValues is the largest distinct-value set a varying field may
	// be enumerated over, counting the `absent` bucket. Past it the field says
	// nothing: a longer list stops being a fact an agent can hold and starts being
	// the data it replaced.
	invariantMaxEnumValues = 5
	// invariantMaxEnumValueBytes rejects enumerated values that look like free
	// text rather than a state token.
	invariantMaxEnumValueBytes = 24
	// invariantMaxCoverageValues bounds the distinct-value set a coverage entry
	// counts. Past it the exact count is no longer known, so nothing is claimed.
	invariantMaxCoverageValues = 4096
	// invariantMaxDenseDigits bounds the digit run a dense claim will parse, well
	// inside int64 so the arithmetic that verifies density cannot overflow.
	invariantMaxDenseDigits = 18
	// invariantMinCoverageValues is the smallest distinct set worth a coverage
	// entry: one value is a constant, and two bounds that are the same value say
	// nothing a reader could not already see.
	invariantMinCoverageValues = 2
	// invariantMaxCoverageEntries bounds how many identifier columns one summary
	// describes. A record typically carries several identifier columns, and each
	// entry can consume more of the marker budget than a useful state fact. Entries
	// past the cap are dropped in field order.
	// Entries past the cap are dropped in field order.
	invariantMaxCoverageEntries = 2
)

// invariantEnumAbsent names the bucket for units that lack the field entirely, so
// enumerated counts always sum to the run's elided count.
const invariantEnumAbsent = "absent"

// Entry kinds. kindEnum and kindEnumWeak render identically and in one section;
// they differ only in what gets shed first when the budget binds.
const (
	kindConstant = iota
	kindEnum
	kindEnumWeak
	kindCoverageDense
	kindCoverage
	kindRange
)

// entry is one rendered fact awaiting a place in the summary.
type entry struct {
	kind int
	text string
}

// invariantSecretFieldRe drops fields whose NAME suggests a credential. The
// values live in the elided bytes either way, but a marker is quoted, logged, and
// pasted far more freely than the body it replaced.
var invariantSecretFieldRe = regexp.MustCompile(`(?i)(secret|passw|token|api[_-]?key|authoriz|credential|cookie|session)`)

// summarizeElided renders the facts that hold across every unit of one elided
// run, or "" when there are none, extraction found no structure, or the summary
// would not fit its budget. elidedBytes is the size of the content the marker
// replaces and may be 0 to skip the proportional check.
func summarizeElided(units [][]field, elidedBytes int) string {
	if len(units) < invariantMinUnits {
		return ""
	}

	// One accumulator per field name, built in first-appearance order. A field is
	// registered wherever it first shows up, not only in the first unit: a field
	// that appears late cannot be a constant, but it can still be enumerated with
	// an `absent` bucket for the units that lacked it.
	type agg struct {
		seen           int
		constant       bool
		numeric        bool
		first          string
		minStr, maxStr string
		minVal, maxVal float64
		counts         map[string]int
		valueOrder     []string
		tooWide        bool // more distinct values than may ever be enumerated
		// Coverage tracking runs independently of the enum tally, because the
		// fields it describes are exactly the ones that blow past tooWide.
		covValues      map[string]struct{}
		covMin, covMax string
		covUsable      bool
	}
	aggs := make(map[string]*agg, invariantMaxTrackedFields)
	order := make([]string, 0, invariantMaxTrackedFields)

	for _, fields := range units {
		if len(fields) == 0 {
			// A unit we could not read is a unit we cannot make claims about.
			return ""
		}
		seenHere := make(map[string]bool, len(fields))
		for _, f := range fields {
			if seenHere[f.name] {
				continue // a repeated key within one unit: the first occurrence stands
			}
			seenHere[f.name] = true
			a := aggs[f.name]
			if a == nil {
				if len(order) >= invariantMaxTrackedFields {
					continue
				}
				value, err := strconv.ParseFloat(f.value, 64)
				a = &agg{
					constant:  true,
					numeric:   err == nil,
					first:     f.value,
					minStr:    f.value,
					maxStr:    f.value,
					minVal:    value,
					maxVal:    value,
					counts:    make(map[string]int, invariantMaxEnumValues+1),
					covValues: make(map[string]struct{}),
					covUsable: true,
				}
				aggs[f.name] = a
				order = append(order, f.name)
			}
			a.seen++
			if f.value != a.first {
				a.constant = false
			}
			if a.numeric {
				value, err := strconv.ParseFloat(f.value, 64)
				switch {
				case err != nil:
					a.numeric = false
				default:
					if value < a.minVal {
						a.minVal, a.minStr = value, f.value
					}
					if value > a.maxVal {
						a.maxVal, a.maxStr = value, f.value
					}
				}
			}
			if !a.tooWide {
				if _, known := a.counts[f.value]; !known {
					if len(a.counts) >= invariantMaxEnumValues {
						// A sixth distinct value: stop counting entirely rather than
						// keep a tally that could only ever render as a partial list.
						a.tooWide = true
					} else {
						a.valueOrder = append(a.valueOrder, f.value)
					}
				}
				if !a.tooWide {
					a.counts[f.value]++
				}
			}
			if a.covUsable {
				switch {
				case !invariantEnumValueUsable(f.value) || f.value == invariantEnumAbsent:
					// One free-text value and no range over this field is worth
					// printing — kill the whole entry, never a partial one. A
					// literal `absent` would also be unreadable next to the bucket
					// of the same name.
					a.covUsable = false
				case len(a.covValues) >= invariantMaxCoverageValues:
					a.covUsable = false // the exact distinct count is no longer known
				default:
					if _, known := a.covValues[f.value]; !known {
						a.covValues[f.value] = struct{}{}
						if len(a.covValues) == 1 {
							a.covMin, a.covMax = f.value, f.value
						} else {
							if f.value < a.covMin {
								a.covMin = f.value
							}
							if f.value > a.covMax {
								a.covMax = f.value
							}
						}
					}
				}
			}
		}
	}

	entries := make([]entry, 0, len(order))
	coverages := 0
	for _, name := range order {
		a := aggs[name]
		if name == "" || len(name) > invariantMaxNameBytes || invariantSecretFieldRe.MatchString(name) {
			continue
		}
		switch {
		case a.seen == len(units) && a.constant:
			if invariantValueUsable(a.first) {
				entries = append(entries, entry{kindConstant, name + "=" + a.first})
			}
		case a.seen == len(units) && a.numeric && a.minStr != a.maxStr:
			if invariantValueUsable(a.minStr) && invariantValueUsable(a.maxStr) {
				entries = append(entries, entry{kindRange, name + "=" + a.minStr + ".." + a.maxStr})
			}
		default:
			text, weak := enumerateField(name, a.counts, a.valueOrder, a.tooWide, len(units)-a.seen)
			if text != "" {
				kind := kindEnum
				if weak {
					kind = kindEnumWeak
				}
				entries = append(entries, entry{kind, text})
				continue
			}
			// No class to enumerate — the field has too many distinct values, or a
			// different one in every unit. Either way the useful fact left is which
			// values the run covers. It applies whether or not the values repeat:
			// A delivery identifier may appear more than once, so it is not an
			// identifier by the strict reading, yet its coverage can answer which
			// expected delivery has no event.
			if a.covUsable && coverages < invariantMaxCoverageEntries {
				if cov := coverageEntry(name, a.covValues, len(units)-a.seen, a.covMin, a.covMax); cov != "" {
					kind := kindCoverage
					if strings.HasSuffix(cov, " present") {
						kind = kindCoverageDense
					}
					entries = append(entries, entry{kind, cov})
					coverages++
				}
			}
		}
	}
	if len(entries) == 0 {
		return ""
	}

	budget := summaryBudget(elidedBytes)

	// Shed whole entries until the rendered summary fits: identifier-ish
	// enumerations first (they name units rather than describe them), then ranges,
	// then bounded coverage, then constants, then DENSE coverage, class
	// enumerations last.
	//
	// The two coverage forms are not worth the same and are not shed together. A
	// dense one (`wh-5000..wh-5059 all 60 present`) settles membership outright,
	// which is the entire question a set-difference task asks; a bounded one
	// (`55 distinct, a..z`) only rules values out. Dense coverage is retained later
	// because it can settle membership outright when the facts compete for space.
	//
	// Bounded coverage sits below constants because it runs ~50 bytes an entry
	// against ~16, and on a four-row ledger class promoting it above constants
	// bought `txn_id: 4 distinct, …` by shedding `all state=charged note=ok`,
	// trading the fact that answers "were any refunds in there?" for one naming
	// rows the neighbouring marker already bounds. A range only bounds a column, while
	// a class enumeration answers whether anything in state X was elided. Within a kind the latest field
	// goes first, so the head of a summary is stable as the tail is trimmed.
	selected := make([]bool, len(entries))
	for i := range selected {
		selected[i] = true
	}
	for {
		rendered := renderInvariants(entries, selected)
		if len(rendered) <= budget {
			return rendered
		}
		victim := -1
		for _, kind := range []int{kindEnumWeak, kindRange, kindCoverage, kindConstant, kindCoverageDense, kindEnum} {
			for i := len(entries) - 1; i >= 0; i-- {
				if selected[i] && entries[i].kind == kind {
					victim = i
					break
				}
			}
			if victim >= 0 {
				break
			}
		}
		if victim < 0 {
			return "" // nothing left to shed; the run buys no summary at all
		}
		selected[victim] = false
	}
}

// renderInvariants joins the selected entries into the marker summary: the
// constants as one `all …` section, each enumeration as its own `name: v×n …`
// section, the ranges as one `range …` section.
func renderInvariants(entries []entry, selected []bool) string {
	sections := make([]string, 0, len(entries))
	collect := func(kind int) []string {
		var out []string
		for i, e := range entries {
			if selected[i] && e.kind == kind {
				out = append(out, e.text)
			}
		}
		return out
	}
	if constants := collect(kindConstant); len(constants) > 0 {
		sections = append(sections, "all "+strings.Join(constants, " "))
	}
	// Every `name: …` entry — class enumerations, weak ones, and coverage —
	// renders in one group in field order, whatever the shed priority was.
	for i, e := range entries {
		if selected[i] && (e.kind == kindEnum || e.kind == kindEnumWeak || e.kind == kindCoverage || e.kind == kindCoverageDense) {
			sections = append(sections, e.text)
		}
	}
	if ranges := collect(kindRange); len(ranges) > 0 {
		sections = append(sections, "range "+strings.Join(ranges, " "))
	}
	return strings.Join(sections, "; ")
}

// enumerateField renders `name: value×count …` covering EVERY elided unit, or ""
// when the field cannot be enumerated in full. absent is how many units lacked
// the field; those get their own bucket so the counts always sum to the run size.
//
// Buckets are ordered by descending count (ties keep first-appearance order), so
// the dominant class reads first. Every rejection here is total: there is no
// shortened or partial enumeration, because a list that looks complete and is not
// would let an agent conclude that a value it cannot see was never there.
func enumerateField(name string, counts map[string]int, valueOrder []string, tooWide bool, absent int) (string, bool) {
	if tooWide || len(valueOrder) == 0 {
		return "", false
	}
	type bucket struct {
		value string
		count int
	}
	buckets := make([]bucket, 0, len(valueOrder)+1)
	for _, value := range valueOrder {
		if value == invariantEnumAbsent || !invariantEnumValueUsable(value) {
			return "", false
		}
		buckets = append(buckets, bucket{value, counts[value]})
	}
	if len(buckets)+min(absent, 1) > invariantMaxEnumValues {
		return "", false
	}
	// Real values by descending count (ties keep first-appearance order) so the
	// dominant class reads first; the absent bucket always trails, because it
	// describes the run rather than naming a value the field ever took.
	sort.SliceStable(buckets, func(i, j int) bool { return buckets[i].count > buckets[j].count })
	if absent > 0 {
		buckets = append(buckets, bucket{invariantEnumAbsent, absent})
	}

	// Buckets always sum to the run size, so "every bucket holds one unit" is
	// exactly "this field is a different value in every unit" — an identifier.
	// Enumerating it restates the data instead of describing it, and costs the
	// budget that the fields carrying an actual class would have used.
	units, singletons := 0, 0
	for _, bk := range buckets {
		units += bk.count
		if bk.count == 1 {
			singletons++
		}
	}
	if singletons == len(buckets) {
		return "", false
	}
	// Averaging two units or fewer per bucket is not an identifier but is close to
	// one: printed, but the first thing shed when the budget binds.
	weak := 2*len(buckets) >= units

	var b strings.Builder
	b.WriteString(name)
	b.WriteString(":")
	for _, bk := range buckets {
		b.WriteString(" ")
		b.WriteString(bk.value)
		b.WriteString("×")
		b.WriteString(strconv.Itoa(bk.count))
	}
	return b.String(), weak
}

// summaryBudget is how many bytes of summary a run that dropped elidedBytes may
// buy: a quarter of them, never under invariantMinBudget so a small anomalous
// class can still be described, never over half of them so the marker always
// shrinks what it replaced, and never over the absolute cap.
func summaryBudget(elidedBytes int) int {
	if elidedBytes <= 0 {
		return invariantMaxBytes
	}
	budget := elidedBytes / invariantMaxShareDen
	if budget < invariantMinBudget {
		budget = invariantMinBudget
	}
	if half := elidedBytes / 2; budget > half {
		budget = half
	}
	if budget > invariantMaxBytes {
		budget = invariantMaxBytes
	}
	return budget
}

// worthEliding reports whether a run of dropped units should become a marker at
// all. A run of invariantMinElideUnits or more always is. A shorter one must earn
// it: it has to carry a summary AND still halve its own bytes, because a marker
// plus a recovery handle that costs about what the units cost buys nothing and
// reads as a hole the agent feels obliged to fill.
func worthEliding(unitCount, markerBytes, elidedBytes int, summary string) bool {
	if unitCount >= invariantMinElideUnits {
		return true
	}
	return summary != "" && 2*markerBytes <= elidedBytes
}

// invariantEnumValueUsable holds an enumerated value to a tighter standard than a
// constant: it must read as a state token, not a sentence. Anything with a space,
// or longer than a short identifier, is free text and is not enumerated.
func invariantEnumValueUsable(value string) bool {
	if !invariantValueUsable(value) || len(value) > invariantMaxEnumValueBytes {
		return false
	}
	return !strings.ContainsAny(value, " \t")
}

// invariantValueUsable rejects values that do not belong in a one-line marker:
// empty, oversized, non-printable, or carrying marker syntax that a later pass
// would have to disambiguate.
func invariantValueUsable(value string) bool {
	if value == "" || len(value) > invariantMaxValueBytes || !utf8.ValidString(value) {
		return false
	}
	if strings.Contains(value, "caveman") || strings.Contains(value, "…") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// logfmtPairRe matches a `key=value` token with an optionally quoted value. It is
// deliberately narrow: a key must look like an identifier, so prose containing an
// `=` does not parse as a field.
var logfmtPairRe = regexp.MustCompile(`(?:^|[\s,\[{(])([A-Za-z_][A-Za-z0-9_.\-]*)=("[^"]*"|'[^']*'|[^\s,\]})]+)`)

// lineFields reads one line of a line-oriented payload as whatever it is: a
// complete JSON object (an NDJSON event stream — webhook deliveries, audit
// events, structured application logs all ship this way) or a logfmt line.
//
// Without the JSON arm an NDJSON class can summarize to a bare line count while
// a related table states what was expected. The agent then knows nothing about
// what was delivered and has to recover the event stream to find out.
func lineFields(line []byte) []field {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 1 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		var event any
		if err := decoder.Decode(&event); err == nil && !decoder.More() {
			if fields := objectFields(event); len(fields) > 0 {
				return fields
			}
		}
	}
	return logfmtFields(line)
}

// logfmtFields extracts the `key=value` tokens of one log line, in order.
func logfmtFields(line []byte) []field {
	matches := logfmtPairRe.FindAllSubmatch(line, invariantMaxFieldsPerUnit)
	if len(matches) == 0 {
		return nil
	}
	fields := make([]field, 0, len(matches))
	for _, m := range matches {
		value := string(m[2])
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if value == "" {
			continue
		}
		fields = append(fields, field{name: string(m[1]), value: value})
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// rowFields pairs a table row with its header, in column order. A row whose
// header cell is empty contributes nothing for that column: an unnamed field
// cannot be stated.
func rowFields(header, row []string) []field {
	fields := make([]field, 0, len(row))
	for i, cell := range row {
		if i >= len(header) || i >= invariantMaxFieldsPerUnit {
			break
		}
		name := strings.TrimSpace(header[i])
		value := strings.TrimSpace(cell)
		if name == "" || value == "" {
			continue
		}
		fields = append(fields, field{name: name, value: value})
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// objectFields extracts the scalar top-level members of a JSON object, in sorted
// key order (a decoded object has no inherent order, and sorting is what makes
// the rendered summary reproducible). Nested objects and arrays are skipped:
// stating a fact about a subtree would need a rule for how to render it, and a
// marker has no room for one.
func objectFields(v any) []field {
	object, ok := v.(map[string]any)
	if !ok || len(object) == 0 {
		return nil
	}
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	fields := make([]field, 0, len(names))
	for _, name := range names {
		if len(fields) >= invariantMaxFieldsPerUnit {
			break
		}
		var value string
		switch t := object[name].(type) {
		case string:
			value = t
		case json.Number:
			value = t.String()
		case bool:
			value = strconv.FormatBool(t)
		default:
			continue // null, nested object, nested array
		}
		if value == "" {
			continue
		}
		fields = append(fields, field{name: name, value: value})
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// elisionNotePrefix opens the one-per-payload contract line. It is detected on
// re-entry, so the line is never elided and never appended twice.
const elisionNotePrefix = "… caveman: elided "

// elisionNoteMinElidedBytes is how much a payload must have actually dropped
// before it can afford the contract line. The threshold keeps the contract line
// smaller than the data it describes.
const elisionNoteMinElidedBytes = 4096

// wantsElisionNote reports whether a payload that dropped elidedBytes should
// carry the contract line.
func wantsElisionNote(elidedBytes int) bool { return elidedBytes >= elisionNoteMinElidedBytes }

// elisionNote is the single trailing line that tells the agent how to read a
// view that has had units elided. Every clause is a property the elider actually
// guarantees: keepNonRedundant only ever drops a unit that a surviving unit
// already represents, and summarizeElided only states facts it checks across the
// whole run.
// It is also kept short on purpose. It is paid once per tool result, so a
// paragraph would compete with the data it describes.
func elisionNote(unit string) string {
	return elisionNotePrefix + unit + " resemble shown " + unit +
		" and match the stated invariants — answer from this view; if you truly cannot, one broad caveman_retrieve …"
}

// coverageEntry renders what a run covers for a field with too many distinct
// values to enumerate. Every claim is about the DISTINCT set — how many different
// values the elided units held, and its bounds — never about how often each
// occurred.
//
// The default form, `name: N distinct, min..max`, claims exactly three things:
// how many distinct values the run held, the byte-wise smallest, and the
// byte-wise largest. The count sits next to the bounds so the span is never read
// as a contiguous set — twenty-five values lie SOMEWHERE in
// `ord-1000..ord-1024`, and which twenty-five is not claimed. That is enough to
// rule an id OUT (outside the bounds) but not to rule one IN, which is the
// question set-membership tasks ask: whether a candidate value is in the elided
// set or absent from the stream. The bounded form does not claim membership;
// dense coverage is needed for that.
//
// So when the run happens to be EXACTLY dense — every value one shared prefix
// plus a fixed-width integer, and the integers covering min..max with no gaps —
// it says so: `wh-5000..wh-5599 all 600 present`. That is a checkable literal
// claim (denseCoverage counts it; it is never guessed from the endpoints), and
// it settles membership without a retrieve.
//
// When the run is NOT dense the default form stands, and its own arithmetic is
// informative: `600 distinct, wh-5000..wh-5602` says three ids in that span are
// missing. WHICH three is not claimed and never will be — locating a gap needs
// the data, not the marker.
//
// Units lacking the field get the same `absent×N` bucket the enumerations use, so
// a reader can still account for the whole run. A run with absent units never
// takes the dense form: "all N present" next to "absent×1" reads as a
// contradiction even though the two count different things.
func coverageEntry(name string, values map[string]struct{}, absent int, minValue, maxValue string) string {
	distinct := len(values)
	if distinct < invariantMinCoverageValues || minValue == maxValue {
		return ""
	}
	if absent == 0 {
		if low, high, dense := denseCoverage(values); dense {
			return name + ": " + low + ".." + high + " all " + strconv.Itoa(distinct) + " present"
		}
	}
	text := name + ": " + strconv.Itoa(distinct) + " distinct, " + minValue + ".." + maxValue
	if absent > 0 {
		text += ", " + invariantEnumAbsent + "×" + strconv.Itoa(absent)
	}
	return text
}

// denseCoverage reports whether the DISTINCT values are exactly one shared prefix
// followed by a contiguous run of fixed-width integers, and returns its
// endpoints. `wh-5003..wh-5020 all 18 present` therefore says every one of those
// eighteen ids appears at least once among the elided units — which is what a
// membership question asks — and says nothing about how many times.
//
// Every condition is checked, none assumed:
//
//   - each value ends in digits, and the part before them is identical across all
//     of them (one id space, not two interleaved);
//   - the digit runs are all the SAME WIDTH. Mixed widths admit leading-zero
//     ambiguity — `wh-07` and `wh-7` may or may not be one value — and a claim
//     that turns on how a producer zero-pads is not a claim worth making;
//   - the integers parse exactly and span exactly len(values): max-min+1 equals
//     the distinct count, so there is no room for a gap.
//
// The result does not depend on map iteration order, so the rendered summary
// stays byte-stable across turns.
func denseCoverage(values map[string]struct{}) (string, string, bool) {
	if len(values) < invariantMinCoverageValues {
		return "", "", false
	}
	var (
		prefix          string
		width           int
		low, high       int64
		lowVal, highVal string
		started         bool
	)
	for value := range values {
		body, digits := splitTrailingDigits(value)
		if digits == "" || len(digits) > invariantMaxDenseDigits {
			return "", "", false
		}
		if !started {
			prefix, width = body, len(digits)
		} else if body != prefix || len(digits) != width {
			return "", "", false
		}
		n, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return "", "", false
		}
		switch {
		case !started:
			low, high, lowVal, highVal, started = n, n, value, value, true
		case n < low:
			low, lowVal = n, value
		case n > high:
			high, highVal = n, value
		}
	}
	// The only claim: the span holds exactly as many integers as we counted, so
	// every one of them is present.
	if high-low+1 != int64(len(values)) {
		return "", "", false
	}
	return lowVal, highVal, true
}

// splitTrailingDigits cuts a value into its leading text and its maximal run of
// trailing ASCII digits.
func splitTrailingDigits(value string) (string, string) {
	end := len(value)
	for end > 0 && value[end-1] >= '0' && value[end-1] <= '9' {
		end--
	}
	return value[:end], value[end:]
}
