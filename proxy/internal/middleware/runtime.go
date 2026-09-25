package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/tokens"
	ident "github.com/JuliusBrussee/caveman/proxy/internal/identity"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type Config struct {
	// Store is SQLite (*store.Store) or Postgres (*store.PostgresMiddleware).
	Store store.MiddlewareStore
	// Recovery is the process-global CCR. Originals now live in Store, owned by
	// their scope; CCR is only read, for grants an older runtime issued. May be nil.
	Recovery *ccr.Store
	// Identify resolves the caller from its credential only, never from a client
	// namespace, framework run ID, or a tenant field in request JSON. Its
	// Principal names who owns the data, which namespaces it may use (checked on
	// every scoped route) and its quota overrides.
	Identify               func(*http.Request) (ident.Principal, error)
	Build, Mode, TrustMode string
	// Ephemeral declares that the store does not survive a restart (an emptyDir,
	// a container without a volume) whatever the backend says, so capabilities
	// report persistent:false and nothing is compressed that could not be
	// recovered after the restart.
	Ephemeral bool
	// Retention slides with use. MaxRetention caps the slide, counted from a
	// scope's creation (§12); it is raised to Retention when smaller.
	Retention, MaxRetention time.Duration
	Limits                  Limits
	// Capacity bounds the middleware store in total and per principal. A
	// principal at its quota gets capacity; other principals are unaffected.
	// With more than one principal (TrustMode resolver), a per-principal cap
	// left at 0 defaults to a quarter of the global one, so no single principal
	// can fill the store for everyone; a principal's own quota overrides it.
	Capacity store.MiddlewareLimits
	// PrincipalInFlight bounds the slots one principal may hold in each request
	// queue; it waits for its own slots before taking a shared one. 0 is half
	// of each queue with more than one principal, else no bound. Server-side
	// only: capabilities limits carry no such key (§13).
	PrincipalInFlight int
	// Keys seals originals at rest. Nil stores them in plaintext. Under a key,
	// originals stored in plaintext are refused unless PlaintextOriginals is set,
	// for the window while a store written before encryption expires.
	Keys               *Keyring
	PlaintextOriginals bool
	// Logger receives one audit line per request (never content) and storage
	// warnings. Nil disables both.
	Logger *slog.Logger
	Now    func() time.Time
}

type Runtime struct {
	cfg     Config
	eng     *engine.Engine
	counter tokens.Counter
	// caps is the protocol 1.1 capabilities view; legacyCaps is the exact view
	// a request without the features header gets (§3).
	caps, legacyCaps             Capabilities
	transforms, legacyTransforms map[string]compressors.Capability
	queue, retrieveQueue         chan struct{}
	// fair and fairRetrieve are each principal's share of queue and retrieveQueue.
	fair, fairRetrieve perPrincipal
	quota              rateQuota
	metrics            metrics

	renewMu  sync.Mutex
	renewals map[string]struct{}

	// sweepBudget bounds one sweep; backlog is what the last one left behind.
	sweepBudget time.Duration
	backlog     atomic.Int64

	readyMu  sync.Mutex
	readyAt  time.Time
	readyErr error
}

// serverFeatures are the negotiable 1.1 features, sorted (§3).
var serverFeatures = []string{FeatureHTTPStatusV2, FeatureOriginalsLifecycle, FeatureRevisionTolerant, FeatureTolerantReader}

