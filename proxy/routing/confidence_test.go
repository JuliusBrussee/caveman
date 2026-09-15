package routing

import "testing"

func TestConfidenceScoreEmptyIsNearZero(t *testing.T) {
	for _, ans := range []string{"", "   ", " \n\t\r "} {
		if s := ConfidenceScore(ans, Features{}); s > 0.05 {
			t.Fatalf("score = %v for %q, want ~0 for empty/whitespace answer", s, ans)
		}
	}
}

func TestConfidenceScoreRefusalIsLow(t *testing.T) {
	for _, ans := range []string{
		"I cannot help with that request.",
		"I'm sorry, but I can't assist with this.",
		"As an AI language model, I am unable to do that.",
		"I don't have access to that information.",
	} {
		if s := ConfidenceScore(ans, Features{}); s >= 0.5 {
			t.Fatalf("score = %v for %q, want low (<0.5) for a refusal", s, ans)
		}
	}
}

func TestConfidenceScoreVeryShortIsLow(t *testing.T) {
	if s := ConfidenceScore("ok", Features{}); s >= 0.5 {
		t.Fatalf("score = %v, want low (<0.5) for a very short answer", s)
	}
}

func TestConfidenceScoreNormalIsHigh(t *testing.T) {
	ans := "The capital of France is Paris, a city on the Seine in the north of the country."
	if s := ConfidenceScore(ans, Features{}); s < 0.8 {
		t.Fatalf("score = %v, want high (>=0.8) for a substantive answer", s)
	}
}

func TestShouldEscalateThresholdBoundary(t *testing.T) {
	if ShouldEscalate(0.5, 0.5) {
		t.Fatalf("ShouldEscalate(0.5, 0.5) = true, want false when score == tau")
	}
	if !ShouldEscalate(0.49, 0.5) {
		t.Fatalf("ShouldEscalate(0.49, 0.5) = false, want true when score < tau")
	}
	if ShouldEscalate(0.8, 0.5) {
		t.Fatalf("ShouldEscalate(0.8, 0.5) = true, want false when score > tau")
	}
}
