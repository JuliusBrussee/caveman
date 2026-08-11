package mem

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestRecallRespectsTokenBudget stores several matching memories whose combined
// injected tokens exceed a small budget and asserts Recall packs them greedily
// without overshooting. Before the budget contract, Recall returned every match
// whole regardless of size.
func TestRecallRespectsTokenBudget(t *testing.T) {
	s := newStore(t)
	const memories = 12
	for i := 0; i < memories; i++ {
		// Distinct, non-repetitive text so nothing collapses via redundancy and
		// each memory carries a real, similar token cost.
		if _, err := s.Remember(fmt.Sprintf(
			"kubernetes ingress note %d covers nginx routing rule alpha%d beta%d gamma%d delta%d for the cluster",
			i, i, i, i, i)); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}

	const budget = 60
	hits, err := s.Recall("kubernetes ingress nginx routing rule", RecallOptions{Limit: memories, TokenBudget: budget})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit within budget")
	}
	total := 0
	for _, h := range hits {
		total += h.TokensAdded
	}
	if total > budget {
		t.Fatalf("recall injected %d tokens, over the %d-token budget (%d hits)", total, budget, len(hits))
	}
	if len(hits) >= memories {
		t.Fatalf("budget must drop some of the %d matches, returned %d", memories, len(hits))
	}
}

func TestRecallUnlimitedSentinelReturnsEveryRankedHit(t *testing.T) {
	s := newStore(t)
	const memories = 12
	for i := 0; i < memories; i++ {
		if _, err := s.Remember(fmt.Sprintf(
			"kubernetes ingress note %d covers nginx routing alpha%d beta%d gamma%d delta%d for the cluster",
			i, i, i, i, i)); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}

	hits, err := s.Recall("kubernetes ingress nginx routing", RecallOptions{
		Limit:       memories,
		TokenBudget: UnlimitedTokenBudget,
	})
	if err != nil {
		t.Fatalf("recall unlimited: %v", err)
	}
	if len(hits) != memories {
		t.Fatalf("unlimited recall returned %d of %d ranked hits", len(hits), memories)
	}
	total := 0
	for _, hit := range hits {
		total += hit.TokensAdded
	}
	if total <= 60 {
		t.Fatalf("fixture only used %d tokens; does not prove packing was disabled", total)
	}
}

func TestRecallRejectsUnknownNegativeTokenBudget(t *testing.T) {
	s := newStore(t)
	if _, err := s.Recall("anything", RecallOptions{TokenBudget: -2}); err == nil {
		t.Fatal("unknown negative token budget must fail closed")
	}
}

// TestRecallOversizedSingleHitReturnsHeadAndHandle reproduces the 440k-token
// bug: a single large memory that compression does not shrink. Recall must
// return a budget-sized head plus a working CCR handle to the byte-exact
// original — never the whole body, and never an empty handle.
func TestRecallOversizedSingleHitReturnsHeadAndHandle(t *testing.T) {
	s := newStore(t)
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "kubernetes ingress fact %d unique-token-%d distinct phrase zeta%d omega%d\n", i, i, i, i)
	}
	big := b.String()
	if len(big) > MaxMemoryBytes {
		t.Fatalf("test fixture %d bytes exceeds the %d-byte cap", len(big), MaxMemoryBytes)
	}
	if _, err := s.Remember(big); err != nil {
		t.Fatalf("remember: %v", err)
	}

	const budget = 100
	hits, err := s.Recall("kubernetes ingress fact", RecallOptions{TokenBudget: budget})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("oversized single hit should return exactly one head, got %d", len(hits))
	}
	h := hits[0]
	if h.TokensAdded > budget {
		t.Fatalf("head injected %d tokens, over the %d-token budget", h.TokensAdded, budget)
	}
	if len(h.Text) >= len(big) {
		t.Fatalf("head (%d bytes) was not truncated below the body (%d bytes)", len(h.Text), len(big))
	}
	if h.RecoveryHandle == "" {
		t.Fatal("a head that dropped the tail must carry a recovery handle")
	}
	original, err := s.Recover(h.RecoveryHandle)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if string(original) != big {
		t.Fatal("recovered original is not byte-identical to the stored memory")
	}
}

// TestRememberRejectsOversized asserts Remember fails closed on a memory over the
// byte cap, with the cave_memory_too_large idiom, and stores nothing.
func TestRememberRejectsOversized(t *testing.T) {
	s := newStore(t)
	_, err := s.Remember(strings.Repeat("x", MaxMemoryBytes+1))
	if err == nil {
		t.Fatal("expected an error remembering an oversized memory")
	}
	if !errors.Is(err, ErrMemoryTooLarge) {
		t.Fatalf("error = %v, want ErrMemoryTooLarge", err)
	}
	if count, err := s.Count(); err != nil || count != 0 {
		t.Fatalf("rejected memory must not be stored: count=%d err=%v", count, err)
	}
	// The byte cap itself must remain storable.
	if _, err := s.Remember(strings.Repeat("y", MaxMemoryBytes)); err != nil {
		t.Fatalf("a memory exactly at the cap must be accepted: %v", err)
	}
}
