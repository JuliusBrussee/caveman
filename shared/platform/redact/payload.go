package redact

// Payload redaction — the rule engine that scrubs captured request/response
// bodies before they are ever written to storage.
//
// This file extends the package past its original log-only scope. The two
// surfaces share the same secret patterns on purpose (one place to fix a
// pattern), but they are NOT the same operation:
//
//   - String() scrubs a short log line and replaces whole matches.
//   - Payload() scrubs a whole customer prompt or completion that will later be
//     replayed and graded. It replaces only the sensitive SPAN and keeps the
//     structure around it, because a corpus of blanked lines is worthless.
//
// This is mechanism only. WHICH organization has which rules, and whether a
// capture is permitted at all, is cloud policy and lives in the caller.
//
// Contract, in order of importance:
//
//  1. Fail closed. Every error path returns a nil body. A caller that stores
//     what Payload returned without checking err stores nothing.
//  2. Built-ins are a floor. They are applied first and unconditionally; no
//     org-supplied rule can remove, disable, or shadow one.
//  3. Deterministic. No clock, no randomness, no map iteration reaches the
//     output. Org rules are sorted into a canonical order before use, so the
//     caller's slice order cannot change the bytes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MaxPayloadBytes bounds the work a single redaction pass may do. Go's regexp
// is RE2, so there is no catastrophic-backtracking cliff to fall off — cost is
// linear in the body — but linear cost on an unbounded body is still a
// denial-of-service surface, so the body size is capped outright. Bodies above
// the cap are an error (no capture), never a silent skip.
const MaxPayloadBytes = 8 << 20

// Rule types mirror the CHECK constraint on the redaction_rules table
// (regex, json_path, header, builtin). Payload implements the two that are
// meaningful against an opaque body; the other two fail the pass closed rather
// than being silently ignored, because ignoring a rule an operator configured
// is exactly how unredacted data leaks.
const (
	RuleTypeRegex    = "regex"
	RuleTypeJSONPath = "json_path"
	RuleTypeHeader   = "header"
	RuleTypeBuiltin  = "builtin"
)

// Origin values distinguish the built-in floor from operator-supplied rules in
// the report.
const (
	OriginBuiltin = "builtin"
	OriginOrg     = "org"
)

var (
	// ErrBodyTooLarge means the body exceeded MaxPayloadBytes.
	ErrBodyTooLarge = errors.New("redact: payload exceeds redaction size limit")
	// ErrInvalidRule means a rule could not be compiled or was structurally
	// unusable (empty name/pattern, or a pattern that matches the empty
	// string and would therefore rewrite the entire body).
	ErrInvalidRule = errors.New("redact: invalid redaction rule")
	// ErrRuleUnsupported means the rule's type is not implementable against an
	// opaque body by this pass.
	ErrRuleUnsupported = errors.New("redact: unsupported redaction rule type")
)