func New(cfg Config) (*Runtime, error) {
	if cfg.Store == nil || cfg.Identify == nil {
		return nil, Failure{"configuration"}
	}
	if cfg.Mode != "compress" && cfg.Mode != "record" {
		cfg.Mode = "record"
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.MaxRetention <= 0 {
		cfg.MaxRetention = 7 * 24 * time.Hour
	}
	cfg.MaxRetention = max(cfg.MaxRetention, cfg.Retention)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Build == "" {
		cfg.Build = "development"
	}
	if cfg.TrustMode == "" {
		cfg.TrustMode = "resolver"
	}
	l := &cfg.Limits
	if l.DeadlineMS <= 0 {
		// 100ms covered the queue plus Engine work only on an idle machine; the
		// shared budget is what clients see as a bypass under any real load.
		l.DeadlineMS = DefaultDeadlineMS
	}
	if l.RetrieveDeadlineMS <= 0 {
		l.RetrieveDeadlineMS = DefaultRetrieveDeadlineMS
	}
	if l.RequestBytes <= 0 {
		l.RequestBytes = DefaultRequestBytes
	}
	if l.ReceiptBytes <= 0 {
		l.ReceiptBytes = DefaultReceiptBytes
	}
	if l.QueueDepth <= 0 {
		l.QueueDepth = DefaultQueueDepth
	}
	if l.RetrieveQueueDepth <= 0 {
		l.RetrieveQueueDepth = DefaultRetrieveQueueDepth
	}
	l.QuotaRequestsPerMinute = max(l.QuotaRequestsPerMinute, 0)
	if cfg.Capacity.Rows <= 0 {
		cfg.Capacity.Rows = store.DefaultMiddlewareRows
	}
	if cfg.Capacity.Bytes <= 0 {
		cfg.Capacity.Bytes = store.DefaultMiddlewareBytes
	}
	multi := cfg.TrustMode != "single_operator"
	if multi {
		if cfg.Capacity.PrincipalRows <= 0 {
			cfg.Capacity.PrincipalRows = cfg.Capacity.Rows / 4
		}
		if cfg.Capacity.PrincipalBytes <= 0 {
			cfg.Capacity.PrincipalBytes = cfg.Capacity.Bytes / 4
		}
	}
	share := func(depth int) int {
		if cfg.PrincipalInFlight > 0 {
			return min(cfg.PrincipalInFlight, depth)
		}
		if multi {
			return max(depth/2, 1)
		}
		return 0
	}
	if cfg.Keys != nil {
		keys := *cfg.Keys
		keys.plaintext = cfg.PlaintextOriginals
		cfg.Keys = &keys
	}
	// The contract schemas bound these, so configuration may only lower them.
	for _, limit := range []struct {
		value   *int
		ceiling int
	}{{&l.SegmentBytes, DefaultSegmentBytes}, {&l.PageBytes, DefaultPageBytes}, {&l.MaxSegments, DefaultMaxSegments}, {&l.MaxManifestItems, DefaultMaxManifestItems}} {
		if *limit.value <= 0 || *limit.value > limit.ceiling {
			*limit.value = limit.ceiling
		}
	}
	if err := cfg.Store.InitMiddleware(context.Background()); err != nil {
		return nil, err
	}
	counter := newMemoCounter(tokens.Default())
	r := &Runtime{cfg: cfg, eng: engine.New(cfg.Recovery, counter), counter: counter,
		transforms: map[string]compressors.Capability{}, legacyTransforms: map[string]compressors.Capability{},
		queue: make(chan struct{}, l.QueueDepth), retrieveQueue: make(chan struct{}, l.RetrieveQueueDepth),
		fair: perPrincipal{limit: share(l.QueueDepth)}, fairRetrieve: perPrincipal{limit: share(l.RetrieveQueueDepth)},
		quota: rateQuota{limit: l.QuotaRequestsPerMinute}, renewals: map[string]struct{}{}, sweepBudget: defaultSweepBudget}
	caps := r.eng.Capabilities()
	legacy := []compressors.Capability{}
	for _, cap := range caps {
		r.transforms[cap.TransformID] = cap
		// SDK 1.1.0 rejects a whole document over one transform it cannot use.
		if cap.Deterministic && (cap.Recovery == "exact_ccr" || cap.Recovery == "none") {
			r.legacyTransforms[cap.TransformID] = cap
			legacy = append(legacy, cap)
		}
	}
	r.caps = Capabilities{SchemaVersion: ProtocolVersion, RuntimeBuild: cfg.Build, PolicyRevision: policyRevision(caps), Transforms: caps,
		Limits: cfg.Limits, Persistent: cfg.Store.Persistent() && !cfg.Ephemeral, Recovery: true,
		RetentionSeconds: int64(cfg.Retention.Seconds()), TrustMode: cfg.TrustMode, Mode: cfg.Mode,
		Protocol: &ProtocolRange{ProtocolMin, ProtocolMax}, Features: serverFeatures, MaxRetentionSeconds: int64(cfg.MaxRetention.Seconds())}
	r.legacyCaps = r.caps
	r.legacyCaps.Transforms, r.legacyCaps.PolicyRevision = legacy, policyRevision(legacy)
	r.legacyCaps.Limits = Limits{DeadlineMS: l.DeadlineMS, RequestBytes: l.RequestBytes, SegmentBytes: l.SegmentBytes, PageBytes: l.PageBytes}
	r.legacyCaps.Protocol, r.legacyCaps.Features, r.legacyCaps.MaxRetentionSeconds = nil, nil, 0
	r.reportKeys(context.Background())
	return r, nil
}

// reportKeys logs, once at startup, the stored originals this runtime cannot
// open: sealed with a key it does not have, or plaintext under a keyring. Their
// grants answer recovery_unavailable and their choices are not reused.
func (r *Runtime) reportKeys(ctx context.Context) {
	if r.cfg.Logger == nil {
		return
	}
	keys, err := r.cfg.Store.MiddlewareKeys(ctx)
	if err != nil {
		r.warn("middleware key inventory failed", err)
		return
	}
	for _, id := range slices.Sorted(maps.Keys(keys)) {
		switch {
		case r.cfg.Keys.usable(id):
		case id == "":
			r.cfg.Logger.Warn("middleware originals stored in plaintext are refused while a key is configured; set allow_plaintext_originals until they expire", "originals", keys[id])
		default:
			r.cfg.Logger.Warn("middleware originals sealed with an unavailable key cannot be recovered", "key_id", id, "originals", keys[id])
		}
	}
}

func policyRevision(caps []compressors.Capability) string {
	b, _ := json.Marshal(caps)
	return "middleware-v1:" + digest(b)
}

// view is the capabilities document and transform set a request negotiated.
func (r *Runtime) view(n negotiated) (Capabilities, map[string]compressors.Capability) {
	if n.client {
		return r.caps, r.transforms
	}
	return r.legacyCaps, r.legacyTransforms
}

func digest(b []byte) string          { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func identity(parts ...string) string { b, _ := json.Marshal(parts); return digest(b) }
func authority(principal string, s Scope) string {
	return identity(principal, s.Namespace, s.SessionID, s.BranchID, s.CacheEpoch)
}

// expiry slides a scope to now+retention, capped at created+max retention.
func (r *Runtime) expiry(now, created int64) int64 {
	expires := now + int64(r.cfg.Retention.Seconds())
	if created > 0 {
		expires = min(expires, created+int64(r.cfg.MaxRetention.Seconds()))
	}
	return expires
}

// write runs fn in a write transaction that stamps and bounds rows by
// principal; the principal's own quota overrides the runtime-wide one.
func (r *Runtime) write(ctx context.Context, principal ident.Principal, fn func(*store.MiddlewareTx) error) error {
	limits := r.cfg.Capacity
	if principal.Quota.Rows > 0 {
		limits.PrincipalRows = principal.Quota.Rows
	}
	if principal.Quota.Bytes > 0 {
		limits.PrincipalBytes = principal.Quota.Bytes
	}
	return r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		tx.Principal, tx.Limits, tx.Usable = principal.Name, limits, r.cfg.Keys.usable
		return fn(tx)
	})
}

