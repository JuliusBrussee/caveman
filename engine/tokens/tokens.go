// Package tokens counts tokens for the engine's compression ratios. Counts are
// local estimates; provider usage is authoritative downstream.
//
// The default counter is a real BPE tokenizer (OpenAI o200k_base) whose vocab is
// embedded in the binary, so counting is deterministic and fully offline. The
// Counter interface lets a per-provider tokenizer drop in later without touching
// any compressor.
package tokens

import (
	"sync"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

// Counter estimates the token length of a payload. Implementations must be
// deterministic: the same bytes always yield the same count.
type Counter interface {
	// Count returns a local token estimate for b.
	Count(b []byte) int
	// Name identifies the counting basis (e.g. the encoding name) for display
	// and cross-surface parity.
	Name() string
}

// bpeCounter wraps an offline tiktoken codec.
type bpeCounter struct {
	name  string
	codec tokenizer.Codec
}

// NewBPECounter builds a counter for a tiktoken encoding (vocab is embedded, so
// no network access occurs).
func NewBPECounter(enc tokenizer.Encoding) (Counter, error) {
	codec, err := tokenizer.Get(enc)
	if err != nil {
		return nil, err
	}
	return &bpeCounter{name: string(enc), codec: codec}, nil
}

func (c *bpeCounter) Name() string { return c.name }

func (c *bpeCounter) Count(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n, err := c.codec.Count(string(b))
	if err != nil {
		// A counting error never breaks compression; fall back to the
		// deterministic approximation rather than guessing or panicking.
		return approxTokens(b)
	}
	return n
}

// approxCounter is a deterministic byte/rune-based estimate used as a fallback
// and where a BPE codec is unnecessary. ~4 characters per token is the widely
// used rule of thumb for English-weighted text.
type approxCounter struct{}

// NewApproxCounter returns the deterministic ~chars/4 estimator.
func NewApproxCounter() Counter { return approxCounter{} }

func (approxCounter) Name() string { return "approx-chars/4" }

func (approxCounter) Count(b []byte) int { return approxTokens(b) }

func approxTokens(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	runes := utf8.RuneCount(b)
	n := runes / 4
	if n < 1 {
		n = 1
	}
	return n
}

var (
	defaultOnce sync.Once
	defaultCtr  Counter
)

// Default returns the engine's shared default counter: the o200k_base BPE
// tokenizer (the GPT-4o-family encoding), used across every content type. If
// the embedded codec cannot be loaded — a build-time
// invariant that should never fail at runtime — it degrades to the deterministic
// approximation so the engine never crashes.
func Default() Counter {
	defaultOnce.Do(func() {
		if c, err := NewBPECounter(tokenizer.O200kBase); err == nil {
			defaultCtr = c
		} else {
			defaultCtr = NewApproxCounter()
		}
	})
	return defaultCtr
}
