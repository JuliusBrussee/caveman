package redact

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func mustPayload(t *testing.T, body []byte, rules []Rule) ([]byte, RedactionReport) {
	t.Helper()
	out, report, err := Payload(body, rules)
	if err != nil {
		t.Fatalf("Payload returned error: %v", err)
	}
	return out, report
}

func TestPayloadCatchesEveryBuiltin(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantRule string
	}{
		{"email", "Please contact alice.smith+tag@example.com about the order.", "email"},
		{"bearer-token", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345", "bearer-token"},
		{"credit-card", "paid with 4111 1111 1111 1111 yesterday", "credit-card"},
		{"credit-card-unspaced", "paid with 4111111111111111 yesterday", "credit-card"},
		{"ssn", "his number is 123-45-6789 on file", "ssn"},
		{"ssn-labeled", "SSN: 123456789 on file", "ssn"},
		{"sk-prefixed-key", "key sk-abcdefghijklmnopqrstuvwxyz1234", "sk-prefixed-key"},
		{"aws-access-key", "creds AKIAIOSFODNN7EXAMPLE here", "aws-access-key"},
		{"cave-project-key", "use cave_live_abcdefghijkl_mnopqrstuvwx now", "cave-project-key"},
		{"dsn-with-credentials", "dsn postgres://user:hunter2@db.internal:5432/app", "dsn-with-credentials"},
		{
			"pem-private-key",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----",
			"pem-private-key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, report := mustPayload(t, []byte(tc.body), nil)
			if bytes.Equal(out, []byte(tc.body)) {
				t.Fatalf("body unchanged, expected %q to fire: %s", tc.wantRule, out)
			}
			var found bool
			for _, f := range report.Findings {
				if f.Rule == tc.wantRule {
					found = true
					if f.Count < 1 {
						t.Fatalf("finding %q has count %d", f.Rule, f.Count)
					}
					if f.Origin != OriginBuiltin {
						t.Fatalf("finding %q origin = %q, want %q", f.Rule, f.Origin, OriginBuiltin)
					}
				}
			}
			if !found {
				t.Fatalf("report has no finding for %q: %+v", tc.wantRule, report.Findings)
			}
			if !bytes.Contains(out, []byte("[REDACTED:"+tc.wantRule+"]")) {
				t.Fatalf("output missing typed placeholder for %q: %s", tc.wantRule, out)
			}
		})
	}
}

func TestPayloadPreservesSurroundingShape(t *testing.T) {
	// A redactor that blanks whole lines destroys the corpus. Only the matched
	// span may go, and structural context (the Bearer scheme, the SSN label,
	// the JSON keys) must survive.
	body := []byte(`{"user":"bob@example.com","note":"call me","auth":"Bearer abcdefghijklmnopqrstuvwxyz012345"}`)
	out, _ := mustPayload(t, body, nil)
	for _, keep := range []string{`{"user":"`, `"note":"call me"`, `"auth":"Bearer `, `"}`} {
		if !bytes.Contains(out, []byte(keep)) {
			t.Fatalf("output lost context %q: %s", keep, out)
		}
	}
	if bytes.Contains(out, []byte("bob@example.com")) {
		t.Fatalf("email survived: %s", out)
	}
}

func TestPayloadDoesNotOverRedactOrdinaryNumbers(t *testing.T) {
	// 1111111111111111 fails the Luhn check; 666-45-6789 is not a valid SSN
	// area. Neither may be touched, or every invoice line in the corpus dies.
	body := []byte("order 1111 1111 1111 1111 total 1234567890 ref 666-45-6789 qty 42")
	out, report := mustPayload(t, body, nil)
	if !bytes.Equal(out, body) {
		t.Fatalf("over-redacted benign numbers: %s (findings %+v)", out, report.Findings)
	}
	if report.TotalMatches != 0 {
		t.Fatalf("TotalMatches = %d, want 0", report.TotalMatches)
	}
}

