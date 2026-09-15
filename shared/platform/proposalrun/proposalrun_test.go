package proposalrun_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/proposalrun"
)

// buildChain appends rows the way the two writers do: prev = last row_hash, seq+1.
func buildChain(t *testing.T, actions []string) []proposalrun.Run {
	t.Helper()
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	runs := make([]proposalrun.Run, 0, len(actions))
	prev := ""
	for i, a := range actions {
		seq := int64(i + 1)
		created := base.Add(time.Duration(i) * time.Second).Truncate(time.Microsecond)
		detail := json.RawMessage(`{"step":"` + a + `"}`)
		rh, err := proposalrun.RowHash(prev, seq, a, detail, 0, created)
		if err != nil {
			t.Fatalf("RowHash: %v", err)
		}
		runs = append(runs, proposalrun.Run{Seq: seq, Action: a, Detail: detail, PrevHash: prev, RowHash: rh, CreatedAt: created})
		prev = rh
	}
	return runs
}

func TestVerifyChain_Green(t *testing.T) {
	runs := buildChain(t, []string{"queued", "token_minted", "diff_applied", "gates_green", "pr_opened", "token_revoked"})
	if err := proposalrun.VerifyChain(runs); err != nil {
		t.Fatalf("expected a green chain, got: %v", err)
	}
}

func TestVerifyChain_DetectsTamperedDetail(t *testing.T) {
	runs := buildChain(t, []string{"queued", "diff_applied"})
	// Tamper the stored detail of row 2 without recomputing the hash.
	runs[1].Detail = json.RawMessage(`{"step":"diff_applied","files":999}`)
	if err := proposalrun.VerifyChain(runs); err == nil {
		t.Fatal("expected VerifyChain to detect tampered detail")
	}
}

func TestVerifyChain_DetectsBrokenLink(t *testing.T) {
	runs := buildChain(t, []string{"queued", "token_minted", "pr_opened"})
	// Cut the link: row 3's prev_hash no longer points at row 2.
	runs[2].PrevHash = "sha256:deadbeef"
	if err := proposalrun.VerifyChain(runs); err == nil {
		t.Fatal("expected VerifyChain to detect a broken prev_hash link")
	}
}

func TestVerifyChain_DetectsReorder(t *testing.T) {
	runs := buildChain(t, []string{"queued", "token_minted"})
	// Drop seq 1 and renumber seq 2 → 1: the recompute must fail (prev_hash/seq mismatch).
	bad := []proposalrun.Run{runs[1]}
	bad[0].Seq = 1
	if err := proposalrun.VerifyChain(bad); err == nil {
		t.Fatal("expected VerifyChain to reject a reseq'd row")
	}
}

// CanonicalDetail must be stable under key reordering / whitespace so the hash is
// independent of Postgres JSONB reformatting on read-back.
func TestCanonicalDetail_Stable(t *testing.T) {
	a, err := proposalrun.CanonicalDetail(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("CanonicalDetail a: %v", err)
	}
	b, err := proposalrun.CanonicalDetail(json.RawMessage(`{ "a": 1, "b": 2 }`))
	if err != nil {
		t.Fatalf("CanonicalDetail b: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical detail not stable: %q vs %q", a, b)
	}
	empty, _ := proposalrun.CanonicalDetail(nil)
	if string(empty) != "{}" {
		t.Fatalf("empty detail should canonicalize to {}, got %q", empty)
	}
}

func TestCanonicalDetail_PreservesExactNumberLiterals(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "2^53",
			in:   `{"large":9007199254740992}`,
			want: `{"large":9007199254740992}`,
		},
		{
			name: "2^53 plus one",
			in:   `{"large":9007199254740993}`,
			want: `{"large":9007199254740993}`,
		},
		{
			name: "large negative",
			in:   `{"large":-9007199254740993}`,
			want: `{"large":-9007199254740993}`,
		},
		{
			name: "decimal",
			in:   `{"value":123.4500}`,
			want: `{"value":123.45}`,
		},
		{
			name: "exponent",
			in:   `{"value":1.2300e+10}`,
			want: `{"value":12300000000}`,
		},
		{
			name: "nested array and map",
			in:   `{"z":[9007199254740993,-9007199254740993,1.000e-3],"a":{"n":9007199254740992}}`,
			want: `{"a":{"n":9007199254740992},"z":[9007199254740993,-9007199254740993,0.001]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := proposalrun.CanonicalDetail(json.RawMessage(tt.in))
			if err != nil {
				t.Fatalf("CanonicalDetail: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("canonical detail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalDetail_NormalizesStorageEquivalentNumberSpellings(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "integer decimal exponent", parts: []string{`1`, `1.0`, `1e0`}, want: `{"value":1}`},
		{name: "trailing fractional zeroes", parts: []string{`123.4500`, `123.45`, `1.2345e2`}, want: `{"value":123.45}`},
		{name: "expanded exponent", parts: []string{`1e3`, `1000`, `1.000e+3`}, want: `{"value":1000}`},
		{name: "small exponent", parts: []string{`1e-7`, `0.0000001`, `1.000e-7`}, want: `{"value":1e-7}`},
		{name: "signed zero", parts: []string{`0`, `-0`, `-0.0`, `0e20`}, want: `{"value":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, part := range tt.parts {
				got, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":` + part + `}`))
				if err != nil {
					t.Fatalf("CanonicalDetail(%s): %v", part, err)
				}
				if string(got) != tt.want {
					t.Fatalf("canonical detail for %s = %q, want %q", part, got, tt.want)
				}
			}
		})
	}
}

