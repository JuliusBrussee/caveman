package safety

import "testing"

func TestOrder_LadderAndFailClosed(t *testing.T) {
	if !(Order(ByteSafe) < Order(ProviderNative) &&
		Order(ProviderNative) < Order(Structural) &&
		Order(Structural) < Order(Behavioral)) {
		t.Errorf("ladder must sort S0<S1<S2<S3, got %d %d %d %d",
			Order(ByteSafe), Order(ProviderNative), Order(Structural), Order(Behavioral))
	}
	// An unrecognized class fails closed: it sorts AFTER every known tier.
	if Order("S9_BOGUS") <= Order(Behavioral) {
		t.Errorf("unknown class must sort last, got %d", Order("S9_BOGUS"))
	}
}

func TestShort_NormalizesLongAndShortFailsClosed(t *testing.T) {
	cases := map[Class]string{
		ByteSafe:       "S0",
		ProviderNative: "S1",
		Structural:     "S2",
		Behavioral:     "S3",
		"S0":           "S0",
		"S1":           "S1",
		"S2":           "S2",
		"S3":           "S3",
		"S9_BOGUS":     "", // unknown fails closed to empty
		"":             "",
	}
	for c, want := range cases {
		if got := Short(c); got != want {
			t.Errorf("Short(%q) = %q, want %q", c, got, want)
		}
	}
}

func TestRequiresEvalGate(t *testing.T) {
	cases := map[Class]bool{
		ByteSafe:       false,
		ProviderNative: false,
		Structural:     true,
		Behavioral:     true,
		"S9_BOGUS":     true, // unknown fails closed → requires a gate
	}
	for c, want := range cases {
		if got := RequiresEvalGate(c); got != want {
			t.Errorf("RequiresEvalGate(%q) = %v, want %v", c, got, want)
		}
	}
}