func TestPayloadOrgRuleCannotDisableBuiltin(t *testing.T) {
	body := []byte("mail bob@example.com now")
	// Every shape an org could try: a same-named rule, a builtin-typed row
	// naming the builtin, and a rule whose replacement re-emits the match.
	rules := []Rule{
		{Name: "email", Type: RuleTypeRegex, Pattern: `zzz-never-matches`, Replacement: "x"},
		{Name: "email", Type: RuleTypeBuiltin, Pattern: "email", Replacement: "$0"},
		{Name: "passthrough", Type: RuleTypeRegex, Pattern: `bob@example\.com`, Replacement: "bob@example.com"},
	}
	out, report := mustPayload(t, body, rules)
	if bytes.Contains(out, []byte("bob@example.com")) {
		t.Fatalf("org rules suppressed a builtin: %s", out)
	}
	if !bytes.Contains(out, []byte("[REDACTED:email]")) {
		t.Fatalf("builtin email placeholder missing: %s", out)
	}
	var sawBuiltinEmail bool
	for _, f := range report.Findings {
		if f.Rule == "email" && f.Origin == OriginBuiltin {
			sawBuiltinEmail = true
		}
	}
	if !sawBuiltinEmail {
		t.Fatalf("builtin email finding missing: %+v", report.Findings)
	}
}

func TestPayloadAppliesOrgRules(t *testing.T) {
	body := []byte("internal ticket CAVE-9182 raised")
	rules := []Rule{{Name: "ticket", Type: RuleTypeRegex, Pattern: `CAVE-\d+`, Replacement: "[TICKET]", Priority: 10}}
	out, report := mustPayload(t, body, rules)
	if !bytes.Contains(out, []byte("[TICKET]")) {
		t.Fatalf("org rule did not fire: %s", out)
	}
	var found bool
	for _, f := range report.Findings {
		if f.Rule == "ticket" {
			found = true
			if f.Origin != OriginOrg {
				t.Fatalf("origin = %q, want %q", f.Origin, OriginOrg)
			}
		}
	}
	if !found {
		t.Fatalf("org finding missing: %+v", report.Findings)
	}
}

func TestPayloadOrgReplacementIsLiteral(t *testing.T) {
	// An org's replacement text is data, not a regexp template. $1 must not
	// expand a captured group back into the output.
	body := []byte("secret-token-value")
	rules := []Rule{{Name: "literal", Type: RuleTypeRegex, Pattern: `secret-(token)-value`, Replacement: "kept:$1"}}
	out, _ := mustPayload(t, body, rules)
	if !bytes.Equal(out, []byte("kept:$1")) {
		t.Fatalf("replacement expanded a group: %s", out)
	}
}

func TestPayloadDeterministicAcrossRuleOrder(t *testing.T) {
	body := []byte("mail bob@example.com ticket CAVE-1 code XYZ-99 card 4111111111111111")
	a := []Rule{
		{Name: "ticket", Type: RuleTypeRegex, Pattern: `CAVE-\d+`, Replacement: "[T]", Priority: 10},
		{Name: "code", Type: RuleTypeRegex, Pattern: `XYZ-\d+`, Replacement: "[C]", Priority: 20},
	}
	b := []Rule{a[1], a[0]}

	outA, repA := mustPayload(t, body, a)
	outB, repB := mustPayload(t, body, b)
	if !bytes.Equal(outA, outB) {
		t.Fatalf("rule slice order changed output:\n%s\n%s", outA, outB)
	}
	jsonA, _ := json.Marshal(repA)
	jsonB, _ := json.Marshal(repB)
	if !bytes.Equal(jsonA, jsonB) {
		t.Fatalf("rule slice order changed report:\n%s\n%s", jsonA, jsonB)
	}
	for i := 0; i < 25; i++ {
		out, rep := mustPayload(t, body, a)
		if !bytes.Equal(out, outA) {
			t.Fatalf("run %d output drifted", i)
		}
		raw, _ := json.Marshal(rep)
		if !bytes.Equal(raw, jsonA) {
			t.Fatalf("run %d report drifted", i)
		}
	}
}

