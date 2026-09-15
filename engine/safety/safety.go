// Package safety is the engine's S0–S4 safety-class registry. Every compressor
// declares its class; the class is inherent to the compression method, not a
// user choice. The registry is the single place that answers two honesty
// questions about a class: does it change model-visible bytes, and does it
// require a recoverable record (CCR) before it may run.
//
// It is a leaf package (no engine dependency) so both the engine core and the
// compressors can import it without an import cycle.
package safety

// Class is a position on the engine's safety ladder.
type Class int

const (
	// S0 — byte-safe behavior (metadata, accounting). No model-visible bytes change.
	S0 Class = iota
	// S1 — provider-native hints (cache, routing). No model-visible bytes change.
	S1
	// S2 — structural changes that need SDK cooperation.
	S2
	// S3 — behavioral changes (routing, reasoning); eval-gated in Cloud.
	S3
	// S4 — lossy structural compression. Alters model-visible bytes, so it is
	// opt-in, must be reversible (CCR), and discloses what it dropped.
	S4
)

// Info describes the honesty contract of a safety class.
type Info struct {
	Class Class
	Name  string
	// ByteSafe is true when the class never alters model-visible bytes.
	ByteSafe bool
	// RequiresCCR is true when the class is lossy and may only run if the
	// original bytes are first stored for recovery.
	RequiresCCR bool
	// Reversible is true when the model-visible output retains the full value
	// without data dropped. S4 defaults false; method metadata can override for
	// lossless-to-model S4 transforms such as TOON.
	Reversible bool
}

var registry = map[Class]Info{
	S0: {Class: S0, Name: "S0", ByteSafe: true, RequiresCCR: false, Reversible: true},
	S1: {Class: S1, Name: "S1", ByteSafe: true, RequiresCCR: false, Reversible: true},
	S2: {Class: S2, Name: "S2", ByteSafe: false, RequiresCCR: false, Reversible: true},
	S3: {Class: S3, Name: "S3", ByteSafe: false, RequiresCCR: false, Reversible: false},
	S4: {Class: S4, Name: "S4", ByteSafe: false, RequiresCCR: true, Reversible: false},
}

// Lookup returns the Info for a class. The second return is false for an
// unknown class — callers fail closed (treat an unknown class as unsafe to run).
func Lookup(c Class) (Info, bool) {
	info, ok := registry[c]
	return info, ok
}

// Valid reports whether c is a registered class.
func (c Class) Valid() bool {
	_, ok := registry[c]
	return ok
}

// String returns the class name, or "S?" for an unknown class.
func (c Class) String() string {
	if info, ok := registry[c]; ok {
		return info.Name
	}
	return "S?"
}
