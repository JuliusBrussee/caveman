package redact

import (
	"crypto/rand"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// The bare 9-digit rule in Payload is LABEL-ONLY on purpose: in JSON agent
// traffic an unlabelled 9-digit run is far more often a timestamp, an order id
// or a row id than a government id, and shredding all of them would empty the
// corpus of the exact fields it exists for. That trade was made for a
// tenant-scoped, encrypted, TTL'd capture store.
//
// It does not survive contact with a corpus that leaves the tenant boundary.
// ResidualRisk is where the trade inverts: it does not try to redact harder,
// it FLAGS the record so the export drops it. Dropping a sample costs nothing;
// exporting a customer's SSN is unrecoverable.
func TestResidualRiskFlagsUnlabelledGovernmentIDShapes(t *testing.T) {
	flagged := []string{
		`{"note":"customer id 123456789 needs review"}`,
		`{"rows":[{"v":"078051120"}]}`,
		"a bare 123 45 6789 with spaces",
		"hyphenated 123-45-6789 that Payload would have replaced only if it ran",
	}
	for _, body := range flagged {
		got := ResidualRisk([]byte(body))
		if !slices.Contains(got, RiskGovernmentIDShape) {
			t.Fatalf("ResidualRisk(%q) = %v, want it to contain %q", body, got, RiskGovernmentIDShape)
		}
	}
}

// The flag must not fire on shapes that are structurally impossible as a US
// government id, or on digit runs that are plainly something else. Otherwise
// the screen drops the whole corpus and the honest answer degenerates to "we
// export nothing", which is a different kind of failure.
func TestResidualRiskDoesNotFlagImpossibleOrLongerDigitRuns(t *testing.T) {
	clean := []string{
		`{"ts":1785412800123}`,               // 13-digit epoch ms
		`{"ts":1785412800}`,                  // 10-digit epoch seconds
		`{"id":"000123456"}`,                 // area 000 is never issued
		`{"id":"666123456"}`,                 // area 666 is never issued
		`{"id":"900123456"}`,                 // 9xx area is never issued
		`{"id":"123006789"}`,                 // group 00 is never issued
		`{"id":"123450000"}`,                 // serial 0000 is never issued
		`{"id":"1234567890123456"}`,          // longer run, not a 9-digit token
		`{"uuid":"3f2504e0-4f89-11d3-9a0c"}`, // hex, not a digit run
		`{"empty":""}`,
	}
	for _, body := range clean {
		if got := ResidualRisk([]byte(body)); slices.Contains(got, RiskGovernmentIDShape) {
			t.Fatalf("ResidualRisk(%q) = %v, want no %q", body, got, RiskGovernmentIDShape)
		}
	}
}

// A long, high-entropy token that matches no known credential prefix is the
// other residual risk: the built-in floor catches sk-, AKIA, PEM blocks and
// labelled assignments, but a raw random secret pasted with no label matches
// nothing and would sail into the corpus intact.
//
// SINGLE-CASE shapes are the ones the first cut of this rule missed. It
// required lower AND upper AND digits at >=20% each, so a hex digest, a
// lowercase base32 token and an all-caps key — the three commonest secret
// shapes in the wild — all passed straight through.
func TestResidualRiskFlagsHighEntropyTokens(t *testing.T) {
	for name, body := range map[string]string{
		"mixed base62":       `{"value":"9f8Ka2Lm4Qp7Zx1Rt6Bv3Nc5Yd8Wq0Es2Hj4Uk7Gi1Ao"}`,
		"64-char hex digest": `{"value":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}`,
		"lowercase base32":   `{"value":"mfrggzdfmztwq2lknnwg23tpobyxg5dfoj4ha3ddmnqxi5df7a2k9x"}`,
		"all-caps key":       `{"value":"AKQ4XZ7PLM2WD9TRB6NHVC3JSY8FGK5QZW1MPX0TRDLA2NBV7CY"}`,
	} {
		if got := ResidualRisk([]byte(body)); !slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("%s: ResidualRisk = %v, want it to contain %q", name, got, RiskHighEntropyToken)
		}
	}
}

// UUIDs are everywhere in agent traffic — trace ids, tool-call ids, row keys —
// and they are long, digit-rich and high-diversity. Flagging them would drop a
// large fraction of every corpus for no privacy gain, so they are excluded by
// shape.
func TestResidualRiskDoesNotFlagUUIDs(t *testing.T) {
	for _, body := range []string{
		`{"trace_id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301"}`,
		`{"id":"01927F3A-4B5C-7D8E-9F01-A2B3C4D5E6F7"}`,
	} {
		if got := ResidualRisk([]byte(body)); slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("ResidualRisk(%q) = %v, want no %q", body, got, RiskHighEntropyToken)
		}
	}
}

