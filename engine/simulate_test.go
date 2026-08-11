package engine_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/safety"
)

// fakeNonLossy is a byte-safe (S1, non-CCR) compressor used to exercise the
// Recoverable=true-by-class branch — no shipped compressor is non-lossy, so the
// only way to cover it is a synthetic one.
type fakeNonLossy struct{}

func (fakeNonLossy) ContentType() string               { return "fake" }
func (fakeNonLossy) SafetyClass() safety.Class         { return safety.S1 }
func (fakeNonLossy) Compress(in []byte) ([]byte, bool) { return []byte("x"), true }

// TestSimulateReportsReductionAndStoresNothing is the S13 acceptance: a unit dry-run
// on a fixture, no network, and — critically — no CCR write. It reports the token
// reduction a real Compress would achieve and leaves the recovery store empty.
func TestSimulateReportsReductionAndStoresNothing(t *testing.T) {
	e := newEngine(t) // backed by an in-memory CCR store

	sim := e.Simulate([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress})

	if sim.ContentType != engine.TypeJSON {
		t.Errorf("content type = %q, want json", sim.ContentType)
	}
	if sim.TokensSaved <= 0 || sim.TokensAfter >= sim.TokensBefore {
		t.Fatalf("expected a positive saving, got saved=%d (%d->%d)", sim.TokensSaved, sim.TokensBefore, sim.TokensAfter)
	}
	if sim.TokensSaved != sim.TokensBefore-sim.TokensAfter {
		t.Errorf("TokensSaved=%d inconsistent with before-after=%d", sim.TokensSaved, sim.TokensBefore-sim.TokensAfter)
	}
	if sim.TokenCountBasis == "" {
		t.Error("token_count_basis must disclose estimator")
	}
	if sim.Ratio <= 0 {
		t.Errorf("ratio = %v, want > 0", sim.Ratio)
	}
	if sim.SafetyClass != "S4" || !sim.Lossy {
		t.Errorf("json compressor is S4/lossy; got class=%q lossy=%v", sim.SafetyClass, sim.Lossy)
	}
	if sim.Method != "elision" {
		t.Errorf("method = %q, want elision", sim.Method)
	}
	if sim.LosslessToModel == nil || *sim.LosslessToModel {
		t.Errorf("lossless_to_model = %v, want explicit false", sim.LosslessToModel)
	}
	if !sim.Recoverable {
		t.Error("with a store present the saving is recoverable")
	}
	if sim.Basis != engine.BasisInferred {
		t.Errorf("basis = %q, want %q", sim.Basis, engine.BasisInferred)
	}

	// The defining property: Simulate performed NO CCR Put.
	stats, err := e.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Totals.Count != 0 {
		t.Errorf("Simulate must store nothing; CCR holds %d recoveries", stats.Totals.Count)
	}
}

// TestSimulateMatchesCompressAccounting checks that the dry-run matches Compress: its token
// accounting equals what a real Compress reports for the same input.
func TestSimulateMatchesCompressAccounting(t *testing.T) {
	e := newEngine(t)
	opts := engine.Options{Mode: engine.ModeCompress}

	sim := e.Simulate([]byte(arrayJSON), opts)
	res, err := e.Compress([]byte(arrayJSON), opts)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if sim.TokensBefore != res.TokensBefore || sim.TokensAfter != res.TokensAfter {
		t.Errorf("simulate (%d->%d) != compress (%d->%d)", sim.TokensBefore, sim.TokensAfter, res.TokensBefore, res.TokensAfter)
	}
	if sim.ContentType != res.ContentType || sim.Ratio != res.Ratio {
		t.Errorf("simulate (type=%q ratio=%v) != compress (type=%q ratio=%v)", sim.ContentType, sim.Ratio, res.ContentType, res.Ratio)
	}
}

// TestSimulateRecordAndPassThroughClaimNothing covers the pass-through conditions.
func TestSimulateRecordAndPassThroughClaimNothing(t *testing.T) {
	e := newEngine(t)

	// record mode never transforms.
	rec := e.Simulate([]byte(arrayJSON), engine.Options{Mode: engine.ModeRecord})
	if rec.TokensSaved != 0 || rec.Compressor != "" || rec.Ratio != 0 {
		t.Errorf("record mode must claim nothing: %+v", rec)
	}
	if !rec.Recoverable {
		t.Error("a pass-through is trivially recoverable")
	}

	// plain prose has no compressor → pass-through.
	txt := e.Simulate([]byte("just some ordinary prose with nothing to compress"), engine.Options{Mode: engine.ModeCompress})
	if txt.TokensSaved != 0 || txt.Compressor != "" {
		t.Errorf("plain text must claim nothing: %+v", txt)
	}
}

