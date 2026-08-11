package redact

// Residual-risk screening — the second gate, for bytes that leave the tenant.
//
// Payload() is calibrated for a tenant-scoped, encrypted, TTL'd capture store:
// it keeps structure, and it deliberately declines to redact a bare 9-digit
// run because in JSON agent traffic that shape is far more often a timestamp,
// an order id or a row id than a government id, and shredding them all would
// empty the corpus of exactly the fields it exists for.
//
// That trade does not survive contact with a corpus that leaves the tenant
// boundary, and Payload's own comment says so. ResidualRisk is the inversion.
// It does not redact harder — redacting harder is what makes a corpus
// worthless. It FLAGS, so an export can DROP the record whole. The asymmetry
// is the argument: a dropped sample costs nothing (the published compression
// corpora are tens of thousands of chunks, a bar this traffic clears many
// times over), and an exported customer secret is unrecoverable.
//
// This is mechanism only, like the rest of the package. WHICH export drops on
// which flags is the caller's policy.
//
// The screen trusts NOTHING in the body it is handed. It used to strip
// `[REDACTED:…]`-shaped text before scanning, so that a successfully redacted
// record was not dropped for the evidence of its own redaction. But the body
// is customer bytes: anybody who can put text in a prompt can write that shape
// themselves, and every byte the screen removes on the strength of
// attacker-supplied punctuation is a byte the screen did not look at. The
// strip is gone. Payload's own placeholders are short, single-case and
// digit-free by construction, so they clear the screen on their merits — which
// is pinned by TestResidualRiskPlaceholdersClearTheScreenOnTheirMerits rather
// than assumed.

import (
	"bytes"
	"regexp"
	"sort"
	"unicode/utf8"
)

// Risk classes. Closed set: a caller may switch on these.
const (
	// RiskGovernmentIDShape is a digit run that could be a US SSN/ITIN and
	// carries no label, so Payload's labelled-only rule left it in place.
	RiskGovernmentIDShape = "government-id-shape"
	// RiskHighEntropyToken is a token that has the shape of secret material.
	// Three things reach it, in descending order of confidence:
	//
	//   1. a vendor-published credential prefix (ghp_, xoxb-, AIza, …), which
	//      is proof on its own and is never subjected to the heuristics;
	//   2. a value sitting in a credential-labelled assignment (SECRET=,
	//      GITHUB_TOKEN=, "api_key": "…"), where the label is the evidence,
	//      the same way Payload's bare-SSN rule treats an adjacent "SSN:";
	//   3. a long, high-diversity run that matched no credential pattern at
	//      all — the original meaning of the class, and still the bulk of it.
	RiskHighEntropyToken = "high-entropy-token"
)

// governmentIDShape matches a 9-digit run in the three printed groupings,
// bounded so it cannot be a slice of a longer number. Epoch-second (10) and
// epoch-millisecond (13) timestamps therefore do not match at all, which is
// what keeps this from flagging most of the corpus. validSSN then applies the
// SSA's own issuance rules, so structurally impossible values pass.
//
// Group 2 is the number; groups 1 and 3 are the boundaries, and the trailing
// one is CONSUMED by the match. That is why this is scanned by hand below
// instead of with FindAllSubmatch: FindAll resumes at the end of the whole
// match, so in a digit list every OTHER candidate lost the non-digit it needed
// on its left and was never tested. `{"ssns":[900112233,123456789,987654321]}`
// hid a valid SSN between two invalid ones for exactly that reason.
//
// The `^` and `$` are text anchors, not line anchors, and deliberately stay
// that way: `\n` is already a member of `[^0-9]`, so (?m) would add nothing
// here, and the sibling uuidShape below anchors a WHOLE token — (?m) there
// would let a multi-line value match on one of its lines.
var governmentIDShape = regexp.MustCompile(`(^|[^0-9])(\d{3}[ \-]?\d{2}[ \-]?\d{4})($|[^0-9])`)