// Ordinary prose, code, and structured agent traffic must not trip the
// entropy flag — long identifiers made of one case class or with low symbol
// diversity are not secrets.
func TestResidualRiskDoesNotFlagOrdinaryLongStrings(t *testing.T) {
	clean := []string{
		`{"path":"cloud/worker/internal/corpus/builder_test_helpers_for_things.go"}`,
		`{"text":"` + strings.Repeat("the quick brown fox ", 8) + `"}`,
		`{"const":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`,
		`{"snake":"a_very_long_but_entirely_ordinary_identifier_name_here"}`,
		// CamelCase carries roughly one capital per word (~15-20%), well under
		// the digitless fallback's one-third bar.
		`{"type":"SomeVeryLongTypeNameHereForThingsAndOtherThings"}`,
		// A long single-alphabet run: high length, low symbol diversity.
		`{"bits":"0101010101010101010101010101010101010101010101"}`,
	}
	for _, body := range clean {
		if got := ResidualRisk([]byte(body)); slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("ResidualRisk(%q) = %v, want no %q", body, got, RiskHighEntropyToken)
		}
	}
}

// A body that has already been through Payload must not be flagged for the
// placeholders Payload itself wrote — otherwise every successfully redacted
// record would be dropped.
func TestResidualRiskIgnoresItsOwnPlaceholders(t *testing.T) {
	body, _, err := Payload([]byte(`{"ssn":"123-45-6789","email":"a@b.com","auth":"Bearer abcdefghijklmnopqrstuvwx"}`), nil)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if got := ResidualRisk(body); len(got) != 0 {
		t.Fatalf("ResidualRisk(redacted body) = %v, want none; body was %s", got, body)
	}
}

// Findings are deterministic and de-duplicated: the same body always produces
// the same flags in the same order, so a manifest tally is reproducible.
func TestResidualRiskIsDeterministicAndDeduplicated(t *testing.T) {
	body := []byte(`{"a":"123456789","b":"234567891","c":"9f8Ka2Lm4Qp7Zx1Rt6Bv3Nc5Yd8Wq0Es2Hj4Uk7Gi1Ao"}`)
	first := ResidualRisk(body)
	second := ResidualRisk(body)
	if !slices.Equal(first, second) {
		t.Fatalf("ResidualRisk not deterministic: %v vs %v", first, second)
	}
	seen := map[string]bool{}
	for _, f := range first {
		if seen[f] {
			t.Fatalf("ResidualRisk returned duplicate flag %q in %v", f, first)
		}
		seen[f] = true
	}
}

// The corpus builder's package doc gates enabling CAVE_CORPUS_BUILD_ENABLED on
// measuring the drop rate first, and it NAMES the shapes that drive it. Those
// names are a factual claim about this rule, and a claim in a comment rots the
// moment the rule is tuned — which is exactly what happened once already, when
// widening looksLikeSecret to catch single-case secrets made the entropy rule
// the larger driver while the gate text still described only the SSN rule.
//
// This pins the list. If a future change to looksLikeSecret moves any of these
// shapes, this fails and whoever made the change updates the gate text in the
// same commit, rather than leaving a gate that under-describes the risk it
// exists to gate.
func TestResidualRiskFlagsShapesTheCorpusGateNames(t *testing.T) {
	flagged := map[string]string{
		"md5 digest (32 hex)":    "5d41402abc4b2a76b9719d911017c592",
		"git SHA (40 hex)":       "e83c5163316f89bfbde7d9ab23ca2e25604af290",
		"sha256 digest (64 hex)": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"JWT payload segment":    "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ",
		"JWT signature segment":  "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
	}
	for name, token := range flagged {
		if !slices.Contains(ResidualRisk([]byte(`{"v":"`+token+`"}`)), RiskHighEntropyToken) {
			t.Fatalf("%s no longer flags; the corpus gate text names it as a drop driver", name)
		}
	}

	// The calibration half of the same claim: these must NOT flag, or the gate
	// text's "not flagged, for calibration" list is wrong too.
	clean := map[string]string{
		"UUID":            "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		"digit-only run":  "4242424242424242424242424242424242",
		"mostly-zero b64": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg",
	}
	for name, token := range clean {
		if slices.Contains(ResidualRisk([]byte(`{"v":"`+token+`"}`)), RiskHighEntropyToken) {
			t.Fatalf("%s now flags; the corpus gate text lists it as NOT flagged", name)
		}
	}
}

