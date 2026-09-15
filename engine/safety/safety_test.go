package safety_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

func TestKnownClasses(t *testing.T) {
	for _, c := range []safety.Class{safety.S0, safety.S1, safety.S2, safety.S3, safety.S4} {
		info, ok := safety.Lookup(c)
		if !ok {
			t.Fatalf("class %v should be registered", c)
		}
		if !c.Valid() {
			t.Errorf("class %v should be valid", c)
		}
		if info.Class != c {
			t.Errorf("info.Class = %v, want %v", info.Class, c)
		}
	}
}

func TestS4IsLossyAndRequiresCCR(t *testing.T) {
	info, _ := safety.Lookup(safety.S4)
	if info.ByteSafe {
		t.Error("S4 must not be byte-safe")
	}
	if !info.RequiresCCR {
		t.Error("S4 must require CCR recovery")
	}
	if info.Reversible {
		t.Error("S4 is not reversible by class; method metadata may override for TOON")
	}
}

func TestByteSafeClasses(t *testing.T) {
	for _, c := range []safety.Class{safety.S0, safety.S1} {
		info, _ := safety.Lookup(c)
		if !info.ByteSafe {
			t.Errorf("%v should be byte-safe", c)
		}
		if info.RequiresCCR {
			t.Errorf("%v must not require CCR", c)
		}
	}
}

func TestReversibleClasses(t *testing.T) {
	for _, c := range []safety.Class{safety.S0, safety.S1, safety.S2} {
		info, _ := safety.Lookup(c)
		if !info.Reversible {
			t.Errorf("%v should be reversible by class", c)
		}
	}
	for _, c := range []safety.Class{safety.S3, safety.S4} {
		info, _ := safety.Lookup(c)
		if info.Reversible {
			t.Errorf("%v should not be reversible by class", c)
		}
	}
}

func TestUnknownClassFailsClosed(t *testing.T) {
	unknown := safety.Class(99)
	if _, ok := safety.Lookup(unknown); ok {
		t.Error("unknown class must not be found")
	}
	if unknown.Valid() {
		t.Error("unknown class must not be valid")
	}
	if unknown.String() != "S?" {
		t.Errorf("unknown class String() = %q, want S?", unknown.String())
	}
}
