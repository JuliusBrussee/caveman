// Package proposalrun is the single, pinned home for the canonical SHA-256 hash
// chain over the append-only `proposal_runs` audit trail (the Cave Agent's
// tool-call timeline). It lives in the shared platform module so BOTH writers —
// control-api's store (the `queued` row) and the worker's agent consumer (every
// subsequent row) — compute byte-identical hashes from byte-identical inputs.
// A divergence here would silently break the chain, so there is exactly ONE
// implementation and both services import it.
//
// The discipline mirrors cloud/metering (receipts): a canonical field set, fixed
// number formatting, deterministic JSON, and a re-walkable VerifyChain. The
// honesty point is auditability — proposal_runs records every action the agent
// took, on what, under which policy (EU AI Act logging) — and the chain makes
// tampering detectable.
package proposalrun

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Run is one append-only proposal_runs row in the canonical shape the chain is
// computed over. cost_usd is the AGENT'S OWN COGS spend for the step — never
// savings, never netted against any savings figure.
type Run struct {
	Seq       int64           `json:"seq"`
	Action    string          `json:"action"`
	Detail    json.RawMessage `json:"detail"`
	CostUSD   float64         `json:"cost_usd"`
	PrevHash  string          `json:"prev_hash"`
	RowHash   string          `json:"row_hash"`
	CreatedAt time.Time       `json:"created_at"`
}

// CanonicalDetail returns the deterministic JSON encoding of a detail blob:
// keys sorted (Go's encoding/json sorts map keys recursively), whitespace
// stripped. An empty/absent detail canonicalizes to "{}".
//
// Numbers are decoded with Decoder.UseNumber and normalized by exact decimal
// coefficient/exponent arithmetic, so they never round through float64. The
// canonical form is a plain decimal with insignificant zeroes removed; zero is
// always "0". Thus storage-equivalent spellings (for example 1, 1.0, and 1e0)
// hash identically, while adjacent arbitrary-precision integers remain
// distinct. The bounds mirror PostgreSQL's numeric-backed jsonb limits and
// reject values that could not be persisted there before allocating output.
// The decoder enforces the JSON number grammar (including rejecting NaN/Inf),
// and a second decode below rejects any trailing value or non-whitespace data.
func CanonicalDetail(detail json.RawMessage) ([]byte, error) {
	if len(detail) == 0 {
		return []byte("{}"), nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(detail))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("proposalrun: detail is not valid JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("proposalrun: detail contains trailing JSON")
		}
		return nil, fmt.Errorf("proposalrun: detail contains trailing data: %w", err)
	}
	v, err := canonicalizeJSONValue(v)
	if err != nil {
		return nil, fmt.Errorf("proposalrun: canonicalize detail: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("proposalrun: marshal canonical detail: %w", err)
	}
	return out, nil
}

// PostgreSQL jsonb stores JSON numbers using its numeric type. These are the
// documented numeric bounds (131072 digits before and 16383 after the decimal
// point); rejecting at the canonicalization boundary keeps write and read-back
// hashing deterministic and avoids unbounded strings from hostile exponents.
// maxJSONBNumberExponent is an additional allocation guard. PostgreSQL can
// accept some zero literals with a larger positive exponent after normalizing
// their zero weight; this package rejects those spellings conservatively so a
// hostile exponent never drives unbounded intermediate arithmetic.
const (
	maxJSONBNumericIntegerDigits  = 131072
	maxJSONBNumericFractionDigits = 16383
	maxJSONBNumberCoefficient     = maxJSONBNumericIntegerDigits + maxJSONBNumericFractionDigits + 1
	maxJSONBNumberExponent        = maxJSONBNumericIntegerDigits + maxJSONBNumericFractionDigits
	maxJSONBNumberLexeme          = maxJSONBNumberCoefficient + 16
)

// canonicalJSONNumber is an already-validated JSON number representation used
// only while recursively rebuilding the decoded value. MarshalJSON writes the
// bytes directly, so the number is not quoted as a string.
type canonicalJSONNumber string

func (n canonicalJSONNumber) MarshalJSON() ([]byte, error) {
	return []byte(n), nil
}