// choiceKey identifies a segment's persisted choice within its scope.
func choiceKey(s Segment) string { return identity(s.ID, s.SourceID, s.SHA256) }

// held reports whether the authority holds an original for digest that this
// runtime can open; one sealed with a key it lacks is as good as absent.
func (r *Runtime) held(owned map[string]store.StoredOriginal, digest string) bool {
	o, ok := owned[digest]
	return ok && o.Body && r.cfg.Keys.usable(o.KeyID)
}

func (r *Runtime) optimize(ctx context.Context, principal ident.Principal, req OptimizeRequest, inputDigest string, n negotiated) (OptimizeResponse, error) {
	if err := r.validate(req, n); err != nil {
		return OptimizeResponse{}, err
	}
	caps, _ := r.view(n)
	auth := authority(principal.Name, req.Scope)
	// Scope identity excludes the policy revision and transform list (§2), so
	// persisted choices, and the provider-cached bytes they produced, survive a
	// runtime upgrade. Reuse is gated per choice on policy.transforms instead.
	scopeID := identity(auth, req.Adapter.ID, req.Adapter.SerializationRevision)
	manifest, _ := json.Marshal(req.ContextManifest)
	response := OptimizeResponse{SchemaVersion: ProtocolVersion, RequestID: req.RequestID, InputDigest: inputDigest,
		RuntimeBuild: r.cfg.Build, PolicyRevision: caps.PolicyRevision, Status: "bypassed", Reason: "no_candidate",
		Replacements: []Replacement{}, Skipped: []Skip{},
		Measurement: Measurement{Basis: "inferred", Tokenizer: r.counter.Name(), Scope: "segment", OverheadCoverage: "segment_and_declared_recovery_tool"},
		Stability:   Stability{Native: "persistent_choices", ProviderBytes: "unobserved", ProviderCacheHits: "unobserved"}}
	if req.RecoveryBinding != nil {
		response.Recovery.BindingID = req.RecoveryBinding.ID
		response.Measurement.RecoveryOverheadTokens = r.counter.Count([]byte(req.RecoveryBinding.OverheadText))
	}
	response.Recovery.Persistent = r.caps.Persistent
	base := response
	now := r.cfg.Now().Unix()
	prepared := make([]preparedChoice, len(req.Segments))
	var keys []string
	for i, s := range req.Segments {
		if prepared[i].reason = r.screen(req, s); prepared[i].reason == "" {
			keys = append(keys, choiceKey(s))
		}
	}
	// Read first, then perform Engine work without holding the metadata writer.
	// One snapshot answers scope, plan, choices and their originals in a
	// constant number of round trips, whatever the segment count. The final
	// transaction rechecks them; a competing process's first published bytes
	// always win. An exact replay needs no write at all: its renewal joins the
	// batched ones.
	var replay *OptimizeResponse
	var created int64
	var chosen map[string]store.MiddlewareChoice
	var owned map[string]store.StoredOriginal
	err := r.cfg.Store.ReadMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		var err error
		if replay, created, _, err = previousPlan(tx, scopeID, auth, req, inputDigest, now, n.client); err != nil || replay != nil {
			return err
		}
		if chosen, err = tx.Choices(scopeID, keys); err != nil {
			return err
		}
		owned, err = tx.Originals(auth, ownedDigests(req, chosen))
		return err
	})
	if err == nil && replay != nil {
		replay.PolicyRevision = caps.PolicyRevision
		replay.Recovery.ExpiresAt = r.expiry(now, created)
		r.renew(auth)
		return *replay, nil
	}
	for i := 0; err == nil && i < len(req.Segments); i++ {
		if err = ctx.Err(); err == nil {
			prepared[i], err = r.prepareChoice(ctx, auth, req, req.Segments[i], prepared[i].reason, chosen, owned)
		}
	}
	rebaselined := false
	if err == nil {
		err = r.write(ctx, principal, func(tx *store.MiddlewareTx) error {
			var err error
			rebaselined, err = r.publish(ctx, tx, auth, scopeID, manifest, req, inputDigest, now, prepared, &response, n.client)
			return err
		})
	}
	if err == nil && rebaselined {
		r.metrics.rebaselines.Add(1)
	}
	if reason := decision(err); reason != "" && n.statusV2 {
		return r.bypass(base, req, prepared, scopeID, reason), nil
	}
	return response, err
}