// TestSimulateLossyUnrecoverableWithoutStore is the honesty case: a lossy (S4)
// compressor with NO store. A real Compress fails closed to pass-through (zero
// reduction), but Simulate surfaces the would-be reduction as requiring CCR
// potential flagged Recoverable=false — never silently counted as achievable today.
func TestSimulateLossyUnrecoverableWithoutStore(t *testing.T) {
	e := engine.New(nil, nil) // no store

	sim := e.Simulate([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress})
	if sim.TokensSaved <= 0 {
		t.Fatalf("Simulate should surface the would-be saving even without a store, got %d", sim.TokensSaved)
	}
	if !sim.Lossy {
		t.Error("json is S4/lossy")
	}
	if sim.Recoverable {
		t.Error("without a store a lossy saving is NOT recoverable; must be flagged false")
	}

	// Contrast: a real Compress with no store passes through and claims nothing.
	res, err := e.Compress([]byte(arrayJSON), engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if !res.PassedThrough() || res.Ratio != 0 {
		t.Errorf("Compress with no store must pass through (claim nothing); got ratio=%v handle=%q", res.Ratio, res.RecoveryHandle)
	}
}

// TestSimulateNonLossyRecoverableWithoutStore exercises the byte-safe branch: a
// non-lossy (S1) compressor's reduction is Recoverable even with NO store, because it
// needs no CCR. (Forces Options.Type since Detect never routes to "fake".)
func TestSimulateNonLossyRecoverableWithoutStore(t *testing.T) {
	reg := compressors.NewRegistry()
	reg.Register(fakeNonLossy{})
	e := engine.NewWithRegistry(nil, nil, reg) // no store

	sim := e.Simulate([]byte("a reasonably long input the fake compressor shrinks to one token"),
		engine.Options{Mode: engine.ModeCompress, Type: "fake"})

	if sim.TokensSaved <= 0 {
		t.Fatalf("expected a saving from the non-lossy compressor, got %d", sim.TokensSaved)
	}
	if sim.Lossy {
		t.Error("an S1 compressor is not lossy")
	}
	if !sim.Recoverable {
		t.Error("a byte-safe (non-CCR) saving is recoverable even with no store")
	}
	if sim.SafetyClass != "S1" {
		t.Errorf("safety class = %q, want S1", sim.SafetyClass)
	}
	if sim.Method != "fake" {
		t.Errorf("method = %q, want fake", sim.Method)
	}
	if sim.LosslessToModel == nil || !*sim.LosslessToModel {
		t.Errorf("lossless_to_model = %v, want true for S1", sim.LosslessToModel)
	}
}

func TestSimulateBestOfTOONIsLosslessToModelButNeedsCCRForByteExactRecovery(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")
	counter := bestOfCounter{inputTokens: 100, toonTokens: 10, elisionTokens: 20}
	e := engine.New(nil, counter)

	sim := e.Simulate([]byte(bestOfJSONInput), engine.Options{Mode: engine.ModeCompress})
	if sim.Method != "toon" {
		t.Fatalf("method = %q, want toon", sim.Method)
	}
	if sim.Lossy {
		t.Fatal("TOON is lossless-to-model and must not be marked lossy")
	}
	if sim.LosslessToModel == nil || !*sim.LosslessToModel {
		t.Fatalf("lossless_to_model = %v, want true", sim.LosslessToModel)
	}
	if !sim.RequiresCCR {
		t.Fatal("TOON is S4 in v1 and still requires CCR for byte-exact recovery")
	}
	if sim.Recoverable {
		t.Fatal("without a store, byte-exact recovery must be false")
	}
}

func TestSimulateBestOfElisionRemainsLossy(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")
	counter := bestOfCounter{inputTokens: 100, toonTokens: 20, elisionTokens: 10}
	e := engine.New(nil, counter)

	sim := e.Simulate([]byte(bestOfJSONInput), engine.Options{Mode: engine.ModeCompress})
	if sim.Method != "elision" {
		t.Fatalf("method = %q, want elision", sim.Method)
	}
	if !sim.Lossy {
		t.Fatal("elision drops data and must be marked lossy")
	}
	if sim.LosslessToModel == nil || *sim.LosslessToModel {
		t.Fatalf("lossless_to_model = %v, want false", sim.LosslessToModel)
	}
	if !sim.RequiresCCR {
		t.Fatal("elision requires CCR")
	}
}
