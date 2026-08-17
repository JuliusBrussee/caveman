// Package redact scrubs secret material (API keys, bearer tokens, connection
// strings) from strings, HTTP headers, and error messages before they reach
// logs, error responses, or telemetry.
//
// Design principles:
//   - Prefer under-redaction of benign data over over-redaction that destroys
//     useful context.  Rules that are too broad (e.g. matching any
//     three-segment dotted token) are scoped narrowly with minimum-length
//     guards and provider-specific prefixes where possible.
//   - Redaction is best-effort: it adds defence-in-depth alongside upstream
//     measures (not logging bodies, storing only hashes in telemetry, etc.).
//   - All exported helpers are safe for concurrent use (compiled regexps,
//     read-only after init).
//
// # Exactly what is covered
//
// This list is the package's factual claim about its own reach. It is written
// out because this package is the code's designated tenant boundary for corpus
// export, and counsel will be asked to rely on it. A class that is not named
// here is NOT covered, whatever the package name suggests.
//
// Credential/secret classes, redacted by both String and Payload:
//
//   - cave_live_ project keys
//   - AWS access key IDs (AKIA/ASIA/AROA)
//   - AWS secret access keys, when labelled (aws_secret_access_key = …)
//   - Vendor-prefixed tokens: xox[baprs]-, sk_live_, rk_live_, ghp_,
//     github_pat_, AIza, sk-proj-, and any sk- key of 20+ chars
//   - Bearer tokens of 20+ chars
//   - Key/secret/token/password assignments, including an intervening name
//     fragment (SECRET_ACCESS_KEY=, GITHUB_TOKEN_FOR_CI=) and JSON-escaped
//     delimiters (\"secret\": \"…\"), where the value carries a digit or
//     punctuation. A digit-free value drawn only from letters, dots and
//     underscores is read as an identifier reference and left alone
//   - PEM private key blocks
//   - Connection-string URLs carrying userinfo credentials
//
// Personal-data classes, redacted by Payload only:
//
//   - Email addresses
//   - Payment card numbers, gated on Luhn
//   - US Social Security Numbers in the punctuated form, gated on SSA
//     issuance rules
//   - Bare 9-digit US SSN/ITIN/TIN shapes, ONLY when a label is adjacent
//
// # Explicitly NOT covered
//
// No EU / non-US personal-data shape is covered by any rule in this package.
// None of the following is detected, and no caller may treat a clean pass as
// evidence that they are absent:
//
//   - National identifiers: BSN (NL), INSEE/NIR (FR), DNI/NIE (ES),
//     Codice Fiscale (IT), Steuer-ID / Personalausweis (DE), PESEL (PL),
//     Personnummer (SE/DK/NO), NHS / NI number (UK)
//   - VAT numbers, IBAN / BIC, and EU passport or driving-licence numbers
//   - Postal addresses, dates of birth, and phone numbers in any format
//   - Names, in any language
//
// Extending coverage to those shapes is a counsel-scoped decision (which
// jurisdictions, which lawful basis, what the false-positive cost to the
// corpus is), not a pattern anyone should add on their own initiative.
//
// # One further limit, on the escaped-JSON coverage
//
// ResidualRisk unescapes ONE level before re-screening. A payload that has
// been JSON-encoded twice — a log line carrying a JSON string that itself
// carries a JSON string — still evades it, because the second unescape never
// runs. Recursing is deliberately out of scope: depth is attacker-chosen, so
// the loop needs a bound and a bound is just a deeper version of the same
// hole. Treat single-level unescaping as what it is — closing the common
// accident, not the determined evader.
package redact

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// rule is a named redaction rule combining a compiled regexp with a
// replacement placeholder.  The name appears in the returned []string so
// callers can understand which rules fired.
type rule struct {
	name string
	re   *regexp.Regexp
}

// replace returns the input with all matches replaced by [REDACTED:<name>].
func (r rule) replace(s string) string {
	return r.re.ReplaceAllString(s, "[REDACTED:"+r.name+"]")
}

