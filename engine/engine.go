// Package engine is the Caveman Engine's core API. It detects what
// kind of content a payload is, routes it to a safety-classed compressor, counts the
// token reduction, and records the original for recovery — keeping what answers
// depend on and dropping the noise, before the bytes ever reach a model.
//
// The four-call surface (Compress, Retrieve, Detect, Stats) is the stable
// contract the proxy, SDKs, CLI, MCP server, and browser build all share.
// Simulate is an additional network-free dry run that reports what compression
// would do without changing stored state.
package engine

import (
	"bytes"
	"errors"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/safety"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

// Engine is the compression core. Construct it with New.
type Engine struct {
	registry *compressors.Registry
	counter  tokens.Counter
	store    *ccr.Store
}

// New builds an engine with the default compressor registry. store backs CCR
// recovery and stats and may be nil — without it the engine still detects and
// pass-through-compresses, but it will never run a lossy compressor, because a
// lossy result with no recovery would violate the reversibility contract.
// counter may be nil, in which case the default offline BPE counter is used.
func New(store *ccr.Store, counter tokens.Counter) *Engine {
	if counter == nil {
		counter = tokens.Default()
	}
	return &Engine{registry: defaultRegistry(counter), counter: counter, store: store}
}

// NewWithRegistry is New with a caller-supplied registry (for tests or embedders
// that register a custom compressor set).
func NewWithRegistry(store *ccr.Store, counter tokens.Counter, registry *compressors.Registry) *Engine {
	if counter == nil {
		counter = tokens.Default()
	}
	if registry == nil {
		registry = defaultRegistry(counter)
	}
	return &Engine{registry: registry, counter: counter, store: store}
}

func defaultRegistry(counter tokens.Counter) *compressors.Registry {
	registry := compressors.Default()
	if strings.EqualFold(os.Getenv("CAVE_ENGINE_TOON"), "best-of") {
		registry.Register(compressors.NewJSONStrategy(counter))
	}
	return registry
}

// Compress detects the content type, routes to the matching compressor, and
// returns the compressed result. It is fail-closed: in record mode, when no
// compressor matches, when the compressor reports a parse problem, when the
// result is not smaller, or when a lossy result cannot be made recoverable, it
// passes the original bytes through unchanged (no handle, zero ratio).
func (e *Engine) Compress(input []byte, opts Options) (Result, error) {
	body, listing := unwrapListing(input)
	ct := opts.Type
	if ct == "" {
		ct = e.Detect(body)
	}
	before := e.counter.Count(input)
	res := Result{
		Output:          input,
		ContentType:     ct,
		TokensBefore:    before,
		TokensAfter:     before,
		TokenCountBasis: e.counter.Name(),
		Basis:           BasisInferred,
	}

	// record mode never transforms.
	if opts.Mode.normalized() == ModeRecord {
		return res, nil
	}

	comp, ok := e.registry.For(ct)
	if !ok {
		return res, nil // no compressor for this type → pass-through
	}

	// A lossy (S4) compressor may only run if we can store the original for
	// recovery. Without a store, fail closed to pass-through.
	info, known := safety.Lookup(comp.SafetyClass())
	if !known {
		return res, nil // unknown safety class → fail closed
	}
	if info.RequiresCCR && e.store == nil && !opts.ExternalRecovery {
		return res, nil
	}

	out, meta, ok := compressWith(comp, body, opts.Query)
	if !ok || out == nil {
		return res, nil // parse problem → pass-through
	}
	out = listing.rewrap(out)
	after := e.counter.Count(out)
	if after >= before || bytes.Equal(out, input) {
		return res, nil // not actually smaller → pass-through, claim nothing
	}

	meta = normalizeMetadata(comp, meta, info)

	if info.RequiresCCR && !opts.ExternalRecovery {
		handle, err := e.store.Put(ccr.Recovery{
			ContentType:  ct,
			Compressor:   comp.ContentType(),
			TokensBefore: before,
			TokensAfter:  after,
			Original:     append([]byte(nil), input...),
			Metadata:     append([]byte(nil), meta.RecoveryMetadata...),
		})
		if err != nil {
			// Keep every result field pass-through on a failed recovery write. A
			// caller must never receive transformed bytes without a durable handle.
			return res, err
		}
		res.RecoveryHandle = handle
	}
	// Publish transformed metadata only after CCR has succeeded. This makes the
	// result fail closed if persistence is unavailable.
	res.Output = out
	res.TokensAfter = after
	res.Ratio = float64(before-after) / float64(before)
	res.Method = meta.Method
	res.LosslessToModel = meta.LosslessToModel
	return res, nil
}

// Simulate runs the same detect → route → compress → count pipeline as Compress
// but stores NOTHING (no CCR Put) and makes no network call: a pure dry-run that
// reports the token reduction a real Compress would achieve as a local estimate.
//
// It mirrors every pass-through condition Compress has (record mode, no matching
// compressor, unknown safety class, parse problem, not-actually-smaller) and claims
// nothing in those cases. The one deliberate difference: for a lossy (S4) compressor
// with no store, Compress fails closed to pass-through, but Simulate still reports
// the would-be reduction with Recoverable=false — showing that CCR is required
// before the transform can be emitted. The caller buckets by
// SafetyClass/Recoverable; the number is a token count of compressor output.
func (e *Engine) Simulate(input []byte, opts Options) SimResult {
	body, listing := unwrapListing(input)
	ct := opts.Type
	if ct == "" {
		ct = e.Detect(body)
	}
	before := e.counter.Count(input)
	res := SimResult{
		ContentType:     ct,
		TokensBefore:    before,
		TokensAfter:     before,
		TokenCountBasis: e.counter.Name(),
		Basis:           BasisInferred,
		Recoverable:     true, // pass-through is trivially recoverable (no transform)
	}

	if opts.Mode.normalized() == ModeRecord {
		return res // record mode never transforms
	}
	comp, ok := e.registry.For(ct)
	if !ok {
		return res // no compressor for this type → pass-through, claim nothing
	}
	info, known := safety.Lookup(comp.SafetyClass())
	if !known {
		return res // unknown safety class → fail closed, claim nothing
	}
	out, meta, ok := compressWith(comp, body, opts.Query)
	if !ok || out == nil {
		return res // parse problem → pass-through
	}
	out = listing.rewrap(out)
	after := e.counter.Count(out)
	if after >= before || bytes.Equal(out, input) {
		return res // not actually smaller → claim nothing
	}

	res.Compressor = comp.ContentType()
	res.SafetyClass = info.Name
	res.TokensAfter = after
	res.TokensSaved = before - after
	res.Ratio = float64(before-after) / float64(before)
	res.Recoverable = !info.RequiresCCR || e.store != nil
	meta = normalizeMetadata(comp, meta, info)
	res.Method = meta.Method
	res.LosslessToModel = meta.LosslessToModel
	res.RequiresCCR = info.RequiresCCR
	res.Lossy = meta.LosslessToModel != nil && !*meta.LosslessToModel
	return res
}

// compressWith dispatches to a compressor's query-aware path when a query is
// set and the compressor implements QueryAwareCompressor, else the plain byte
// transform. It is the single point that keeps Compress and Simulate in sync.
func compressWith(comp compressors.Compressor, input []byte, query string) ([]byte, compressors.Metadata, bool) {
	if mc, ok := comp.(compressors.MetadataCompressor); ok {
		return mc.CompressWithMetadata(input, query)
	}
	if query != "" {
		if qa, ok := comp.(compressors.QueryAwareCompressor); ok {
			out, ok := qa.CompressQuery(input, query)
			return out, compressors.Metadata{}, ok
		}
	}
	out, ok := comp.Compress(input)
	return out, compressors.Metadata{}, ok
}

func normalizeMetadata(comp compressors.Compressor, meta compressors.Metadata, info safety.Info) compressors.Metadata {
	if meta.Method == "" {
		meta.Method = comp.ContentType()
	}
	if meta.LosslessToModel == nil {
		meta.LosslessToModel = boolPtr(info.Reversible)
	}
	return meta
}

func boolPtr(v bool) *bool {
	return &v
}

// Retrieve returns the exact original bytes for a recovery handle.
//
// The CCR store holds two id spaces, and an agent cannot be expected to know
// which one minted the reference it was handed. Compression mints blob handles
// (`ccr_…`, store.Get); the native runtime's tool-output masking mints typed
// OBJECT ids and shows them as `ccr://<id>` (store.GetObject). So a lookup that
// misses the blob table falls through to the object table before it fails.
//
// Recovery must resolve every reference the runtime can emit. A handle that
// cannot resolve invites repeated retrieval attempts, especially when the failed
// retrieval result is itself eligible for masking. Resolve both blob and typed
// object handles before failing so recovery never points to another dead pointer.
func (e *Engine) Retrieve(handle string) ([]byte, error) {
	if e.store == nil {
		return nil, ccr.ErrNotFound
	}
	original, err := e.store.Get(handle)
	if err == nil {
		return original, nil
	}
	if !errors.Is(err, ccr.ErrNotFound) {
		return nil, err
	}
	object, objectErr := e.store.GetObject(handle)
	if objectErr == nil {
		return object.Data, nil
	}
	if !errors.Is(objectErr, ccr.ErrNotFound) {
		return nil, objectErr
	}
	return nil, err
}

// RetrieveMetadata returns optional compressor metadata stored beside a recovery
// handle. The raw Retrieve path remains byte-exact and never returns this wrapper
// data.
func (e *Engine) RetrieveMetadata(handle string) ([]byte, error) {
	if e.store == nil {
		return nil, ccr.ErrNotFound
	}
	return e.store.GetMetadata(handle)
}

// Stats returns aggregate compression accounting from the recovery store.
func (e *Engine) Stats() (ccr.Stats, error) {
	if e.store == nil {
		return ccr.Stats{ByContentType: map[string]ccr.Bucket{}, Basis: BasisInferred}, nil
	}
	return e.store.Summary()
}