// ownedDigests are the digests of the chosen segments whose original lives in
// this store (a choice without a CCR handle).
func ownedDigests(req OptimizeRequest, chosen map[string]store.MiddlewareChoice) []string {
	var digests []string
	for _, s := range req.Segments {
		if c, ok := chosen[choiceKey(s)]; ok && c.Handle == "" {
			digests = append(digests, s.SHA256)
		}
	}
	return digests
}

// publish is optimize's single write transaction: scope, choices, originals and
// the plan commit together or not at all, in a constant number of statements.
// It reports whether the request started a new epoch (see previousPlan).
func (r *Runtime) publish(ctx context.Context, tx *store.MiddlewareTx, auth, scopeID string, manifest []byte, req OptimizeRequest, inputDigest string, now int64, prepared []preparedChoice, response *OptimizeResponse, epochs bool) (bool, error) {
	prior, created, rebaselined, err := previousPlan(tx, scopeID, auth, req, inputDigest, now, epochs)
	if err != nil {
		return false, err
	}
	expires := r.expiry(now, created)
	if prior != nil {
		policyRevision := response.PolicyRevision
		*response = *prior
		response.PolicyRevision = policyRevision
		response.Recovery.ExpiresAt = expires
		return false, tx.Renew(auth, now, int64(r.cfg.Retention.Seconds()), int64(r.cfg.MaxRetention.Seconds()))
	}
	if created == 0 {
		created = now
	}
	response.Recovery.ExpiresAt = expires
	if err := tx.SaveScope(store.MiddlewareScope{ID: scopeID, Authority: auth, Manifest: manifest, Sequence: req.Sequence, ExpiresAt: expires, CreatedAt: created}); err != nil {
		return false, err
	}
	var keys []string
	for i, segment := range req.Segments {
		if prepared[i].eligible {
			keys = append(keys, choiceKey(segment))
		}
	}
	chosen, err := tx.Choices(scopeID, keys)
	if err != nil {
		return false, err
	}
	owned, err := tx.Originals(auth, ownedDigests(req, chosen))
	if err != nil {
		return false, err
	}
	var choices []store.MiddlewareChoice
	var originals []store.MiddlewareOriginal
	var owners []int // the replacement each of originals belongs to
	for i, segment := range req.Segments {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		replacement, reason, err := r.publishChoice(req, segment, prepared[i], chosen, owned)
		if err != nil {
			return false, err
		}
		before := prepared[i].before
		response.Measurement.TokensBefore += before
		if reason != "" {
			response.Measurement.TokensAfter += before
			response.Skipped = append(response.Skipped, Skip{segment.ID, reason})
			continue
		}
		response.Measurement.TokensAfter += replacement.TokensAfter
		if !replacement.Reused {
			body, _ := json.Marshal(replacement)
			choices = append(choices, store.MiddlewareChoice{ID: choiceKey(segment), Grant: replacement.RecoveryHandle, Original: segment.SHA256, Payload: body})
			originals = append(originals, store.MiddlewareOriginal{Digest: segment.SHA256, KeyID: prepared[i].keyID, Body: prepared[i].sealed})
			owners = append(owners, len(response.Replacements))
		}
		response.Replacements = append(response.Replacements, replacement)
	}
	// Count declared native tool/schema overhead once per request, including
	// replays. The client supplies only its generated recovery tool, not any
	// existing prompt or provider credentials. Final provider framing remains
	// unobserved, so this is always a segment estimate, never a request saving.
	if len(response.Replacements) > 0 && response.Measurement.TokensBefore-response.Measurement.TokensAfter <= response.Measurement.RecoveryOverheadTokens {
		// Abort all new choices and originals as well as the plan.
		return false, Failure{ReasonNotSmaller}
	}
	if len(choices) > 0 {
		if err := tx.SaveChoices(scopeID, choices); err != nil {
			return false, err
		}
		// The originals commit with the plan that references them, so an
		// aborted or not_smaller plan leaves nothing recoverable behind.
		credits, err := tx.SaveOriginals(auth, originals)
		if err != nil {
			return false, err
		}
		for j, credit := range credits {
			replacement := &response.Replacements[owners[j]]
			replacement.UniqueOriginal = credit
			if credit {
				response.Measurement.UniqueTokensReduced += replacement.TokensBefore - replacement.TokensAfter
			}
		}
	}
	if len(response.Replacements) > 0 {
		response.Status = "optimized"
		response.Reason = "eligible"
		response.Recovery.Available = true
	} else if req.Mode == "record" || r.cfg.Mode == "record" {
		response.Status = "record"
		response.Reason = "record"
	} else if len(response.Skipped) > 0 {
		response.Reason = response.Skipped[0].Reason
	}
	if !r.caps.Persistent {
		response.Stability.Native = "unavailable"
	}
	for _, skipped := range response.Skipped {
		if skipped.Reason == ReasonCacheStateUnavailable || skipped.Reason == CodeRecoveryUnavailable {
			response.Stability.Native = "unavailable"
			break
		}
	}
	// Reuse/accounting flags are observations, not part of chosen bytes.
	stable := make([][2]string, 0, len(response.Replacements))
	for _, replacement := range response.Replacements {
		stable = append(stable, [2]string{replacement.SegmentID, replacement.SHA256})
	}
	setBytes, _ := json.Marshal(stable)
	response.ReplacementSetID = identity(scopeID, digest(setBytes))
	// A plan references replacement text through its choices (previousPlan
	// restores it) instead of storing every reused segment's text again.
	stored := *response
	stored.Replacements = slices.Clone(response.Replacements)
	for i := range stored.Replacements {
		stored.Replacements[i].Text = ""
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return false, err
	}
	return rebaselined, tx.SavePlan(scopeID, req.IdempotencyKey, inputDigest, b, now+int64(r.cfg.Retention.Seconds()))
}