// base64 of random or compressed bytes is named in the gate text as a drop
// driver. It is probabilistic — the digit-density test is on the encoded
// output — so this asserts the population behaviour the gate text describes
// rather than one lucky string.
func TestResidualRiskFlagsMostRandomBase64(t *testing.T) {
	const trials = 200
	var flagged int
	for i := 0; i < trials; i++ {
		buf := make([]byte, 64)
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(ResidualRisk([]byte(`{"v":"`+base64.StdEncoding.EncodeToString(buf)+`"}`)), RiskHighEntropyToken) {
			flagged++
		}
	}
	// Measured at ~81% over 200 trials when this was written. The bar is set
	// well below that: the gate text's claim is "most", and this test exists to
	// catch the rule silently ceasing to flag base64 at all, not to pin a rate.
	if flagged < trials/2 {
		t.Fatalf("random base64 flagged %d/%d; the corpus gate text names it as a drop driver", flagged, trials)
	}
}

// --- Boundary-screen bypasses closed 2026-07-31 -----------------------------

// Credentials do not arrive naked. They arrive assigned to a name, quoted
// inside JSON, or sitting in a path — and highEntropyToken's alphabet includes
// `_`, `/` and `=`, so all of that WELDS into one candidate run whose
// statistics are the label's, not the secret's. The old rule then exempted any
// run with more than two separators outright, on the theory that separators
// mean "identifier", so the more thoroughly a secret was labelled the more
// certainly the screen skipped it.
//
// Every string below was verified to pass the screen CLEAN before this fix.
func TestResidualRiskFlagsCredentialsInTheirNaturalForm(t *testing.T) {
	for name, body := range map[string]string{
		// Four separators before the token; the exemption skipped it outright.
		"welded env assignment": `GITHUB_TOKEN_FOR_CI=ghp_16C7e42F292c6912E7710c838347Ae178B4a`,
		// Path separators do the same job as underscores.
		"path-embedded token": `/etc/secrets/ghp_16C7e42F292c6912E7710c838347Ae178B4a/token`,
		// Three underscores, and the value's own statistics (67% uppercase,
		// one digit) clear every entropy test on their own.
		"aws secret access key": `AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
		// The bare token: no separators, but 40 chars of mostly-lowercase
		// base62 that the diversity/digit-density tests are not certain to
		// catch either. The vendor prefix decides it.
		"bare vendor token": `ghp_16C7e42F292c6912E7710c838347Ae178B4a`,
	} {
		if got := ResidualRisk([]byte(body)); !slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("%s: ResidualRisk(%q) = %v, want it to contain %q", name, body, got, RiskHighEntropyToken)
		}
	}
}

// A vendor-published prefix is proof of what the token is, so it overrides the
// entropy heuristics rather than being scored by them. Slack's xoxb- tokens in
// particular are mostly digits and hyphens and were skipped by the
// separator-count exemption; Stripe's sk_live_ keys are short.
func TestResidualRiskFlagsKnownCredentialPrefixes(t *testing.T) {
	// Fixtures are split mid-token so GitHub push protection never sees a
	// contiguous secret-shaped literal; runtime values are unchanged.
	for name, body := range map[string]string{
		"slack bot token":   `xoxb-23456` + `78901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx`,
		"slack user token":  `xoxp-23456` + `78901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx`,
		"stripe live key":   `sk_live_` + `4eC39HqLyjWDarjtT1zdp7dc`,
		"stripe restricted": `rk_live_` + `4eC39HqLyjWDarjtT1zdp7dc`,
		"github pat":        `ghp_16C7e42F292c6912E7710c838347Ae178B4a`,
		"github fine grain": `github_pat_11ABCDEFG0abcdefghijkl_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij`,
		"google api key":    `AIzaSyD-1234567890abcdefghijklmnopqrstuv`,
		"openai project":    `sk-proj-abcdefghijklmnopqrstuvwxyz1234`,
	} {
		if got := ResidualRisk([]byte(body)); !slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("%s: ResidualRisk(%q) = %v, want it to contain %q", name, body, got, RiskHighEntropyToken)
		}
	}
}

// governmentIDShape CONSUMES the non-digit on the number's right, and
// FindAllSubmatch resumes at the end of the whole match — so in a list of
// numbers every OTHER candidate lost the boundary it needed on its left and
// was never tested at all.
//
// The probe is the worst case that shape produces: two structurally impossible
// SSNs (both 9xx areas, which validSSN rejects) bracketing a real one. The
// first match consumed the comma the second needed, the second was skipped,
// the third was rejected, and the body screened clean.
func TestResidualRiskFlagsEveryEntryInADigitList(t *testing.T) {
	for _, body := range []string{
		`{"ssns":[900112233,123456789,987654321]}`,
		// The same shape one position over, so a fix that merely shifts the
		// off-by-one does not pass.
		`{"ssns":[123456789,900112233,987654321]}`,
		`{"ssns":[900112233,987654321,123456789]}`,
		// Punctuated forms in a list, same mechanism.
		`900-11-2233 123-45-6789 987-65-4321`,
	} {
		if got := ResidualRisk([]byte(body)); !slices.Contains(got, RiskGovernmentIDShape) {
			t.Fatalf("ResidualRisk(%q) = %v, want it to contain %q", body, got, RiskGovernmentIDShape)
		}
	}
	// The calibration half: a list of only-impossible numbers still passes, or
	// the fix has simply stopped applying validSSN.
	clean := `{"ssns":[900112233,987654321,666123456]}`
	if got := ResidualRisk([]byte(clean)); slices.Contains(got, RiskGovernmentIDShape) {
		t.Fatalf("ResidualRisk(%q) = %v, want no %q", clean, got, RiskGovernmentIDShape)
	}
}

// The screen used to strip `[REDACTED:…]`-shaped text before scanning. The
// body is customer bytes: anyone who can put text in a prompt can write that
// shape, and every stripped byte is a byte the screen did not look at. Here
// the literal splits a credential's value into two sub-32 halves and the whole
// record screened clean.
func TestResidualRiskDoesNotTrustEmbeddedPlaceholderText(t *testing.T) {
	body := `aws_secret_access_key=je7MtGbClwBF2Zp9Ut[REDACTED:email]kh3yCo8nvbEXAMPLEKEY`
	if got := ResidualRisk([]byte(body)); !slices.Contains(got, RiskHighEntropyToken) {
		t.Fatalf("ResidualRisk(%q) = %v, want it to contain %q", body, got, RiskHighEntropyToken)
	}
}

// Removing the strip is only safe because Payload's own placeholders clear the
// screen on their merits: they are short, single-case, digit-free and carry no
// 9-digit run. That is a property of the placeholder NAMES, so it is checked
// against the live rule set rather than assumed — a future rule named with a
// digest or a long random suffix would drop every record it ever fired on.
func TestResidualRiskPlaceholdersClearTheScreenOnTheirMerits(t *testing.T) {
	for _, c := range builtinPayloadRules {
		if got := ResidualRisk(c.repl); len(got) != 0 {
			t.Fatalf("placeholder %s would itself be flagged: %v", c.repl, got)
		}
	}
	body, _, err := Payload([]byte(`{"ssn":"123-45-6789","email":"a@b.com","card":"4111111111111111",`+
		`"auth":"Bearer abcdefghijklmnopqrstuvwx","key":"sk-abcdefghijklmnopqrstuvwxyz1234"}`), nil)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if got := ResidualRisk(body); len(got) != 0 {
		t.Fatalf("ResidualRisk(redacted body) = %v, want none; body was %s", got, body)
	}
}

// A body that arrived as a JSON string literal can spell its secrets in \u
// escapes: the bytes on the wire are not the bytes the model reads, and every
// pattern in the package matches the wire bytes. The screen therefore scans
// the unescaped form as well. It only FLAGS — there is no unescaped body to
// write back, and Payload deliberately does not attempt one, which is why this
// is a screen-side fix and not a redaction-side one.
func TestResidualRiskSeesThroughJSONEscapes(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		// Spells "123456789". On the wire it is four-digit groups split by
		// backslashes, so no 9-digit run exists for the screen to find.
		"escaped ssn digits": {
			body: `{"id":"\u0031\u0032\u0033\u0034\u0035\u0036\u0037\u0038\u0039"}`,
			want: RiskGovernmentIDShape,
		},
		// One escaped character is enough to hide a vendor prefix, and what is
		// left on the wire is too digit-poor for the entropy tests.
		"escaped credential prefix": {
			body: `{"t":"\u0073k_live_abcdefghijklmnopqrstuvwx"}`,
			want: RiskHighEntropyToken,
		},
	}
	for name, tc := range cases {
		if got := ResidualRisk([]byte(tc.body)); !slices.Contains(got, tc.want) {
			t.Fatalf("%s: ResidualRisk(%q) = %v, want it to contain %q", name, tc.body, got, tc.want)
		}
		// The whole point is that the WIRE bytes are clean. If a fixture starts
		// flagging without being unescaped it has stopped testing this path.
		onWire := map[string]bool{}
		screenBytes([]byte(tc.body), onWire)
		if onWire[tc.want] {
			t.Fatalf("%s: fixture flags on the wire bytes; it no longer tests the escape path", name)
		}
	}
	// A body with no backslash must not pay for the second pass at all.
	if jsonUnescape([]byte(`{"a":"b"}`)) != nil {
		t.Fatal("jsonUnescape allocated a second body for an unescaped input")
	}
}

// The old rule exempted any candidate run carrying more than two `/`, `_` or
// `-` characters, on the theory that separators mean "identifier". A secret
// three directories deep in a path was therefore MORE certainly skipped than
// one pasted bare. Splitting replaced the exemption: the path elements score as
// themselves and the digest scores as itself.
func TestResidualRiskSplitsRatherThanExemptingSeparatedRuns(t *testing.T) {
	body := `{"cfg":"a/b/c/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}`
	if got := ResidualRisk([]byte(body)); !slices.Contains(got, RiskHighEntropyToken) {
		t.Fatalf("ResidualRisk(%q) = %v, want it to contain %q", body, got, RiskHighEntropyToken)
	}
}

// Widening the keyword-to-delimiter gap made every numeric field whose NAME
// contains a keyword look like a credential assignment, and LLM traffic is
// made of those. A credential is never a bare decimal.
func TestResidualRiskDoesNotFlagNumericFieldsNamedLikeCredentials(t *testing.T) {
	for _, body := range []string{
		`{"total_tokens": 1234567890123456}`,
		`{"token_ratio": 0.6870588235294117}`,
		`{"tokens_per_correct_task": 500.4347826086956}`,
		`{"cached_token_count": 9007199254740991}`,
	} {
		if got := ResidualRisk([]byte(body)); slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("ResidualRisk(%q) = %v, want no %q", body, got, RiskHighEntropyToken)
		}
	}
}

// Splitting a welded run closed the labelled-credential bypass and opened its
// mirror image: a 32-63 char secret carrying ONE interior separator has both
// halves fall under the 32-char floor, so neither half is ever scored and the
// whole token disappears. Measured over 5,000 random secrets per shape,
// segments-only took 43-char base64url detection from 78.8% to 66.0% and
// 32-char from 84.9% to 55.2%, with a total dead zone wherever the separator
// landed between positions 12 and 37.
//
// This is the unlabelled-secret class the heuristic is the only defence for:
// no label, no vendor prefix, nothing for the credential floor to catch. The
// welded run is therefore scored too, gated on carrying at most two separators.
func TestResidualRiskFlagsSecretsWithOneInteriorSeparator(t *testing.T) {
	// 43 chars of base64url with a single '_' at each dead-zone position.
	const secret = "aG9sZG91dEV4YW1wbGVUb2tlbjkxMjM0NTY3ODkwYWI"
	if len(secret) != 43 {
		t.Fatalf("fixture is %d chars, want 43", len(secret))
	}
	for _, pos := range []int{12, 21, 30, 37} {
		b := []byte(`{"v":"` + secret + `"}`)
		b[6+pos] = '_'
		if got := ResidualRisk(b); !slices.Contains(got, RiskHighEntropyToken) {
			t.Fatalf("separator at %d: ResidualRisk(%s) = %v, want it to contain %q",
				pos, b, got, RiskHighEntropyToken)
		}
	}
	// Two separators is still inside the gate; three tips the run over into
	// "composition of names" and only the segments are scored.
	twoSeps := []byte(`{"v":"` + secret + `"}`)
	twoSeps[6+12] = '_'
	twoSeps[6+30] = '/'
	if got := ResidualRisk(twoSeps); !slices.Contains(got, RiskHighEntropyToken) {
		t.Fatalf("two separators: ResidualRisk(%s) = %v, want it to contain %q",
			twoSeps, got, RiskHighEntropyToken)
	}
}
