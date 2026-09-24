package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/tokens"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type Config struct {
	Store *store.Store
	// Recovery is the process-global CCR. Originals now live in Store, owned by
	// their scope; CCR is only read, for grants an older runtime issued. May be nil.
	Recovery *ccr.Store
	// Principal must resolve authenticated server authority, never a client
	// namespace, framework run ID, or a tenant field from request JSON.
	Principal              func(*http.Request) (string, error)
	Build, Mode, TrustMode string
	// Retention slides with use. MaxRetention caps the slide, counted from a
	// scope's creation (§12); it is raised to Retention when smaller.
	Retention, MaxRetention time.Duration
	Limits                  Limits
	// Capacity bounds the middleware store in total and per principal. A
	// principal at its quota gets capacity; other principals are unaffected.
	Capacity store.MiddlewareLimits
	// Keys seals originals at rest. Nil stores them in plaintext.
	Keys *Keyring
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
	quota                        rateQuota
	metrics                      metrics

	renewMu  sync.Mutex
	renewals map[string]struct{}
}

// serverFeatures are the negotiable 1.1 features, sorted (§3).
var serverFeatures = []string{FeatureHTTPStatusV2, FeatureOriginalsLifecycle, FeatureRevisionTolerant, FeatureTolerantReader}

func New(cfg Config) (*Runtime, error) {
	if cfg.Store == nil || cfg.Principal == nil {
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
		quota: rateQuota{limit: l.QuotaRequestsPerMinute}, renewals: map[string]struct{}{}}
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
		Limits: cfg.Limits, Persistent: cfg.Store.Persistent(), Recovery: true,
		RetentionSeconds: int64(cfg.Retention.Seconds()), TrustMode: cfg.TrustMode, Mode: cfg.Mode,
		Protocol: &ProtocolRange{ProtocolMin, ProtocolMax}, Features: serverFeatures, MaxRetentionSeconds: int64(cfg.MaxRetention.Seconds())}
	r.legacyCaps = r.caps
	r.legacyCaps.Transforms, r.legacyCaps.PolicyRevision = legacy, policyRevision(legacy)
	r.legacyCaps.Limits = Limits{DeadlineMS: l.DeadlineMS, RequestBytes: l.RequestBytes, SegmentBytes: l.SegmentBytes, PageBytes: l.PageBytes}
	r.legacyCaps.Protocol, r.legacyCaps.Features, r.legacyCaps.MaxRetentionSeconds = nil, nil, 0
	return r, nil
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

// write runs fn in a write transaction that stamps and bounds rows by principal.
func (r *Runtime) write(ctx context.Context, principal string, fn func(*store.MiddlewareTx) error) error {
	return r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		tx.Principal, tx.Limits = principal, r.cfg.Capacity
		return fn(tx)
	})
}

func (r *Runtime) optimize(ctx context.Context, principal string, req OptimizeRequest, inputDigest string, n negotiated) (OptimizeResponse, error) {
	if err := r.validate(req, n); err != nil {
		return OptimizeResponse{}, err
	}
	caps, _ := r.view(n)
	auth := authority(principal, req.Scope)
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
	// Read first, then perform Engine work without holding the metadata writer.
	// The final transaction rechecks scope, plan and choices; a competing
	// process's first published bytes always win. An exact replay needs no write
	// at all: its renewal joins the batched ones.
	var replay *OptimizeResponse
	var created int64
	prepared := make([]preparedChoice, len(req.Segments))
	err := r.cfg.Store.ReadMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		var err error
		replay, created, err = previousPlan(tx, scopeID, req, inputDigest, now)
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
			prepared[i], err = r.prepareChoice(ctx, auth, scopeID, req, req.Segments[i])
		}
	}
	if err == nil {
		err = r.write(ctx, principal, func(tx *store.MiddlewareTx) error {
			return r.publish(ctx, tx, auth, scopeID, manifest, req, inputDigest, now, prepared, &response)
		})
	}
	if reason := decision(err); reason != "" && n.statusV2 {
		return r.bypass(base, req, prepared, scopeID, reason), nil
	}
	return response, err
}