// highEntropyToken matches long base64/base62-ish runs. The length floor and
// the diversity check below are both required: length alone flags file paths
// and prose, diversity alone flags short hex ids.
var highEntropyToken = regexp.MustCompile(`[A-Za-z0-9+/_=\-]{32,}`)

// minSecretTokenLen is the floor for treating a run — or one segment of a
// welded run — as a candidate secret.
const minSecretTokenLen = 32

// ResidualRisk reports which residual-risk classes a body still exhibits AFTER
// a Payload pass. An empty result means the body cleared the screen; it does
// NOT mean the body is free of sensitive data, which is not a property any
// pattern matcher can establish. It means nothing this screen knows how to
// look for is left. What it knows how to look for is enumerated in the package
// doc, along with the classes (every EU personal-data shape) that it does not.
//
// Results are sorted and de-duplicated so a manifest tally is reproducible.
func ResidualRisk(body []byte) []string {
	if len(body) == 0 {
		return nil
	}

	found := map[string]bool{}
	screenBytes(body, found)
	// Escaped-JSON evasion: a body that arrived as a JSON string literal can
	// spell a secret with \u00xx escapes, or split it with \/ and \", and every
	// pattern here misses it because the bytes on the wire are not the bytes
	// the model reads. Scanning the unescaped form as well costs one extra pass
	// and only on bodies that contain a backslash at all. It is a screen, so it
	// only has to FLAG the record — there is no unescaped body to write back,
	// and Payload deliberately does not attempt one.
	if unescaped := jsonUnescape(body); unescaped != nil {
		screenBytes(unescaped, found)
	}

	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// screenBytes accumulates the classes scan exhibits into found. It skips any
// class already flagged, so the second (unescaped) pass costs nothing once the
// first has decided.
func screenBytes(scan []byte, found map[string]bool) {
	if !found[RiskGovernmentIDShape] {
		// Resume at the END OF THE CAPTURE, not the end of the match, so the
		// non-digit the match consumed on its right is still available as the
		// next candidate's left boundary.
		for pos := 0; pos < len(scan); {
			loc := governmentIDShape.FindSubmatchIndex(scan[pos:])
			if loc == nil {
				break
			}
			lo, hi := pos+loc[4], pos+loc[5]
			if validSSN(scan[lo:hi]) {
				found[RiskGovernmentIDShape] = true
				break
			}
			pos = hi
		}
	}
	if found[RiskHighEntropyToken] {
		return
	}
	// The credential floor runs before the heuristics and overrides them. A
	// vendor prefix and a credential label are both direct evidence; the
	// entropy tests are a guess, and they demonstrably miss both shapes (a
	// Slack xoxb- token is mostly digits and hyphens; an AWS secret key is
	// nearly digit-free).
	if credentialPrefixRe.Match(scan) || awsSecretAccessKeyRe.Match(scan) {
		found[RiskHighEntropyToken] = true
		return
	}
	for _, m := range keyAssignmentRe.FindAll(scan, -1) {
		if credentialAssignmentValueIsSecretShaped(m) {
			found[RiskHighEntropyToken] = true
			return
		}
	}
	for _, m := range highEntropyToken.FindAll(scan, -1) {
		if candidateLooksLikeSecret(m) {
			found[RiskHighEntropyToken] = true
			return
		}
	}
}

// isTokenSeparator reports whether c is a boundary between a name and a value,
// or between path elements, inside a candidate run.
//
// `-` is deliberately NOT a separator: it is interior to xox[baprs]- and
// sk-proj- tokens, to UUIDs, and to base64url payloads, and splitting on it
// would take those apart.
func isTokenSeparator(c byte) bool {
	switch c {
	case '_', '/', '=', '"', '\'', ' ', '\\':
		return true
	}
	return false
}

// candidateLooksLikeSecret vets a candidate run for the shape of secret
// material by taking it apart on separator boundaries and scoring each
// segment.
//
// Splitting is the fix for the bypass that let every labelled credential
// through in its natural form. highEntropyToken's alphabet includes `_`, `/`
// and `=`, so `AWS_SECRET_ACCESS_KEY=wJalrX…` and
// `/etc/secrets/ghp_16C7…/token` each arrive as ONE run whose statistics are
// the label's and the path's, not the secret's. The old rule then exempted any
// run with more than two separators outright — on the theory that separators
// mean "identifier" — so the more thoroughly a secret was labelled, the more
// certainly it was skipped. That exemption is gone.
//
// Splitting ALONE is not enough, and scoring the welded run alone is not
// either. The two failures are opposite:
//
//   - Score only the welded run and a labelled credential hides inside the
//     label's statistics. That is the bypass above.
//   - Score only the segments and a secret with ONE interior separator
//     disappears: both halves fall under the 32-char floor and neither is ever
//     scored. Measured over 5,000 random secrets per shape, segments-only took
//     43-char base64url detection from 78.8% to 66.0% and 32-char from 84.9%
//     to 55.2%, with a dead zone wherever the separator lands between
//     positions 12 and 37. That is the unlabelled-secret class this heuristic
//     is the only defence for — the credential floor does not backstop it,
//     because an unlabelled secret has neither a label nor a vendor prefix.
//
// So both are scored, with the welded run gated on carrying at most two
// separators. Two is where a run stops looking like one token that happens to
// contain a separator and starts looking like a composition of names: a path,
// a snake_case identifier, an assignment. Above the gate the run is only taken
// apart, which is what keeps `AWS_SECRET_ACCESS_KEY=wJalrX…` from being scored
// as a single high-diversity blob.
//
// Measured over 4,087 post-Payload chunks (22.3 MB) of this repository's own
// source: the screen's drop rate is 3.33% before this lane and 5.99% after.
// The <=2 gate itself costs +0.43pp of that (entropy class 5.17% -> 5.60%
// against segments-only) and closes every single-separator bypass shape; the
// rest is the deleted exemption, which is the point of the lane. The dated
// slugs that drove an earlier UNCONDITIONAL variant to 7.42% are NOT this
// gate's doing — they flag through their own segment either way, because `-`
// is not a separator.
//
// Detection over 5,000 random secrets per shape, pre-lane / segments-only /
// as built: base64url-43 77.9% / 66.4% / 89.2%, base62-32 94.2% / 94.2% /
// 94.2%, hex-64 100% / 100% / 100%, base64std-88 82.0% / 94.7% / 95.8%.
func candidateLooksLikeSecret(tok []byte) bool {
	if credentialPrefixRe.Match(tok) {
		return true
	}
	seps := 0
	for _, c := range tok {
		if isTokenSeparator(c) {
			seps++
		}
	}
	if seps <= 2 && looksLikeSecret(tok) {
		return true
	}
	if seps == 0 {
		return false
	}
	// Segment offsets are walked rather than materialised. A customer-supplied
	// 4 MiB separator-rich run turns bytes.FieldsFunc into a 108 MiB
	// allocation — 27x the body — on a worker goroutine, which is a
	// denial-of-service surface handed straight to the party being screened.
	start := -1
	for i := 0; i <= len(tok); i++ {
		if i < len(tok) && !isTokenSeparator(tok[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if looksLikeSecret(tok[start:i]) {
				return true
			}
			start = -1
		}
	}
	return false
}

// uuidShape matches a canonical UUID. UUIDs are everywhere in agent traffic —
// trace ids, tool-call ids, row keys — and they are long, digit-rich and
// high-diversity, so without this exclusion the screen would drop a large
// fraction of every corpus for no privacy gain.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// looksLikeSecret vets a long token for the shape of a random secret.
//
// The first cut required lower AND upper AND digits at >=20% each, which
// silently missed the most common secret shapes in the wild: a 64-char hex
// digest, a lowercase base32 token, and an all-caps key are all SINGLE-CASE
// and every one of them sailed through. The test now leads on character
// DIVERSITY plus digit density, which those three share and ordinary prose,
// identifiers and paths do not.
//
// What it knowingly does NOT catch, stated so nobody assumes otherwise:
//
//   - a long random token drawn from letters ONLY, with no digits and no case
//     mixing (e.g. a 40-char lowercase-alpha passphrase). Separating that from
//     a long identifier or a run of words without spaces needs a language
//     model, not a regex, and the false-positive cost of guessing is paid in
//     dropped corpus on every long identifier in the fleet.
//   - a token whose case mix is lopsided and whose digits are sparse — the
//     canonical AWS example secret key wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
//     is 67% uppercase and carries one digit, and clears every test here. The
//     credential floor in screenBytes catches it by its LABEL, not its shape;
//     an unlabelled one is a genuine gap.
//   - anything shorter than 32 characters, unless it carries a vendor prefix.
//   - a secret split across fields or lines.
//
// The screen is necessary, not sufficient. It sits on top of the built-in
// credential floor (sk-, AKIA, PEM blocks, DSNs, labelled assignments), which
// catches the prefixed and labelled shapes this deliberately does not chase.
func looksLikeSecret(tok []byte) bool {
	if len(tok) < minSecretTokenLen {
		return false
	}
	if uuidShape.Match(tok) {
		return false
	}

	var seen [256]bool
	var distinct, lower, upper, digit int
	for _, c := range tok {
		if !seen[c] {
			seen[c] = true
			distinct++
		}
		switch {
		case c >= 'a' && c <= 'z':
			lower++
		case c >= 'A' && c <= 'Z':
			upper++
		case c >= '0' && c <= '9':
			digit++
		}
	}
	// A repeated-character constant, a run of one word, or a short alphabet
	// (say, a long binary or decimal string) is not a secret. Hex has 16
	// symbols and a real digest uses nearly all of them, so 12 admits hex
	// while excluding the degenerate cases.
	if distinct < 12 {
		return false
	}
	// Digit density is what separates a digest or key from English words run
	// together and from CamelCase identifiers. Hex digests sit near 60%,
	// base62 keys near 15-20%, prose near 0%.
	if float64(digit)/float64(len(tok)) >= 0.15 {
		return true
	}
	// Digitless fallback: a strong case mix. The bar is a third each, not a
	// fifth, because CamelCase identifiers carry roughly one capital per word
	// (~15-20%) and must not trip it.
	return upper*3 >= len(tok) && lower*3 >= len(tok)
}

// jsonUnescape returns b with JSON string escapes resolved, or nil when b
// carries no backslash and the second scan would therefore be redundant.
//
// It is deliberately permissive: it is decoding an arbitrary fragment, not
// parsing a document, so an escape it does not recognise is emitted verbatim
// rather than treated as an error. Its only consumer is a screen that flags,
// so a wrong decode costs at most one extra scan of nonsense — never a wrong
// output body.
func jsonUnescape(b []byte) []byte {
	start := bytes.IndexByte(b, '\\')
	if start < 0 {
		return nil
	}
	out := make([]byte, 0, len(b))
	out = append(out, b[:start]...)
	for i := start; i < len(b); {
		if b[i] != '\\' || i+1 >= len(b) {
			out = append(out, b[i])
			i++
			continue
		}
		switch e := b[i+1]; e {
		case '"', '\\', '/', '\'':
			out = append(out, e)
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'b':
			out = append(out, '\b')
			i += 2
		case 'f':
			out = append(out, '\f')
			i += 2
		case 'u':
			r, ok := hex4(b, i+2)
			if !ok {
				out = append(out, b[i])
				i++
				continue
			}
			out = utf8.AppendRune(out, r)
			i += 6
		default:
			out = append(out, b[i])
			i++
		}
	}
	return out
}

// hex4 decodes the four hex digits at b[at:at+4].
func hex4(b []byte, at int) (rune, bool) {
	if at+4 > len(b) {
		return 0, false
	}
	var v rune
	for _, c := range b[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}