func TestCanonicalDetail_RejectsInvalidAndTrailingJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{"value":9007199254740993`},
		{name: "nan", raw: `{"value":NaN}`},
		{name: "infinity", raw: `{"value":Infinity}`},
		{name: "trailing value", raw: `{"value":1} {"value":2}`},
		{name: "trailing text", raw: `{"value":1} trailing`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := proposalrun.CanonicalDetail(json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("CanonicalDetail(%q) unexpectedly succeeded", tt.raw)
			}
		})
	}
}

func TestCanonicalDetail_PostgreSQLNumericBounds(t *testing.T) {
	// PostgreSQL numeric permits at most 16,383 digits of display scale. The
	// raw scale must be checked before canonicalization removes trailing zeros.
	acceptedFraction := `0.` + strings.Repeat("0", 16382) + "1"
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":` + acceptedFraction + `}`)); err != nil {
		t.Fatalf("16,383 fractional digits should be accepted: %v", err)
	}
	rejectedFraction := `0.` + strings.Repeat("0", 16383) + "1"
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":` + rejectedFraction + `}`)); err == nil {
		t.Fatal("16,384 fractional digits should be rejected")
	}

	// Trailing zeroes still contribute to PostgreSQL's raw display scale.
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":1.0000e-16379}`)); err != nil {
		t.Fatalf("raw scale at 16,383 should be accepted: %v", err)
	}
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":1.0000e-16380}`)); err == nil {
		t.Fatal("raw scale at 16,384 should be rejected even after zero trimming")
	}

	// The integer side has the documented 131,072-digit bound.
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":1e131071}`)); err != nil {
		t.Fatalf("131,072 integer digits should be accepted: %v", err)
	}
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":1e131072}`)); err == nil {
		t.Fatal("131,073 integer digits should be rejected")
	}
}

func TestCanonicalDetail_ConservativeZeroExponentBound(t *testing.T) {
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":0e147455}`)); err != nil {
		t.Fatalf("zero at the supported exponent bound should be accepted: %v", err)
	}
	// PostgreSQL can normalize some larger positive-exponent zero spellings,
	// but the canonicalizer rejects them to keep exponent work bounded.
	if _, err := proposalrun.CanonicalDetail(json.RawMessage(`{"value":0e147456}`)); err == nil {
		t.Fatal("zero beyond the conservative exponent bound should be rejected")
	}
}

func TestVerifyChain_DetectsAdjacentLargeIntegerTamper(t *testing.T) {
	created := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	detail := json.RawMessage(`{"provider_request_id":9007199254740992}`)
	rowHash, err := proposalrun.RowHash("", 1, "queued", detail, 0, created)
	if err != nil {
		t.Fatalf("RowHash: %v", err)
	}
	runs := []proposalrun.Run{{
		Seq:       1,
		Action:    "queued",
		Detail:    detail,
		PrevHash:  "",
		RowHash:   rowHash,
		CreatedAt: created,
	}}
	if err := proposalrun.VerifyChain(runs); err != nil {
		t.Fatalf("untampered chain should verify: %v", err)
	}

	// Keep row_hash unchanged while changing only the adjacent integer. A
	// float64-based canonicalizer would round both values to the same bytes.
	runs[0].Detail = json.RawMessage(`{"provider_request_id":9007199254740993}`)
	if err := proposalrun.VerifyChain(runs); err == nil {
		t.Fatal("expected VerifyChain to detect adjacent large integer tamper")
	}
}
