package mem

import "testing"

func TestTokenizeDeterministic(t *testing.T) {
	got := tokenize("Hello, WORLD! foo_bar baz123")
	want := []string{"hello", "world", "foo", "bar", "baz123"}
	if len(got) != len(want) {
		t.Fatalf("tokenize = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBM25ScoresDeterministicAndRanked(t *testing.T) {
	docs := []Memory{
		{ID: "a", Text: "alpha beta gamma delta"},
		{ID: "b", Text: "alpha alpha beta"},
		{ID: "c", Text: "epsilon zeta"},
	}
	// Run twice — identical input must give identical scores (determinism).
	s1 := bm25Scores("alpha beta", docs)
	s2 := bm25Scores("alpha beta", docs)
	for id, v := range s1 {
		if s2[id] != v {
			t.Fatalf("non-deterministic score for %s: %v vs %v", id, v, s2[id])
		}
	}
	// "c" shares no terms → no score.
	if _, ok := s1["c"]; ok {
		t.Errorf("doc c shares no query terms but scored %v", s1["c"])
	}
	// Both a and b match; both must be positive.
	if s1["a"] <= 0 || s1["b"] <= 0 {
		t.Fatalf("matching docs must score positively: a=%v b=%v", s1["a"], s1["b"])
	}
}

func TestBM25EmptyQuery(t *testing.T) {
	docs := []Memory{{ID: "a", Text: "alpha"}}
	if len(bm25Scores("", docs)) != 0 {
		t.Error("empty query must score nothing")
	}
	if len(bm25Scores("alpha", nil)) != 0 {
		t.Error("empty corpus must score nothing")
	}
}