// decision names an optimize outcome that is a verdict on the content, not a
// failure of the request. http_status_v2 clients get it as a 200 bypass plan;
// 1.0 clients keep the 503 they always got (legacy_conditions).
func decision(err error) string {
	if err == nil {
		return ""
	}
	switch code := errorCode(err); code {
	case ReasonNotSmaller, ReasonCacheStateUnavailable, CodeRecoveryUnavailable, CodeCapacity:
		return code
	}
	return ""
}

// bypass is the §6 decision plan: nothing replaced, every sent segment skipped,
// the decision's reason on segments that would have been replaced and every
// other segment keeping its own.
func (r *Runtime) bypass(base OptimizeResponse, req OptimizeRequest, prepared []preparedChoice, scopeID, reason string) OptimizeResponse {
	base.Reason = reason
	base.Replacements, base.Skipped = []Replacement{}, make([]Skip, 0, len(req.Segments))
	for i, segment := range req.Segments {
		own := prepared[i].reason
		if own == "" {
			own = reason
		}
		base.Skipped = append(base.Skipped, Skip{segment.ID, own})
		base.Measurement.TokensBefore += prepared[i].before
	}
	base.Measurement.TokensAfter = base.Measurement.TokensBefore
	if reason != ReasonNotSmaller || !r.caps.Persistent {
		base.Stability.Native = "unavailable"
	}
	base.ReplacementSetID = identity(scopeID, digest([]byte("[]")))
	return base
}