func TestPayloadOverlappingOrgRulesResolveByPriority(t *testing.T) {
	body := []byte("value ABC-123 here")
	high := Rule{Name: "narrow", Type: RuleTypeRegex, Pattern: `ABC-123`, Replacement: "[N]", Priority: 1}
	low := Rule{Name: "broad", Type: RuleTypeRegex, Pattern: `ABC-\d+`, Replacement: "[B]", Priority: 50}
	forward, _ := mustPayload(t, body, []Rule{high, low})
	reverse, _ := mustPayload(t, body, []Rule{low, high})
	if !bytes.Equal(forward, reverse) {
		t.Fatalf("priority not honoured independent of slice order:\n%s\n%s", forward, reverse)
	}
	if !bytes.Contains(forward, []byte("[N]")) {
		t.Fatalf("lower priority number should win: %s", forward)
	}
}

func TestPayloadFailsClosedOnBadRules(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
	}{
		{"unsupported json_path", Rule{Name: "j", Type: RuleTypeJSONPath, Pattern: "$.a"}},
		{"unsupported header", Rule{Name: "h", Type: RuleTypeHeader, Pattern: "x-secret"}},
		{"unknown type", Rule{Name: "u", Type: "sorcery", Pattern: "x"}},
		{"empty type", Rule{Name: "e", Pattern: "x"}},
		{"invalid regex", Rule{Name: "bad", Type: RuleTypeRegex, Pattern: `([`}},
		{"empty pattern", Rule{Name: "empty", Type: RuleTypeRegex, Pattern: ""}},
		{"empty name", Rule{Name: "", Type: RuleTypeRegex, Pattern: "x"}},
		{"matches empty string", Rule{Name: "star", Type: RuleTypeRegex, Pattern: `x*`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := Payload([]byte("body with x"), []Rule{tc.rule})
			if err == nil {
				t.Fatalf("expected error, got clean output %q", out)
			}
			if out != nil {
				t.Fatalf("failing pass must return nil body, got %q", out)
			}
		})
	}
}

func TestPayloadRejectsOversizeBody(t *testing.T) {
	body := make([]byte, MaxPayloadBytes+1)
	for i := range body {
		body[i] = 'a'
	}
	out, _, err := Payload(body, nil)
	if err == nil {
		t.Fatal("expected ErrBodyTooLarge")
	}
	if out != nil {
		t.Fatal("oversize body must not return a body")
	}
}

func TestPayloadReportNeverEchoesMatches(t *testing.T) {
	secrets := []string{
		"alice@example.com",
		"abcdefghijklmnopqrstuvwxyz012345",
		"4111111111111111",
		"123-45-6789",
		"sk-abcdefghijklmnopqrstuvwxyz1234",
		"AKIAIOSFODNN7EXAMPLE",
		"hunter2",
		"CAVE-9182",
	}
	body := []byte("alice@example.com Bearer abcdefghijklmnopqrstuvwxyz012345 4111111111111111 " +
		"123-45-6789 sk-abcdefghijklmnopqrstuvwxyz1234 AKIAIOSFODNN7EXAMPLE " +
		"postgres://u:hunter2@h/d CAVE-9182")
	rules := []Rule{{Name: "ticket", Type: RuleTypeRegex, Pattern: `CAVE-\d+`, Replacement: "[T]"}}
	out, report := mustPayload(t, body, rules)

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for _, s := range secrets {
		if strings.Contains(string(raw), s) {
			t.Fatalf("report echoed %q: %s", s, raw)
		}
		if bytes.Contains(out, []byte(s)) {
			t.Fatalf("body retained %q: %s", s, out)
		}
	}
	if report.TotalMatches < len(secrets) {
		t.Fatalf("TotalMatches = %d, want >= %d (%+v)", report.TotalMatches, len(secrets), report.Findings)
	}
	if report.BytesIn != len(body) {
		t.Fatalf("BytesIn = %d, want %d", report.BytesIn, len(body))
	}
	if report.BytesOut != len(out) {
		t.Fatalf("BytesOut = %d, want %d", report.BytesOut, len(out))
	}
	if report.RuleSetHash == "" {
		t.Fatal("RuleSetHash empty")
	}
}

