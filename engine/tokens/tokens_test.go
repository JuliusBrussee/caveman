package tokens_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

func TestDefaultCounterCountsAndIsDeterministic(t *testing.T) {
	c := tokens.Default()
	text := []byte("the quick brown fox jumps over the lazy dog")
	n1 := c.Count(text)
	n2 := c.Count(text)
	if n1 <= 0 {
		t.Fatalf("expected a positive token count, got %d", n1)
	}
	if n1 != n2 {
		t.Errorf("counting must be deterministic: %d != %d", n1, n2)
	}
}

func TestEmptyIsZero(t *testing.T) {
	if n := tokens.Default().Count(nil); n != 0 {
		t.Errorf("empty input must count 0, got %d", n)
	}
}

func TestApproxCounter(t *testing.T) {
	c := tokens.NewApproxCounter()
	if got := c.Count([]byte("abcdefgh")); got != 2 { // 8 runes / 4
		t.Errorf("approx count = %d, want 2", got)
	}
	if got := c.Count([]byte("x")); got != 1 {
		t.Errorf("non-empty must count at least 1, got %d", got)
	}
}

func TestLongerTextCountsMore(t *testing.T) {
	c := tokens.Default()
	short := c.Count([]byte("hello"))
	long := c.Count([]byte("hello hello hello hello hello hello hello hello"))
	if long <= short {
		t.Errorf("longer text should count more tokens: long=%d short=%d", long, short)
	}
}