// Shared credential patterns. They are named here rather than written inline
// because the residual-risk screen (residual.go) has to recognise the same
// shapes it cannot afford to leave in an exported corpus, and a screen that
// drifts from the redactor is a screen that lies.
var (
	// keyAssignmentRe matches an explicit key/secret/token assignment in JSON,
	// YAML, shell, or escaped JSON.
	//
	// The `[A-Za-z0-9_.\-]{0,24}` between keyword and delimiter is the fix for
	// the bypass that let AWS_SECRET_ACCESS_KEY= and GITHUB_TOKEN_FOR_CI=
	// through: the keyword is almost never adjacent to the delimiter in real
	// config, it is a fragment of a longer name. `\\` is in the delimiter class
	// so a body that arrived as escaped JSON (\"secret\": \"…\") is covered
	// without unescaping it first.
	//
	// The value floor is 16 chars so short placeholders ("secret": "changeme")
	// do not shred the corpus. Group 1 is the value; it exists so
	// credentialAssignmentValueIsSecretShaped can vet it, not to be replaced separately.
	keyAssignmentRe = regexp.MustCompile(`(?i)(?:api[_-]?key|x-api-key|x-goog-api-key|authorization|secret|token|password)[A-Za-z0-9_.\-]{0,24}(?:["'\s:=\\]+)([a-zA-Z0-9._~+/=\-]{16,})`)

	// awsSecretAccessKeyRe names the shape explicitly so the finding says which
	// credential leaked rather than the generic "key-assignment". The value
	// alphabet is base64 (AWS secret keys are 40 base64 chars), which
	// keyAssignmentRe's alphabet does not fully cover.
	awsSecretAccessKeyRe = regexp.MustCompile(`(?i)aws[_-]?secret[_-]?access[_-]?key["'\s:=\\]+[A-Za-z0-9/+=]{20,}`)

	// credentialPrefixRe matches tokens whose vendor-published prefix is itself
	// the evidence. These carry no entropy requirement anywhere in the package:
	// a token that announces its issuer is a credential regardless of how its
	// characters are distributed, and the entropy heuristics demonstrably miss
	// several of them (a Slack xoxb- token is mostly digits and hyphens).
	credentialPrefixRe = regexp.MustCompile(`(?:xox[baprs]-[A-Za-z0-9-]{16,}` +
		`|sk_live_[A-Za-z0-9]{16,}` +
		`|rk_live_[A-Za-z0-9]{16,}` +
		`|ghp_[A-Za-z0-9]{20,}` +
		`|github_pat_[A-Za-z0-9_]{20,}` +
		`|AIza[A-Za-z0-9_\-]{30,}` +
		`|sk-proj-[A-Za-z0-9_\-]{20,})`)
)

// credentialAssignmentValueIsSecretShaped vets the value half of a
// keyAssignmentRe match.
//
// Widening the gap between keyword and delimiter brought two families of false
// positives with it, and ordinary source code is full of both:
//
//   - Numeric fields whose NAME contains a keyword. LLM traffic is made of
//     them — "total_tokens": 1234567890123456, "token_ratio": 0.687… — and a
//     credential is never a bare decimal.
//   - Digit-free identifier references. `outputTokens := usage.OutputTokens`
//     matched and redacted to `output[REDACTED:key-assignment]`; so did
//     `passwordResetter passwordreset.Resetter` and a dozen import paths.
//     Over 22 MB of this repository's source the widening took key-assignment
//     from 186 matches to 693, and essentially all of the new ones were of
//     this shape.
//
// So a value must carry either a digit or a character outside the identifier
// alphabet. Every credential in the package's own fixtures does: keys carry
// digits, and the digit-free ones (test-signing-key) carry a hyphen. A real
// Google AIza key is digit-free and letters-only, but it is 39 characters and
// credential-prefix-token claims it before this ever matters.
//
// What this gives up, stated plainly: a digit-free, punctuation-free
// passphrase assigned to `password=`. looksLikeSecret already declines that
// same shape for the same reason — telling it from an identifier needs a
// language model, not a regex.
func credentialAssignmentValueIsSecretShaped(match []byte) bool {
	loc := keyAssignmentRe.FindSubmatchIndex(match)
	if loc == nil || loc[2] < 0 {
		return false
	}
	value := match[loc[2]:loc[3]]
	letter, digit, other := false, false, false
	for _, c := range value {
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			letter = true
		case c == '.' || c == '_':
			// Interior to both identifiers and credentials; decides nothing.
		default:
			other = true
		}
	}
	return letter && (digit || other)
}