func canonicalizeJSONValue(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		canonical, err := canonicalizeJSONNumber(x)
		if err != nil {
			return nil, err
		}
		return canonical, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			canonical, err := canonicalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = canonical
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, item := range x {
			canonical, err := canonicalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = canonical
		}
		return out, nil
	default:
		return v, nil
	}
}

// canonicalizeJSONNumber first computes an exact, storage-stable decimal. If
// encoding/json's historical float64 representation has the same exact value,
// it is retained to keep existing hashes compatible (for example 1e-7 remains
// 1e-7). Values that float64 would round, such as 2^53+1, use the exact decimal
// instead. Both paths converge for a Postgres jsonb read-back.
func canonicalizeJSONNumber(number json.Number) (canonicalJSONNumber, error) {
	literal := number.String()
	exact, err := normalizeJSONNumber(literal)
	if err != nil {
		return "", err
	}
	if exact == "0" {
		return canonicalJSONNumber(exact), nil
	}
	if value, err := number.Float64(); err == nil && !math.IsInf(value, 0) && !math.IsNaN(value) {
		legacy, err := json.Marshal(value)
		if err == nil {
			legacyExact, legacyErr := normalizeJSONNumber(string(legacy))
			if legacyErr == nil && legacyExact == exact {
				return canonicalJSONNumber(string(legacy)), nil
			}
		}
	}
	return canonicalJSONNumber(exact), nil
}

// normalizeJSONNumber converts one JSON number literal to the shortest plain
// decimal that has exactly the same base-10 value. It deliberately avoids
// floating-point conversion; all arithmetic is over digit counts and strings.
func normalizeJSONNumber(literal string) (string, error) {
	if len(literal) == 0 || len(literal) > maxJSONBNumberLexeme {
		return "", fmt.Errorf("number exceeds the supported JSONB numeric size")
	}
	start := 0
	negative := false
	if literal[0] == '-' {
		negative = true
		start = 1
	}
	if start == len(literal) {
		return "", fmt.Errorf("invalid JSON number")
	}

	mantissaEnd := len(literal)
	exponent := 0
	for i := start; i < len(literal); i++ {
		if literal[i] != 'e' && literal[i] != 'E' {
			continue
		}
		if mantissaEnd != len(literal) {
			return "", fmt.Errorf("invalid JSON number")
		}
		mantissaEnd = i
		var err error
		exponent, err = parseJSONExponent(literal[i+1:])
		if err != nil {
			return "", err
		}
	}
	mantissa := literal[start:mantissaEnd]
	dot := strings.IndexByte(mantissa, '.')
	if dot < 0 {
		dot = len(mantissa)
	}
	integerPart := mantissa[:dot]
	fractionPart := ""
	if dot < len(mantissa) {
		fractionPart = mantissa[dot+1:]
	}
	if len(integerPart) == 0 || len(integerPart)+len(fractionPart) > maxJSONBNumberCoefficient {
		return "", fmt.Errorf("invalid or oversized JSON number")
	}
	for i := 0; i < len(integerPart); i++ {
		if integerPart[i] < '0' || integerPart[i] > '9' {
			return "", fmt.Errorf("invalid JSON number")
		}
	}
	for i := 0; i < len(fractionPart); i++ {
		if fractionPart[i] < '0' || fractionPart[i] > '9' {
			return "", fmt.Errorf("invalid JSON number")
		}
	}
	// PostgreSQL numeric retains the input display scale in jsonb output,
	// including trailing fractional zeroes. Validate the raw scale before the
	// canonical reduction below strips those zeroes; otherwise a value such as
	// 1.0000e-16383 would reduce to a representable scale while its original
	// jsonb insert would fail at the 16383-digit numeric limit.
	rawScale := len(fractionPart) - exponent
	if rawScale > maxJSONBNumericFractionDigits {
		return "", fmt.Errorf("JSON number exceeds PostgreSQL fraction-digit limit")
	}

	digits := strings.TrimLeft(integerPart+fractionPart, "0")
	if digits == "" {
		return "0", nil
	}
	scale := len(fractionPart) - exponent
	for scale > 0 && strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
		scale--
	}

	var out string
	switch {
	case scale <= 0:
		zeros := -scale
		if zeros > maxJSONBNumericIntegerDigits-len(digits) {
			return "", fmt.Errorf("JSON number exceeds PostgreSQL integer-digit limit")
		}
		out = digits + strings.Repeat("0", zeros)
	case scale >= len(digits):
		if scale > maxJSONBNumericFractionDigits {
			return "", fmt.Errorf("JSON number exceeds PostgreSQL fraction-digit limit")
		}
		out = "0." + strings.Repeat("0", scale-len(digits)) + digits
	default:
		if scale > maxJSONBNumericFractionDigits || len(digits)-scale > maxJSONBNumericIntegerDigits {
			return "", fmt.Errorf("JSON number exceeds PostgreSQL numeric limits")
		}
		point := len(digits) - scale
		out = digits[:point] + "." + digits[point:]
	}
	if negative {
		return "-" + out, nil
	}
	return out, nil
}

