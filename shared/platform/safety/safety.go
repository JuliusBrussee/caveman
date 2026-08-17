// Package safety defines the optimization safety ladder (S0–S3), the single
// source of truth shared by the worker's detectors
// and the control-api's Cave Architect. The string values are the wire/DB
// representation of `opportunities.safety_class`; do not change them.
//
// The ladder, from least to most invasive:
//
//	S0  byte-safe behavior        — never alters model-visible bytes (default on)
//	S1  provider-native hints     — caching/routing hints; byte-safe, zero app change
//	S2  structural changes        — need SDK/app cooperation (tool deferral, paging…)
//	S3  behavioral changes        — model routing / reasoning effort; need an eval gate
package safety

// Class is a safety-ladder class. It is a string alias so the constants remain
// assignable to the plain `string` fields on Opportunity/OppInput and stay
// byte-compatible with the persisted `safety_class` column.
type Class = string

const (
	ByteSafe       Class = "S0_BYTE_SAFE"
	ProviderNative Class = "S1_PROVIDER_NATIVE"
	Structural     Class = "S2_STRUCTURAL"
	Behavioral     Class = "S3_BEHAVIORAL"
)

// Order returns the ladder position (0–3) for deterministic ordering. An
// unrecognized class fails closed: it sorts last rather than being silently
// grouped with a known tier.
func Order(c Class) int {
	switch c {
	case ByteSafe:
		return 0
	case ProviderNative:
		return 1
	case Structural:
		return 2
	case Behavioral:
		return 3
	default:
		return 99
	}
}

// RequiresEvalGate reports whether promoting an optimizer of this class needs an
// eval gate before it can touch traffic. Byte-safe (S0) and provider-native (S1)
// hints do not change model-visible bytes and so do not; structural (S2) and
// behavioral (S3) changes do. An unrecognized class fails closed (requires one).
func RequiresEvalGate(c Class) bool {
	return c != ByteSafe && c != ProviderNative
}

// Short normalizes a safety class to its short form ("S0".."S3"), accepting
// either the long DB/wire form the detectors persist (e.g. "S2_STRUCTURAL") or an
// already-short value. It is the bridge between the detectors' long-form
// safety_class and surfaces (proposals) whose CHECK constraint stores the short
// form. An unrecognized class fails closed to "" — the caller must reject it, not
// coerce it to a plausible tier.
func Short(class string) string {
	switch class {
	case ByteSafe, "S0":
		return "S0"
	case ProviderNative, "S1":
		return "S1"
	case Structural, "S2":
		return "S2"
	case Behavioral, "S3":
		return "S3"
	default:
		return ""
	}
}