// Run keeps storage work off the request path until ctx ends: batched retrieve
// renewals every second and an expiry sweep every minute. Without it nothing
// is physically deleted at expiry, and renewals flush inline once 1024
// authorities are pending.
func (r *Runtime) Run(ctx context.Context) {
	renew, sweep := time.NewTicker(time.Second), time.NewTicker(time.Minute)
	defer renew.Stop()
	defer sweep.Stop()
	r.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			r.flushRenewals(ctx)
		case <-sweep.C:
			r.sweep(ctx)
		}
	}
}

// defaultSweepBudget bounds one sweep; maxSweepBatch bounds one batch's rows
// per kind.
const (
	defaultSweepBudget = 20 * time.Second
	maxSweepBatch      = 1024
)

// sweep expires in batches, each its own short write transaction, for as long
// as a batch comes back full (more is waiting) and sweepBudget allows; every
// full batch doubles the next one, up to maxSweepBatch. It then counts what is
// left for the backlog gauge.
// ponytail: one sweeper per store (a try-lock elects one per Postgres fleet),
// so reclamation tops out at what it does in sweepBudget a minute: measured
// 2026-09 at ~16k elapsed scopes/s on SQLite and ~14k/s on local Postgres (each
// with a choice, plan and original), a ceiling near 4.6k expiring scopes/s
// sustained. A rising caveman_middleware_expiry_backlog is the signal to shard
// the sweep by authority.
func (r *Runtime) sweep(ctx context.Context) error {
	deadline := time.Now().Add(r.sweepBudget)
	for limit := store.MiddlewareSweepBatch; ; limit = min(limit*2, maxSweepBatch) {
		var full bool
		err := r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
			var err error
			_, full, err = tx.ExpireBatch(r.cfg.Now().Unix(), limit)
			return err
		})
		if err != nil {
			r.warn("middleware expiry sweep failed", err)
			return err
		}
		if !full || time.Now().After(deadline) {
			break
		}
	}
	backlog, err := r.cfg.Store.MiddlewareBacklog(ctx, r.cfg.Now().Unix())
	if err != nil {
		r.warn("middleware expiry backlog count failed", err)
		return err
	}
	r.backlog.Store(backlog)
	return nil
}