func parseJSONExponent(raw string) (int, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("invalid JSON number exponent")
	}
	negative := false
	if raw[0] == '+' || raw[0] == '-' {
		negative = raw[0] == '-'
		raw = raw[1:]
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("invalid JSON number exponent")
	}
	value := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, fmt.Errorf("invalid JSON number exponent")
		}
		digit := int(raw[i] - '0')
		if value > (maxJSONBNumberExponent-digit)/10 {
			return 0, fmt.Errorf("JSON number exponent exceeds supported range")
		}
		value = value*10 + digit
	}
	if negative {
		return -value, nil
	}
	return value, nil
}

// formatTime pins the created_at representation: UTC, microsecond resolution
// (Postgres TIMESTAMPTZ stores microseconds, so truncating here makes the value
// hashed at write time equal the value read back at verify time), RFC3339Nano.
func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

// RowHash computes the pinned canonical row hash:
//
//	row_hash = "sha256:" + hex(SHA256(
//	  prev_hash || "|" || seq || "|" || action || "|" ||
//	  canonicalJSON(detail) || "|" || cost_usd(fixed6) || "|" || created_at(RFC3339Nano,µs,UTC)
//	))
//
// The "|" separators are unambiguous because seq is numeric, action is a fixed
// vocabulary token (no pipes), detail is compact JSON, cost is fixed-6, and the
// timestamp is fixed-width.
func RowHash(prevHash string, seq int64, action string, detail json.RawMessage, costUSD float64, createdAt time.Time) (string, error) {
	canon, err := CanonicalDetail(detail)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte("|"))
	h.Write([]byte(strconv.FormatInt(seq, 10)))
	h.Write([]byte("|"))
	h.Write([]byte(action))
	h.Write([]byte("|"))
	h.Write(canon)
	h.Write([]byte("|"))
	h.Write([]byte(strconv.FormatFloat(costUSD, 'f', 6, 64)))
	h.Write([]byte("|"))
	h.Write([]byte(formatTime(createdAt)))
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyChain re-walks a proposal's runs ordered by seq and asserts:
//   - seq starts at 1 and increases by exactly 1 (no gaps, no dupes);
//   - each row's prev_hash equals the previous row's row_hash ("" for seq 1);
//   - each row's row_hash recomputes from its own stored fields (no tampering).
//
// It returns nil for an empty slice (a proposal with no runs yet is consistent).
func VerifyChain(runs []Run) error {
	if len(runs) == 0 {
		return nil
	}
	sorted := make([]Run, len(runs))
	copy(sorted, runs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	prevHash := ""
	for i, r := range sorted {
		wantSeq := int64(i + 1)
		if r.Seq != wantSeq {
			return fmt.Errorf("proposalrun: seq %d out of order (expected %d)", r.Seq, wantSeq)
		}
		if r.PrevHash != prevHash {
			return fmt.Errorf("proposalrun: seq %d prev_hash does not link to the prior row", r.Seq)
		}
		got, err := RowHash(r.PrevHash, r.Seq, r.Action, r.Detail, r.CostUSD, r.CreatedAt)
		if err != nil {
			return fmt.Errorf("proposalrun: seq %d recompute: %w", r.Seq, err)
		}
		if got != r.RowHash {
			return fmt.Errorf("proposalrun: seq %d row_hash mismatch (tampered)", r.Seq)
		}
		prevHash = r.RowHash
	}
	return nil
}