func TestPayloadRuleSetHashTracksRules(t *testing.T) {
	body := []byte("nothing here")
	_, base := mustPayload(t, body, nil)
	_, same := mustPayload(t, body, nil)
	if base.RuleSetHash != same.RuleSetHash {
		t.Fatal("hash unstable for identical rule sets")
	}
	_, other := mustPayload(t, body, []Rule{{Name: "t", Type: RuleTypeRegex, Pattern: `q+`, Replacement: "[T]"}})
	if base.RuleSetHash == other.RuleSetHash {
		t.Fatal("hash did not change when org rules were added")
	}
}

// adversarialBody builds a body of near-miss shapes chosen to maximise work:
// long digit runs, repeated "Bearer " prefixes with tails too short to match, a
// PEM header that never closes, and broken email fragments.
func adversarialBody(size int) []byte {
	var b bytes.Buffer
	chunk := "Bearer short 1234567890123456789 4111-1111-1111-111 " +
		"-----BEGIN RSA PRIVATE KEY----- aaaaaaaaaaaaaaaaaaaa " +
		"user@ @example.com 000-00-0000 sk-short ssn 000000000 "
	for b.Len() < size {
		b.WriteString(chunk)
	}
	return b.Bytes()
}

// TestPayloadLargeAdversarialBodyAtDefaultCaptureCeiling runs at capture's
// default CAVE_DATA_COLLECTION_MAX_BODY_BYTES setting of 4 MiB. Operators may
// raise that setting up to Payload's 8 MiB hard cap. This keeps the unit-test
// contract deterministic: every near-miss survives unchanged and the pass
// completes without an error. Throughput belongs to
// BenchmarkPayloadAdversarial4MiB; absolute wall-clock limits vary with machine
// speed and concurrent CI load.
//
// Measured 2026-07-31 (Apple M3 Pro, BenchmarkPayloadAdversarial4MiB): 569ms/op,
// 7.37 MB/s — worst case by construction, down from 698ms after the
// labelled-SSN gap was narrowed to one {0,16} window and pem-private-key was
// prescreened on its CLOSING delimiter. A typical 30 KB JSON chat request costs
// 2.13ms at 14.31 MB/s (BenchmarkPayloadTypicalRequest).
//
// This fixture is built to defeat the needle prescreen, so it is not a latency
// budget for real traffic — it is the floor of what seven separate automata
// cost over 4 MiB. Getting materially under it means merging the built-in
// patterns into one automaton, which is a redesign, not a tuning pass.
func TestPayloadLargeAdversarialBodyAtDefaultCaptureCeiling(t *testing.T) {
	const captureCeiling = 4 << 20
	body := adversarialBody(captureCeiling)
	if len(body) < captureCeiling {
		t.Fatalf("fixture is %d bytes, want >= %d", len(body), captureCeiling)
	}

	out, report, err := Payload(body, []Rule{{Name: "t", Type: RuleTypeRegex, Pattern: `CAVE-\d+`, Replacement: "[T]"}})
	if err != nil {
		t.Fatalf("Payload errored on large body: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Fatal("near-miss fixture changed")
	}
	if report.TotalMatches != 0 {
		t.Fatalf("near-miss fixture produced %d matches", report.TotalMatches)
	}
}

// TestPayloadAllocationsDoNotScaleWithFiringRules guards the fix for the
// per-firing-rule full-body copies. Redaction runs inline in the proxy handler
// under concurrency, so allocating two full-body copies for every rule that
// fires turns each capture into tens of megabytes of garbage.
//
// The invariant is per-firing-rule cost, not total: a rule that fires must
// produce a new body, so ~1 body copy each is the floor of an immutable
// design. What must NOT happen is ~2 each — one from a doubled output buffer
// and one from re-deriving the prescreen. Measured on a 1 MiB body with 7 rules
// firing: 2.16 copies per firing rule before the fix, 1.14 after.
func TestPayloadAllocationsDoNotScaleWithFiringRules(t *testing.T) {
	const size = 1 << 20
	quiet := bytes.Repeat([]byte("the quick brown fox jumps over it. "), size/35)
	noisy := append(append([]byte{}, quiet...),
		[]byte(" alice@example.com Bearer abcdefghijklmnopqrstuvwxyz012345 4111111111111111 "+
			"123-45-6789 sk-abcdefghijklmnopqrstuvwxyz1234 AKIAIOSFODNN7EXAMPLE postgres://u:p@h/d")...)

	measure := func(body []byte) (uint64, RedactionReport) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, report, err := Payload(body, nil)
		if err != nil {
			t.Fatalf("Payload: %v", err)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, report
	}

	quietBytes, quietReport := measure(quiet)
	noisyBytes, noisyReport := measure(noisy)
	if len(quietReport.Findings) != 0 {
		t.Fatalf("clean body fired %d rules", len(quietReport.Findings))
	}
	firing := len(noisyReport.Findings)
	if firing < 5 {
		t.Fatalf("fixture only fired %d rules, want at least 5", firing)
	}

	perRuleCopies := float64(noisyBytes-quietBytes) / float64(firing) / float64(len(quiet))
	if perRuleCopies > 1.5 {
		t.Fatalf("%.2f full-body copies per firing rule (%d rules, %d vs %d bytes); want <= 1.5",
			perRuleCopies, firing, noisyBytes, quietBytes)
	}
}

// BenchmarkPayloadAdversarial4MiB owns the throughput regression signal. Run it
// after any pattern change and compare against a saved baseline with benchstat:
//
//	go test ./shared/platform/redact/ -run '^$' -bench PayloadAdversarial -benchmem
func BenchmarkPayloadAdversarial4MiB(b *testing.B) {
	body := adversarialBody(4 << 20)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Payload(body, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPayloadTypicalRequest is the shape that actually runs in production:
// a JSON chat request with one email in it.
func BenchmarkPayloadTypicalRequest(b *testing.B) {
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"` +
		strings.Repeat("summarise the attached report for me. ", 800) +
		`reply to dana@example.com"}]}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Payload(body, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestPayloadNilAndEmptyBody(t *testing.T) {
	out, report, err := Payload(nil, nil)
	if err != nil {
		t.Fatalf("nil body errored: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("nil body produced %q", out)
	}
	if report.TotalMatches != 0 {
		t.Fatalf("TotalMatches = %d", report.TotalMatches)
	}
}

func TestPayloadDoesNotMutateInput(t *testing.T) {
	body := []byte("mail bob@example.com now")
	original := append([]byte(nil), body...)
	if _, _, err := Payload(body, nil); err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if !bytes.Equal(body, original) {
		t.Fatalf("input mutated: %s", body)
	}
}

func TestPayloadLabelledGovernmentIDs(t *testing.T) {
	caught := []string{
		"SSN: 123456789 on file",
		"ssn 123456789",
		"SS# 123456789",
		"SS #123456789",
		"social security 123456789",
		"Social Security Number: 123456789",
		"social sec 123456789",
		"TIN 123456789",
		"ITIN: 123456789",
		"tax id 123456789",
		"tax_id: 123456789",
		// A multi-line form dump must not walk past the gap.
		"SSN\n123456789",
		"Social Security Number\n123456789",
	}
	for _, body := range caught {
		t.Run(body, func(t *testing.T) {
			out, report := mustPayload(t, []byte(body), nil)
			if bytes.Contains(out, []byte("123456789")) {
				t.Fatalf("labelled id survived: %s", out)
			}
			if !bytes.Contains(out, []byte("[REDACTED:ssn]")) {
				t.Fatalf("no ssn placeholder: %s", out)
			}
			if report.TotalMatches != 1 {
				t.Fatalf("TotalMatches = %d, want 1", report.TotalMatches)
			}
		})
	}

	// The label must be a word, not a substring, and an unlabelled 9-digit run
	// stays — that is the deliberate trade that keeps the corpus usable.
	untouched := []string{
		"order 123456789 shipped",
		"class# 123456789",
		"created 1731628800123 ms",
		"protein 123456789",
	}
	for _, body := range untouched {
		t.Run("untouched/"+body, func(t *testing.T) {
			out, _ := mustPayload(t, []byte(body), nil)
			if !bytes.Equal(out, []byte(body)) {
				t.Fatalf("over-redacted %q -> %q", body, out)
			}
		})
	}
}

// payloadNoPrescreen is the reference implementation: every rule runs against
// every body, with no needle prescreen at all. Payload must agree with it
// exactly — that is what makes the prescreen (and its deliberately stale
// re-derivation) an optimisation rather than a behaviour change.
func payloadNoPrescreen(t *testing.T, body []byte, rules []Rule) []byte {
	t.Helper()
	orgRules, _, err := compileOrgRules(rules)
	if err != nil {
		t.Fatalf("compileOrgRules: %v", err)
	}
	out := body
	for _, step := range append(append([]compiledRule{}, builtinPayloadRules...), orgRules...) {
		next, _ := step.apply(out)
		out = next
	}
	return out
}

// TestPayloadStalePrescreenCannotDropAMatch is the guard on the optimisation in
// finding 3. The prescreen is derived once and re-derived only after a rule
// whose replacement can introduce a needle; if that bookkeeping is ever wrong,
// a later rule gets skipped and a secret survives. Each body below is built so
// a REPLACEMENT, not the original text, is what puts a needle in play.
func TestPayloadStalePrescreenCannotDropAMatch(t *testing.T) {
	bodies := []string{
		// The dashed rule fires first and writes "[REDACTED:ssn]", which is the
		// only occurrence of the needle "ssn" anywhere. The labelled rule runs
		// after it and must still see the second, bare number.
		"123-45-6789 123456789",
		// "[REDACTED:bearer-token]" introduces both "bearer" and "token".
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345 and more",
		// "[REDACTED:sk-prefixed-key]" introduces "sk-".
		"key sk-abcdefghijklmnopqrstuvwxyz1234 trailing",
		// Everything at once.
		"a@b.co 123-45-6789 123456789 Bearer abcdefghijklmnopqrstuvwxyz012345 sk-abcdefghijklmnopqrstuvwxyz1234 4111111111111111",
	}
	rules := []Rule{{Name: "ticket", Type: RuleTypeRegex, Pattern: `CAVE-\d+`, Replacement: "[REDACTED:ssn] CAVE"}}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			got, _, err := Payload([]byte(body), rules)
			if err != nil {
				t.Fatalf("Payload: %v", err)
			}
			want := payloadNoPrescreen(t, []byte(body), rules)
			if !bytes.Equal(got, want) {
				t.Fatalf("prescreen changed the result:\n got %s\nwant %s", got, want)
			}
		})
	}

	// And specifically: the second, bare SSN must actually be gone.
	out, _ := mustPayload(t, []byte("123-45-6789 123456789"), nil)
	if bytes.Contains(out, []byte("123456789")) {
		t.Fatalf("a stale prescreen dropped the labelled-SSN match: %s", out)
	}
}

// TestPayloadNeedleIntroducersAreMarked pins the bookkeeping the optimisation
// depends on, so a future placeholder rename cannot silently unmark one.
func TestPayloadNeedleIntroducersAreMarked(t *testing.T) {
	for _, c := range builtinPayloadRules {
		want := introducesNeedle(c.repl)
		if c.replIntroducesNeedle != want {
			t.Fatalf("rule %q: replIntroducesNeedle=%v, want %v for repl %q",
				c.name, c.replIntroducesNeedle, want, c.repl)
		}
	}
	// An org rule whose replacement smuggles in a needle must be marked too.
	compiled, _, err := compileOrgRules([]Rule{{Name: "x", Type: RuleTypeRegex, Pattern: `zzz`, Replacement: "contact ops@x.io"}})
	if err != nil {
		t.Fatalf("compileOrgRules: %v", err)
	}
	if len(compiled) != 1 || !compiled[0].replIntroducesNeedle {
		t.Fatalf("org rule replacement containing '@' was not marked: %+v", compiled)
	}
}

// TestPayloadPrescreenPremisesHold pins the two structural premises that make
// replIntroducesNeedle sound. TestPayloadNeedleIntroducersAreMarked cannot do
// this: it compares the field against the same function that set it, so it only
// detects package-init ordering. These assertions fail on the change that would
// actually break the invariant — a new built-in with an unbracketed
// replacement, a needle containing a bracket, or org rules gaining needles.
func TestPayloadPrescreenPremisesHold(t *testing.T) {
	// Premise 1a: no needle contains a bracket, so a needle cannot straddle the
	// seam between kept text and a replacement.
	for _, n := range needleUniverse {
		if bytes.ContainsAny(n, "[]") {
			t.Fatalf("needle %q contains a bracket; it could straddle a replacement seam", n)
		}
	}
	// Premise 1b: every built-in replacement is bracketed, which is what makes
	// 1a sufficient — any needle overlapping a replacement must include its
	// first or last byte.
	for _, c := range builtinPayloadRules {
		if len(c.repl) < 2 || c.repl[0] != '[' || c.repl[len(c.repl)-1] != ']' {
			t.Fatalf("built-in %q has unbracketed replacement %q; a needle could straddle its seam", c.name, c.repl)
		}
	}
	// Premise 2: org rules declare no needles, so nothing downstream of them is
	// prescreen-gated and their arbitrary replacements cannot cause a skip.
	compiled, _, err := compileOrgRules([]Rule{
		{Name: "a", Type: RuleTypeRegex, Pattern: `AAA`, Replacement: "bearer sk- @ ssn"},
		{Name: "b", Type: RuleTypeRegex, Pattern: `BBB`},
	})
	if err != nil {
		t.Fatalf("compileOrgRules: %v", err)
	}
	if len(compiled) != 2 {
		t.Fatalf("compiled %d org rules, want 2", len(compiled))
	}
	for _, c := range compiled {
		if len(c.needles) != 0 {
			t.Fatalf("org rule %q declares needles %q; org replacements are unbracketed, so a needle could straddle their seam undetected", c.name, c.needles)
		}
	}
}

// --- Boundary-screen bypasses closed 2026-07-31 -----------------------------

// The key-assignment pattern required the keyword to be ADJACENT to the
// delimiter, which is not how credentials are named. In real config the
// keyword is a fragment of a longer name — SECRET_ACCESS_KEY, TOKEN_FOR_CI,
// clientSecretValue — and every one of these bodies passed through Payload
// completely untouched before the `[A-Za-z0-9_.\-]{0,24}` gap was allowed.
func TestPayloadRedactsWeldedKeyAssignments(t *testing.T) {
	secrets := map[string]string{
		"aws secret access key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"github token for ci":   "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"escaped json secret":   "je7MtGbClwBF/2Zp9Utk/h3yCo8nvbEXAMPLEKEY",
		"client secret value":   "abcdefghijklmnopqrstuvwxyz012345",
	}
	bodies := map[string]string{
		"aws secret access key": `AWS_SECRET_ACCESS_KEY=` + secrets["aws secret access key"],
		"github token for ci":   `GITHUB_TOKEN_FOR_CI=` + secrets["github token for ci"],
		// A body that arrived as an escaped JSON string literal: the delimiter
		// run is \": \" and the old class had no backslash in it.
		"escaped json secret": `{"log":"\"aws_secret_access_key\": \"` + secrets["escaped json secret"] + `\""}`,
		"client secret value": `{"clientSecretValue": "` + secrets["client secret value"] + `"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			out, report := mustPayload(t, []byte(body), nil)
			if bytes.Contains(out, []byte(secrets[name])) {
				t.Fatalf("secret survived: %s (findings %+v)", out, report.Findings)
			}
		})
	}
}

// A vendor-published prefix is direct evidence, so it gets its own rule rather
// than relying on the assignment shape or on entropy. None of these was
// redacted at all before: they carry no sk- prefix, no AKIA, no label.
func TestPayloadRedactsVendorPrefixedTokens(t *testing.T) {
	// Fixtures are split mid-token so GitHub push protection never sees a
	// contiguous secret-shaped literal; runtime values are unchanged.
	for name, token := range map[string]string{
		"slack bot":         "xoxb-23456" + "78901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx",
		"slack user":        "xoxp-23456" + "78901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx",
		"stripe live":       "sk_live_" + "4eC39HqLyjWDarjtT1zdp7dc",
		"stripe restricted": "rk_live_" + "4eC39HqLyjWDarjtT1zdp7dc",
		"github pat":        "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"github fine grain": "github_pat_11ABCDEFG0abcdefghijkl_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
		"google api key":    "AIzaSyD-1234567890abcdefghijklmnopqrstuv",
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte("pasted " + token + " into the prompt")
			out, report := mustPayload(t, body, nil)
			if bytes.Contains(out, []byte(token)) {
				t.Fatalf("token survived: %s (findings %+v)", out, report.Findings)
			}
			if !bytes.Contains(out, []byte("pasted ")) || !bytes.Contains(out, []byte(" into the prompt")) {
				t.Fatalf("surrounding context lost: %s", out)
			}
		})
	}
	// sk-proj- keys stay under the older, more specific finding name: the
	// prefix rule is ordered last precisely so it does not rename them.
	_, report := mustPayload(t, []byte("key sk-proj-abcdefghijklmnopqrstuvwxyz1234"), nil)
	if len(report.Findings) != 1 || report.Findings[0].Rule != "sk-prefixed-key" {
		t.Fatalf("sk-proj- key changed finding name: %+v", report.Findings)
	}
}