// Rule is one enabled row of the redaction_rules table, minus the columns that
// are the caller's business. Field names and JSON tags are pinned to that
// table's columns so the two cannot drift.
//
// Deliberately absent:
//   - id / organization_id / project_id — tenancy is the caller's concern.
//   - enabled — filtering disabled rows is the caller's concern; a rule that
//     reaches Payload is a rule that runs.
//
// A row with rule_type='builtin' is accepted and treated as a no-op reference:
// the built-in floor is unconditional, so such a row can neither add to nor
// subtract from it.
type Rule struct {
	Name        string `json:"name"`
	Type        string `json:"rule_type"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

// Finding is the per-rule tally in a RedactionReport. It records that a rule
// fired and how often — never what it matched.
type Finding struct {
	Rule   string `json:"rule"`
	Origin string `json:"origin"`
	Count  int    `json:"count"`
}

// RedactionReport is the evidence stored beside a capture: which classes of
// sensitive material were found, how many of each, and which rule set was in
// force. It contains no matched values — a report that echoes the secret it
// redacted is worse than no report at all.
type RedactionReport struct {
	// RuleSetHash identifies the exact rule set (built-ins + org rules) that
	// produced this result, so a corpus consumer can tell captures made under
	// different redaction regimes apart.
	RuleSetHash  string    `json:"rule_set_hash"`
	Findings     []Finding `json:"findings,omitempty"`
	TotalMatches int       `json:"total_matches"`
	BytesIn      int       `json:"bytes_in"`
	BytesOut     int       `json:"bytes_out"`
}

// compiledRule is one executable redaction step.
type compiledRule struct {
	name   string
	origin string
	re     *regexp.Regexp
	// group is the submatch index to replace. 0 replaces the whole match; a
	// positive index replaces only that capture so surrounding structure (the
	// "Bearer " scheme, an "SSN:" label) survives into the corpus.
	group int
	repl  []byte
	// accept, when non-nil, vets a candidate match. It is how "credit-card
	// shape" becomes "actually passes Luhn" instead of "any long number",
	// which is the difference between a usable corpus and a field of holes.
	accept func([]byte) bool
	// needles are lower-case literals of which at least one MUST appear in any
	// match. They are a prescreen: a plain substring scan is an order of
	// magnitude cheaper than the automaton, and on real traffic most rules
	// never need to run at all. Every needle must be implied by the pattern —
	// an over-broad needle only costs time, a too-narrow one silently drops a
	// match. Rules with no implied literal (the numeric shapes) leave it empty
	// and always run.
	needles [][]byte
	// replIntroducesNeedle is true when this rule's replacement text contains a
	// needle belonging to some rule. It is the ONLY reason the prescreen has to
	// be re-derived after this rule fires.
	//
	// A stale prescreen is safe in one direction and unsafe in the other. It is
	// computed from the body BEFORE any replacement, so it can only claim a
	// needle the current body no longer has — one wasted scan that finds
	// nothing. It becomes UNSAFE only if a replacement puts a needle INTO the
	// body that the original lacked, because a later rule would then be skipped
	// despite being able to match. Re-lowering a multi-megabyte body after
	// every firing rule is a full-body allocation each time, on the request
	// handler's path, so the condition is decided up front instead.
	//
	// Deciding it by inspecting the replacement ALONE is sound only because of
	// two premises. Both are enforced by
	// TestPayloadPrescreenPremisesHold — if either stops holding, the marking
	// silently goes unsound and a secret survives.
	//
	//  1. No needle can straddle a seam. Every built-in replacement starts with
	//     '[' and ends with ']', and no needle contains either character. A
	//     needle spanning the boundary between kept text and a replacement
	//     would have to include the replacement's first or last byte, i.e. '['
	//     or ']'. So every needle in the new body lies wholly in kept text
	//     (hence already in the prescreen) or wholly inside one replacement
	//     (hence caught by inspecting that replacement).
	//  2. Org rules declare no needles, so nothing downstream of them is
	//     prescreen-gated and their arbitrary, unbracketed replacements cannot
	//     cause a skip. Org rules also run last, after every built-in.
	replIntroducesNeedle bool
}

// mayMatch reports whether this rule can possibly fire against a body whose
// ASCII-lowercased form is lowered.
func (c compiledRule) mayMatch(lowered []byte) bool {
	if len(c.needles) == 0 {
		return true
	}
	for _, n := range c.needles {
		if bytes.Contains(lowered, n) {
			return true
		}
	}
	return false
}

// asciiLower returns an ASCII-lowercased copy of b, used only for the needle
// prescreen. It never reaches the output.
func asciiLower(b []byte) []byte { return asciiLowerInto(nil, b) }

// asciiLowerInto lowercases b into dst's storage, reusing it when it is large
// enough. Re-deriving the prescreen after a rule fires is then free of a new
// multi-megabyte allocation in the common case, since a redacted body is close
// in size to the one before it.
func asciiLowerInto(dst, b []byte) []byte {
	if cap(dst) >= len(b) {
		dst = dst[:len(b)]
	} else {
		dst = make([]byte, len(b))
	}
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst[i] = c
	}
	return dst
}

// builtinNeedles maps a credential rule reused from the log-redaction set to
// the literals its pattern requires.
var builtinNeedles = map[string][]string{
	"cave-project-key":      {"cave_live_"},
	"aws-access-key":        {"akia", "asia", "aroa"},
	"aws-secret-access-key": {"aws"},
	"key-assignment":        {"api", "authorization", "secret", "token", "password"},
	// The CLOSING delimiter, not "private key": the pattern cannot match
	// without it, and prose that merely mentions a private key — or a
	// truncated paste that opens a block and never closes it — then costs
	// nothing instead of a full automaton pass over the body.
	"pem-private-key":         {"-----end "},
	"dsn-with-credentials":    {"postgres", "mysql", "mongodb", "redis"},
	"sk-prefixed-key":         {"sk-"},
	"credential-prefix-token": {"xox", "sk_live_", "rk_live_", "ghp_", "github_pat_", "aiza", "sk-proj-"},
}

func needles(lits ...string) [][]byte {
	out := make([][]byte, 0, len(lits))
	for _, l := range lits {
		out = append(out, []byte(l))
	}
	return out
}

// apply rewrites in and reports how many matches it replaced. It never mutates
// in; a pass with no accepted match returns the original slice.
//
// It runs two passes on purpose. The first collects the accepted spans and
// computes the EXACT output length, so the second allocates once at the right
// size. Appending into a len(in)-capacity buffer looks equivalent but is not:
// placeholders are usually longer than what they replace, so the buffer
// overflows on the final append and Go doubles it — a second full-body
// allocation per firing rule, on the request handler's path.
func (c compiledRule) apply(in []byte) ([]byte, int) {
	locs := c.re.FindAllSubmatchIndex(in, -1)
	if len(locs) == 0 {
		return in, 0
	}
	spans := make([][2]int, 0, len(locs))
	size, last := len(in), 0
	for _, loc := range locs {
		lo, hi := loc[2*c.group], loc[2*c.group+1]
		if lo < 0 || hi <= lo || lo < last {
			continue
		}
		if c.accept != nil && !c.accept(in[lo:hi]) {
			continue
		}
		spans = append(spans, [2]int{lo, hi})
		size += len(c.repl) - (hi - lo)
		last = hi
	}
	if len(spans) == 0 {
		return in, 0
	}
	out := make([]byte, 0, size)
	last = 0
	for _, sp := range spans {
		out = append(out, in[last:sp[0]]...)
		out = append(out, c.repl...)
		last = sp[1]
	}
	return append(out, in[last:]...), len(spans)
}

func placeholder(name string) []byte { return []byte("[REDACTED:" + name + "]") }

// builtinPayloadRules is the floor every capture is held to, in application
// order. Secret patterns run first because they are the most specific; the
// broad PII shapes run last so a narrow rule (a DSN's userinfo) is not eaten by
// a broad one (the email inside it).
var builtinPayloadRules = buildBuiltinPayloadRules()

func buildBuiltinPayloadRules() []compiledRule {
	var out []compiledRule

	// Credential patterns are reused verbatim from the log-redaction set above
	// so a fix lands in both surfaces at once. bearer-token is skipped here and
	// re-added below with a capture group, so the "Bearer " scheme survives.
	for _, r := range rules {
		if r.name == "bearer-token" {
			continue
		}
		step := compiledRule{
			name:    r.name,
			origin:  OriginBuiltin,
			re:      r.re,
			repl:    placeholder(r.name),
			needles: needles(builtinNeedles[r.name]...),
		}
		if r.name == "key-assignment" {
			// A field NAME containing a keyword is not a credential: agent
			// traffic and source code are full of "total_tokens": 1234567890
			// and `usage.OutputTokens`, and the widened keyword-to-delimiter
			// gap matches every one of them.
			step.accept = credentialAssignmentValueIsSecretShaped
		}
		out = append(out, step)
	}

	out = append(out,
		compiledRule{
			name:    "bearer-token",
			origin:  OriginBuiltin,
			re:      regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._~+/=\-]{20,})`),
			group:   2,
			repl:    placeholder("bearer-token"),
			needles: needles("bearer"),
		},
		// Credit cards, gated on Luhn. The three alternatives are the real
		// printed groupings (4-4-4-N, Amex 4-6-5, unbroken 13–19) rather than
		// one loose "digits and separators" repetition: a loose repetition
		// greedily welds two adjacent unrelated numbers into one 19-digit
		// candidate, which then fails Luhn and leaks BOTH. Without the Luhn
		// gate the rule would shred order numbers and IDs out of every capture.
		compiledRule{
			name:   "credit-card",
			origin: OriginBuiltin,
			re:     regexp.MustCompile(`\b(?:\d{4}[ \-]\d{4}[ \-]\d{4}[ \-]\d{1,4}|\d{4}[ \-]\d{6}[ \-]\d{5}|\d{13,19})\b`),
			repl:   placeholder("credit-card"),
			accept: luhnValid,
		},
		// SSNs in the punctuated form, gated on the SSA's own validity rules
		// (no 000/666/9xx area, no 00 group, no 0000 serial).
		compiledRule{
			name:   "ssn",
			origin: OriginBuiltin,
			re:     regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			repl:   placeholder("ssn"),
			accept: validSSN,
		},
		// Bare 9-digit government IDs, only when a label says so. Matching every
		// 9-digit run unlabelled would shred truncated epoch-ms timestamps,
		// order and invoice IDs, unseparated phone numbers, and row IDs — the
		// exact fields a replay corpus exists for — while a real SSN in a
		// customer prompt almost always arrives labelled or in the punctuated
		// form the rule above already catches. The label is the evidence that
		// makes the loss worth it, so the label set is wide: SSN, SS#, social
		// security, and the tax identifiers that share the shape and the
		// sensitivity (TIN, ITIN, tax ID). The label itself is kept — only the
		// number is replaced.
		//
		// The gap is ONE `[^0-9]{0,16}` window. It replaced a
		// `[^0-9\n]{0,12}\n?[^0-9\n]{0,12}` pair that reached 24 non-newline
		// characters past the label — far enough to weld a label onto the next
		// field's number in a form dump — and cost roughly a quarter of the
		// whole redaction pass on an adversarial body. 16 still clears every
		// real form separator (": ", "\nNumber: ", " (last 4 on file) ") and
		// tolerates newlines inside the window, so the multi-line dump case
		// ("SSN\n123456789") is still caught.
		//
		// Label-only is a judgement for a tenant-scoped, encrypted, TTL'd
		// capture store. It is NOT adequate for a corpus that leaves the tenant
		// boundary; that decision belongs to whoever builds the export.
		compiledRule{
			name:    "ssn",
			origin:  OriginBuiltin,
			re:      regexp.MustCompile(`(?i)\b(?:ssn\b|social[ _-]?sec(?:urity)?\b|itin\b|tin\b|tax[ _-]?id\b|ss ?#)(?:[ _-]?(?:number|no\.?|#))?[^0-9]{0,16}(\d{3}[ \-]?\d{2}[ \-]?\d{4})\b`),
			group:   1,
			repl:    placeholder("ssn"),
			accept:  validSSN,
			needles: needles("ssn", "social", "tin", "tax", "ss#", "ss #"),
		},
		compiledRule{
			name:    "email",
			origin:  OriginBuiltin,
			re:      regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,24}\b`),
			repl:    placeholder("email"),
			needles: needles("@"),
		},
	)
	for _, c := range out {
		// A group index past the pattern's capture count makes apply() read
		// loc[2*group] out of range at the first match — i.e. the rule works
		// until a body finally contains the secret it exists for, and then the
		// process dies mid-capture. Fail at init instead, where a test sees it.
		if c.group > c.re.NumSubexp() {
			panic("redact: builtin rule " + c.name + " replaces group " +
				strconv.Itoa(c.group) + " but its pattern has " +
				strconv.Itoa(c.re.NumSubexp()) + " capture groups")
		}
	}
	return out
}

// needleUniverse is every needle any rule declares. Org rules contribute none
// (their patterns are arbitrary, so they always run), which makes the universe
// a fixed set known at init.
var needleUniverse = func() [][]byte {
	var out [][]byte
	seen := map[string]bool{}
	for _, c := range builtinPayloadRules {
		for _, n := range c.needles {
			if !seen[string(n)] {
				seen[string(n)] = true
				out = append(out, n)
			}
		}
	}
	return out
}()

// introducesNeedle reports whether replacement text could put a needle into a
// body that did not have one, which is the only case that invalidates the
// prescreen. It is evaluated once per rule, never per body.
func introducesNeedle(repl []byte) bool {
	lowered := asciiLower(repl)
	for _, n := range needleUniverse {
		if bytes.Contains(lowered, n) {
			return true
		}
	}
	return false
}

// markNeedleIntroducers stamps replIntroducesNeedle on the built-in rules. It
// runs after builtinPayloadRules is built, because it needs the finished needle
// universe.
var _ = func() struct{} {
	for i := range builtinPayloadRules {
		builtinPayloadRules[i].replIntroducesNeedle = introducesNeedle(builtinPayloadRules[i].repl)
	}
	return struct{}{}
}()

// builtinRuleSetFingerprint is the built-in half of RuleSetHash. Computed once;
// it changes whenever a built-in pattern changes.
var builtinRuleSetFingerprint = func() string {
	var sb strings.Builder
	for _, c := range builtinPayloadRules {
		sb.WriteString(c.name)
		sb.WriteByte('\x1f')
		sb.WriteString(strconv.Itoa(c.group))
		sb.WriteByte('\x1f')
		sb.WriteString(c.re.String())
		sb.WriteByte('\x1e')
	}
	return sb.String()
}()

// Payload scrubs a captured request or response body and returns the redacted
// bytes plus the evidence of what was removed.
//
// On ANY error it returns a nil body: an unavailable or unusable rule set means
// no capture, not a capture with a note. Callers must treat a non-nil error as
// "do not store this".
//
// rules are the organization's enabled redaction_rules rows. They are additive
// only — the built-in floor runs first and cannot be disabled, shadowed, or
// reordered by anything a tenant configures.
//
// The returned slice ALIASES body when no rule fired; body is never mutated
// (TestPayloadDoesNotMutateInput), so this is safe to read, but a caller that
// intends to mutate the result in place must copy it first.
//
// Payload does not judge how BROAD a rule is. A pattern like ".+" compiles,
// matches, and collapses the whole body into one placeholder — a self-inflicted
// loss of corpus value, not a leak, and the only pattern shapes rejected here
// are the ones that are unusable rather than merely greedy. Rejecting breadth
// belongs at rule-write time, where an operator is present to see the error;
// there is no such surface yet (see the H1 report's concern on the missing
// redaction_rules validator).
func Payload(body []byte, rules []Rule) ([]byte, RedactionReport, error) {
	if len(body) > MaxPayloadBytes {
		return nil, RedactionReport{}, fmt.Errorf("%w: %d bytes (limit %d)", ErrBodyTooLarge, len(body), MaxPayloadBytes)
	}
	orgRules, fingerprint, err := compileOrgRules(rules)
	if err != nil {
		return nil, RedactionReport{}, err
	}

	sum := sha256.Sum256([]byte(builtinRuleSetFingerprint + "\x00" + fingerprint))
	report := RedactionReport{
		RuleSetHash: hex.EncodeToString(sum[:16]),
		BytesIn:     len(body),
	}

	out := body
	// The prescreen is derived ONCE, then re-derived only after a rule whose
	// replacement can introduce a needle (see replIntroducesNeedle). Of the
	// built-ins only three can, and only when they actually fire.
	lowered := asciiLower(out)
	// Built-ins first: whatever an org rule does afterwards, it acts on a body
	// the floor has already cleared.
	for _, step := range append(append([]compiledRule{}, builtinPayloadRules...), orgRules...) {
		if !step.mayMatch(lowered) {
			continue
		}
		next, n := step.apply(out)
		if n == 0 {
			continue
		}
		out = next
		if step.replIntroducesNeedle {
			lowered = asciiLowerInto(lowered, out)
		}
		report.TotalMatches += n
		report.Findings = addFinding(report.Findings, step.name, step.origin, n)
	}
	report.BytesOut = len(out)
	return out, report, nil
}

// addFinding accumulates counts per (rule, origin) preserving first-fire order,
// which is deterministic because the rule order is.
func addFinding(findings []Finding, name, origin string, n int) []Finding {
	for i := range findings {
		if findings[i].Rule == name && findings[i].Origin == origin {
			findings[i].Count += n
			return findings
		}
	}
	return append(findings, Finding{Rule: name, Origin: origin, Count: n})
}

// compileOrgRules validates and canonically orders the tenant's rules, and
// returns a fingerprint of them for RuleSetHash.
func compileOrgRules(in []Rule) ([]compiledRule, string, error) {
	if len(in) == 0 {
		return nil, "", nil
	}
	sorted := append([]Rule(nil), in...)
	// Canonical order: the caller's slice order must never reach the output.
	// Lower priority number wins, ties broken by every remaining field so the
	// order is total.
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Pattern != b.Pattern {
			return a.Pattern < b.Pattern
		}
		return a.Replacement < b.Replacement
	})

	var out []compiledRule
	var sb strings.Builder
	for _, r := range sorted {
		sb.WriteString(r.Name)
		sb.WriteByte('\x1f')
		sb.WriteString(r.Type)
		sb.WriteByte('\x1f')
		sb.WriteString(r.Pattern)
		sb.WriteByte('\x1f')
		sb.WriteString(r.Replacement)
		sb.WriteByte('\x1e')

		switch r.Type {
		case RuleTypeBuiltin:
			// A reference to the unconditional floor. Nothing to run, and
			// nothing it could switch off.
			continue
		case RuleTypeRegex:
		case RuleTypeJSONPath, RuleTypeHeader:
			return nil, "", fmt.Errorf("%w: %q (rule %q)", ErrRuleUnsupported, r.Type, r.Name)
		default:
			return nil, "", fmt.Errorf("%w: %q (rule %q)", ErrRuleUnsupported, r.Type, r.Name)
		}

		if strings.TrimSpace(r.Name) == "" {
			return nil, "", fmt.Errorf("%w: empty name", ErrInvalidRule)
		}
		if r.Pattern == "" {
			return nil, "", fmt.Errorf("%w: rule %q has an empty pattern", ErrInvalidRule, r.Name)
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, "", fmt.Errorf("%w: rule %q: %s", ErrInvalidRule, r.Name, err)
		}
		if re.MatchString("") {
			// Such a pattern matches at every position and would replace the
			// whole body with placeholders. Refuse it rather than destroy the
			// capture.
			return nil, "", fmt.Errorf("%w: rule %q matches the empty string", ErrInvalidRule, r.Name)
		}
		repl := r.Replacement
		if repl == "" {
			repl = "[REDACTED:" + r.Name + "]"
		}
		out = append(out, compiledRule{
			name:                 r.Name,
			origin:               OriginOrg,
			re:                   re,
			replIntroducesNeedle: introducesNeedle([]byte(repl)),
			// The replacement is operator-supplied data, not a regexp
			// template: a literal replace keeps "$1" from expanding a captured
			// group back into the output.
			repl: []byte(repl),
		})
	}
	return out, sb.String(), nil
}

// luhnValid reports whether the digits in b form a 13–19 digit Luhn-valid
// number. Separators are ignored.
func luhnValid(b []byte) bool {
	sum, digits := 0, 0
	double := false
	for i := len(b) - 1; i >= 0; i-- {
		c := b[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
		digits++
	}
	if digits < 13 || digits > 19 {
		return false
	}
	return sum%10 == 0
}

// validSSN reports whether the digits in b form a structurally issuable US
// Social Security Number. Area 000, 666 and 900–999, group 00, and serial 0000
// are never issued, so rejecting them keeps ordinary 9-digit identifiers in the
// corpus.
func validSSN(b []byte) bool {
	var d [9]byte
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			continue
		}
		if n == len(d) {
			return false
		}
		d[n] = c
		n++
	}
	if n != len(d) {
		return false
	}
	area := string(d[0:3])
	if area == "000" || area == "666" || d[0] == '9' {
		return false
	}
	if string(d[3:5]) == "00" || string(d[5:9]) == "0000" {
		return false
	}
	return true
}