// publish is optimize's single write transaction: scope, choices, originals and
// the plan commit together or not at all.
func (r *Runtime) publish(ctx context.Context, tx *store.MiddlewareTx, auth, scopeID string, manifest []byte, req OptimizeRequest, inputDigest string, now int64, prepared []preparedChoice, response *OptimizeResponse) error {
	prior, created, err := previousPlan(tx, scopeID, req, inputDigest, now)
	if err != nil {
		return err
	}
	expires := r.expiry(now, created)
	if prior != nil {
		policyRevision := response.PolicyRevision
		*response = *prior
		response.PolicyRevision = policyRevision
		response.Recovery.ExpiresAt = expires
		return tx.Renew(auth, now, int64(r.cfg.Retention.Seconds()), int64(r.cfg.MaxRetention.Seconds()))
	}
	if created == 0 {
		created = now
	}
	response.Recovery.ExpiresAt = expires
	if err := tx.SaveScope(store.MiddlewareScope{ID: scopeID, Authority: auth, Manifest: manifest, Sequence: req.Sequence, ExpiresAt: expires, CreatedAt: created}); err != nil {
		return err
	}
	for i, segment := range req.Segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		replacement, reason, err := r.publishChoice(tx, auth, scopeID, req, segment, prepared[i])
		if err != nil {
			return err
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
			// The original commits with the plan that references it, so an
			// aborted or not_smaller plan leaves nothing recoverable behind.
			credit, err := tx.SaveOriginal(auth, segment.SHA256, prepared[i].sealed, prepared[i].keyID)
			if err != nil {
				return err
			}
			replacement.UniqueOriginal = credit
			if credit {
				response.Measurement.UniqueTokensReduced += replacement.TokensBefore - replacement.TokensAfter
			}
		}
		response.Replacements = append(response.Replacements, replacement)
	}
	// Count declared native tool/schema overhead once per request, including
	// replays. The client supplies only its generated recovery tool, not any
	// existing prompt or provider credentials. Final provider framing remains
	// unobserved, so this is always a segment estimate, never a request saving.
	if len(response.Replacements) > 0 && response.Measurement.TokensBefore-response.Measurement.TokensAfter <= response.Measurement.RecoveryOverheadTokens {
		// Abort all new choices and originals as well as the plan.
		return Failure{ReasonNotSmaller}
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
		return err
	}
	return tx.SavePlan(scopeID, req.IdempotencyKey, inputDigest, b, now+int64(r.cfg.Retention.Seconds()))
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

// sweep expires in batches, each its own short write transaction, until a batch
// finds nothing left. The pass cap bounds one sweep over a huge backlog.
func (r *Runtime) sweep(ctx context.Context) error {
	for pass := 0; pass < 1000; pass++ {
		var n int64
		err := r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
			var err error
			n, err = tx.Expire(r.cfg.Now().Unix())
			return err
		})
		if err != nil {
			r.warn("middleware expiry sweep failed", err)
			return err
		}
		if n == 0 {
			return nil
		}
	}
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
	err := r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		for auth := range pending {
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

// previousPlan validates a snapshot and returns the scope's creation time (0
// when it has none). It is called again with the write lock, since another
// process can append, revoke, or publish while Engine runs.
func previousPlan(tx *store.MiddlewareTx, scopeID string, req OptimizeRequest, inputDigest string, now int64) (*OptimizeResponse, int64, error) {
	previous, err := tx.Scope(scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if previous.ExpiresAt <= 0 {
		return nil, 0, Failure{CodeDeleted}
	}
	if previous.ExpiresAt <= now {
		return nil, 0, Failure{CodeExpired}
	}
	// Exact replay may refer to a shorter, already prepared turn.
	if b, err := tx.Plan(scopeID, req.IdempotencyKey, inputDigest); err == nil {
		var response OptimizeResponse
		if json.Unmarshal(b, &response) != nil {
			return nil, 0, Failure{ReasonCacheStateUnavailable}
		}
		for i, replacement := range response.Replacements {
			if replacement.Text != "" {
				continue // stored whole by protocol 1.0
			}
			body, _, err := tx.Choice(scopeID, identity(replacement.SegmentID, replacement.SourceID, replacement.OriginalSHA256))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, 0, err
			}
			var chosen Replacement
			if err != nil || json.Unmarshal(body, &chosen) != nil || chosen.SHA256 != replacement.SHA256 {
				return nil, 0, Failure{ReasonCacheStateUnavailable}
			}
			response.Replacements[i].Text = chosen.Text
		}
		return &response, previous.CreatedAt, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	var old []ManifestItem
	if json.Unmarshal(previous.Manifest, &old) != nil {
		return nil, 0, Failure{ReasonCacheStateUnavailable}
	}
	if req.Sequence < previous.Sequence || len(old) > len(req.ContextManifest) || !slices.Equal(old, req.ContextManifest[:min(len(old), len(req.ContextManifest))]) {
		return nil, 0, Failure{CodeEpochChanged}
	}
	return nil, previous.CreatedAt, nil
}