// The labelled-SSN gap used to be two `{0,12}` windows with an optional
// newline between them — 24 non-newline characters of reach, far enough to
// weld a label onto the NEXT field's number, and about a quarter of the whole
// pass's cost on an adversarial body. One `{0,16}` window replaced it.
func TestPayloadLabelledSSNGapWindow(t *testing.T) {
	// Inside the window, including across a newline.
	for _, body := range []string{
		"SSN: 123456789",
		"SSN\n123456789",
		"Tax ID (on file) 123456789",
		"SSN" + strings.Repeat(".", 16) + "123456789",
	} {
		out, _ := mustPayload(t, []byte(body), nil)
		if bytes.Contains(out, []byte("123456789")) {
			t.Fatalf("labelled id inside the window survived: %s", out)
		}
	}
	// Past the window the label is no longer evidence about this number.
	body := "SSN" + strings.Repeat(".", 17) + "123456789"
	out, _ := mustPayload(t, []byte(body), nil)
	if !bytes.Contains(out, []byte("123456789")) {
		t.Fatalf("number 17 characters past the label was still treated as labelled: %s", out)
	}
}

// A group index past a pattern's capture count indexes apply()'s loc slice out
// of range — the rule works until a body finally contains the secret it exists
// for, and then the process dies mid-capture. The build panics instead; this
// pins that the shipped set is consistent.
func TestPayloadBuiltinGroupsExistInTheirPatterns(t *testing.T) {
	for _, c := range builtinPayloadRules {
		if c.group > c.re.NumSubexp() {
			t.Fatalf("rule %q replaces group %d but its pattern has %d capture groups",
				c.name, c.group, c.re.NumSubexp())
		}
	}
}