// renew queues a sliding renewal so retrieve never takes the writer.
func (r *Runtime) renew(auth string) {
	r.renewMu.Lock()
	r.renewals[auth] = struct{}{}
	full := len(r.renewals) >= 1024
	r.renewMu.Unlock()
	if full {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.flushRenewals(ctx)
	}
}

// flushRenewals writes every queued renewal in one transaction. A failed flush
// drops them: a scope then expires at its previous deadline, never later.
func (r *Runtime) flushRenewals(ctx context.Context) error {
	r.renewMu.Lock()
	pending := r.renewals
	r.renewals = map[string]struct{}{}
	r.renewMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	now := r.cfg.Now().Unix()
	// Sorted, so replicas renewing overlapping authorities lock rows in one order.
	err := r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		for _, auth := range slices.Sorted(maps.Keys(pending)) {
			if err := tx.Renew(auth, now, int64(r.cfg.Retention.Seconds()), int64(r.cfg.MaxRetention.Seconds())); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.warn("middleware renewal flush failed", err)
	}
	return err
}

func (r *Runtime) warn(message string, err error) {
	if r.cfg.Logger != nil {
		r.cfg.Logger.Warn(message, "error", err)
	}
}

// previousPlan validates a snapshot and returns an exact replay if there is
// one, the scope's creation time (0 when it has none) and whether the request
// starts a new epoch. It is called again with the write lock, since another
// process can append, revoke, or publish while Engine runs.
//
// A manifest that does not extend the stored one (a trimmed or summarized
// history, a nested agent on the same scope), or a sequence that went
// backwards, is 1.0's epoch_changed. A 1.1 client (epochs) re-baselines
// instead: the scope takes the new manifest and sequence and keeps its choices
// and grants (§12).
func previousPlan(tx *store.MiddlewareTx, scopeID, auth string, req OptimizeRequest, inputDigest string, now int64, epochs bool) (*OptimizeResponse, int64, bool, error) {
	previous, err := tx.Scope(scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		// A new scope id (another adapter or serialization revision) must not
		// resume an authority sessions/delete revoked.
		revoked, err := tx.Revoked(auth)
		if err == nil && revoked {
			err = Failure{CodeDeleted}
		}
		return nil, 0, false, err
	}
	if err != nil {
		return nil, 0, false, err
	}
	if previous.ExpiresAt <= 0 {
		return nil, 0, false, Failure{CodeDeleted}
	}
	if previous.ExpiresAt <= now {
		return nil, 0, false, Failure{CodeExpired}
	}
	// Exact replay may refer to a shorter, already prepared turn.
	if b, err := tx.Plan(scopeID, req.IdempotencyKey, inputDigest); err == nil {
		var response OptimizeResponse
		if json.Unmarshal(b, &response) != nil {
			return nil, 0, false, Failure{ReasonCacheStateUnavailable}
		}
		// Protocol 1.0 stored a plan's text whole; later plans reference choices.
		var keys []string
		for _, replacement := range response.Replacements {
			if replacement.Text == "" {
				keys = append(keys, identity(replacement.SegmentID, replacement.SourceID, replacement.OriginalSHA256))
			}
		}
		chosen, err := tx.Choices(scopeID, keys)
		if err != nil {
			return nil, 0, false, err
		}
		for i, replacement := range response.Replacements {
			if replacement.Text != "" {
				continue
			}
			var c Replacement
			choice, ok := chosen[identity(replacement.SegmentID, replacement.SourceID, replacement.OriginalSHA256)]
			if !ok || json.Unmarshal(choice.Payload, &c) != nil || c.SHA256 != replacement.SHA256 {
				return nil, 0, false, Failure{ReasonCacheStateUnavailable}
			}
			response.Replacements[i].Text = c.Text
		}
		return &response, previous.CreatedAt, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, err
	}
	var old []ManifestItem
	if json.Unmarshal(previous.Manifest, &old) != nil {
		return nil, 0, false, Failure{ReasonCacheStateUnavailable}
	}
	if req.Sequence < previous.Sequence || len(old) > len(req.ContextManifest) || !slices.Equal(old, req.ContextManifest[:min(len(old), len(req.ContextManifest))]) {
		if !epochs {
			return nil, 0, false, Failure{CodeEpochChanged}
		}
		return nil, previous.CreatedAt, true, nil
	}
	return nil, previous.CreatedAt, false, nil
}