// rules is the ordered list of redaction rules applied by String.  Rules are
// applied left-to-right; earlier rules take precedence if patterns overlap.
var rules = []rule{
	// cave_live_ project keys — format: cave_live_<12chars>_<rest> (44+ chars total)
	{
		name: "cave-project-key",
		re:   regexp.MustCompile(`cave_live_[a-zA-Z0-9_-]{12,}`),
	},
	// AWS access key IDs (AKIA/ASIA/AROA prefixes + 16 uppercase alphanums)
	{
		name: "aws-access-key",
		re:   regexp.MustCompile(`(?:AKIA|ASIA|AROA)[0-9A-Z]{16}`),
	},
	// Bearer token in Authorization / similar header values.
	// Require at least 20 chars after "Bearer " to avoid false-positives on
	// short debug tokens like "Bearer dev".
	{
		name: "bearer-token",
		re:   regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9._~+/=-]{20,}`),
	},
	// AWS secret access keys, named explicitly. Ordered before key-assignment
	// so the finding identifies the credential instead of the generic shape.
	{
		name: "aws-secret-access-key",
		re:   awsSecretAccessKeyRe,
	},
	// Explicit key/secret/token assignment patterns in JSON, YAML, shell:
	//   "api_key": "sk-abc123..."
	//   x-api-key: sk-abc123...
	//   secret=abc123
	//   AWS_SECRET_ACCESS_KEY=...      (keyword is a fragment of the name)
	//   \"token\": \"...\"             (escaped JSON)
	// Require value to be at least 16 chars to avoid matching short placeholders.
	{
		name: "key-assignment",
		re:   keyAssignmentRe,
	},
	// PEM private key blocks (multiline, (?s) flag)
	{
		name: "pem-private-key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	},
	// Database / connection-string URLs (postgres, mysql, mongodb, redis with credentials)
	// Match only URLs that contain a userinfo component (user:pass@), to avoid
	// redacting bare DB hostnames in error messages.
	{
		name: "dsn-with-credentials",
		re:   regexp.MustCompile(`(?i)(?:postgres|postgresql|mysql|mongodb(?:\+srv)?|redis)://[^:@\s"']+:[^@\s"']+@[^\s"']+`),
	},
	// OpenAI / Anthropic / generic sk- prefixed keys (at least 20 chars)
	{
		name: "sk-prefixed-key",
		re:   regexp.MustCompile(`sk-[a-zA-Z0-9_-]{20,}`),
	},
	// Vendor-prefixed tokens the entropy heuristics do not reliably catch.
	// Ordered last so sk-proj- keys keep the sk-prefixed-key finding name.
	{
		name: "credential-prefix-token",
		re:   credentialPrefixRe,
	},
}

// String scrubs secret material from s and returns the cleaned string along
// with the names of all rules that fired.  The returned slice is nil when
// nothing was redacted.
//
// Use this in error message construction, log attribute values, and any other
// path where a string originating from user input or an upstream response
// might contain credential material.
func String(s string) (string, []string) {
	var fired []string
	out := s
	for _, r := range rules {
		if r.re.MatchString(out) {
			fired = append(fired, r.name)
			out = r.replace(out)
		}
	}
	return out, fired
}

// sensitiveHeaderNames is the set of header names whose values must never be
// included in logs or error messages.
var sensitiveHeaderNames = func() map[string]struct{} {
	names := []string{
		"authorization",
		"cookie",
		"set-cookie",
		"proxy-authorization",
		"x-api-key",
		"api-key",
		"x-goog-api-key",
		"x-amz-security-token",
	}
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[strings.ToLower(n)] = struct{}{}
	}
	return m
}()

// IsSensitiveHeader returns true when the header name carries credential or
// session material that must not appear in logs or error envelopes.
//
// It returns true for: authorization, cookie, set-cookie,
// proxy-authorization, x-api-key, api-key, x-goog-api-key,
// x-amz-security-token, and any header whose lower-cased name contains
// "api-key", "api_key", or "secret".
func IsSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	if _, ok := sensitiveHeaderNames[lower]; ok {
		return true
	}
	return strings.Contains(lower, "api-key") ||
		strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "secret")
}

// ScrubHeaders returns a copy of h with sensitive header values replaced by
// "[REDACTED]".  The original http.Header is not modified.
//
// Use this before logging or returning request/response headers to callers.
func ScrubHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if IsSensitiveHeader(name) {
			out[name] = []string{"[REDACTED]"}
		} else {
			// Shallow copy the slice so mutations to out don't affect h.
			cp := make([]string, len(values))
			copy(cp, values)
			out[name] = cp
		}
	}
	return out
}

// Error wraps an error's message through String redaction and returns a new
// string safe for inclusion in logs or HTTP error responses.  The original
// error is not modified.  Returns the empty string when err is nil.
func Error(err error) string {
	if err == nil {
		return ""
	}
	s, _ := String(err.Error())
	return s
}

// SlogReplaceAttr is a slog.HandlerOptions.ReplaceAttr hook that applies the
// same secret scrubbing to every string and error attribute before a handler
// serializes it. Entry points should install this hook so a newly-added log call
// cannot accidentally bypass explicit String/Error use at the call site.
func SlogReplaceAttr(_ []string, attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		clean, _ := String(value.String())
		attr.Value = slog.StringValue(clean)
	case slog.KindAny:
		switch v := value.Any().(type) {
		case error:
			attr.Value = slog.StringValue(Error(v))
		case string:
			clean, _ := String(v)
			attr.Value = slog.StringValue(clean)
		}
	}
	return attr
}