// The same family on the redaction side: a numeric field named like a
// credential must survive, or every usage record in the corpus loses its
// counters.
func TestPayloadDoesNotRedactNumericFieldsNamedLikeCredentials(t *testing.T) {
	for _, body := range []string{
		`{"total_tokens": 1234567890123456}`,
		`{"token_ratio": 0.6870588235294117}`,
		`{"tokens_per_correct_task": 500.4347826086956}`,
	} {
		out, report := mustPayload(t, []byte(body), nil)
		if !bytes.Equal(out, []byte(body)) {
			t.Fatalf("over-redacted %q -> %s (findings %+v)", body, out, report.Findings)
		}
	}
}

// Allowing a name fragment between keyword and delimiter made every identifier
// reference whose NAME contains a keyword look like a credential assignment,
// and source code — which agents paste, read and emit constantly — is made of
// them. Over 22 MB of this repository's source the widening took key-assignment
// from 186 matches to 693 before the value test was tightened.
func TestPayloadDoesNotShredCodeIdentifiers(t *testing.T) {
	body := []byte("outputTokens := usage.OutputTokens\n" +
		"passwordResetter        passwordreset.Resetter\n" +
		"token := strings.TrimSpace(header)\n" +
		"apiKey: process.env.CAVE_API_KEY\n" +
		"authorization := strings.TrimSpace(raw)\n")
	out, report := mustPayload(t, body, nil)
	if !bytes.Equal(out, body) {
		t.Fatalf("shredded code identifiers:\n%s\nfindings %+v", out, report.Findings)
	}
}
