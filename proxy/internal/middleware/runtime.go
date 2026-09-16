package middleware

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/tokens"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type Config struct {
	Store    *store.Store
	Recovery *ccr.Store
	// Principal must resolve authenticated server authority, never a client
	// namespace, framework run ID, or a tenant field from request JSON.
	Principal              func(*http.Request) (string, error)
	Build, Mode, TrustMode string
	Retention              time.Duration
	Limits                 Limits
	Now                    func() time.Time
}

type Runtime struct {
	cfg        Config
	eng        *engine.Engine
	counter    tokens.Counter
	caps       Capabilities
	transforms map[string]compressors.Capability
	queue      chan struct{}
}

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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Build == "" {
		cfg.Build = "development"
	}
	if cfg.TrustMode == "" {
		cfg.TrustMode = "resolver"
	}
	if cfg.Limits.DeadlineMS <= 0 {
		// 100ms covered the queue plus Engine work only on an idle machine; the
		// shared budget is what clients see as a bypass under any real load.
		cfg.Limits.DeadlineMS = 500
	}
	if cfg.Limits.RequestBytes <= 0 {
		cfg.Limits.RequestBytes = DefaultRequestBytes
	}
	if cfg.Limits.SegmentBytes <= 0 {
		cfg.Limits.SegmentBytes = DefaultSegmentBytes
	}
	if cfg.Limits.PageBytes <= 0 {
		cfg.Limits.PageBytes = DefaultPageBytes
	}
	if err := cfg.Store.InitMiddleware(context.Background()); err != nil {
		return nil, err
	}
	counter := newMemoCounter(tokens.Default())
	r := &Runtime{cfg: cfg, eng: engine.New(cfg.Recovery, counter), counter: counter,
		transforms: map[string]compressors.Capability{}, queue: make(chan struct{}, 16)}
	caps := r.eng.Capabilities()
	for _, cap := range caps {
		r.transforms[cap.TransformID] = cap
	}
	capsJSON, _ := json.Marshal(caps)
	r.caps = Capabilities{SchemaVersion: ProtocolVersion, RuntimeBuild: cfg.Build,
		PolicyRevision: "middleware-v1:" + digest(capsJSON), Transforms: caps,
		Limits: cfg.Limits, Persistent: cfg.Store.Persistent() && (cfg.Recovery == nil || cfg.Recovery.Persistent()), Recovery: cfg.Recovery != nil,
		RetentionSeconds: int64(cfg.Retention.Seconds()), TrustMode: cfg.TrustMode, Mode: cfg.Mode}
	return r, nil
}

func digest(b []byte) string          { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func identity(parts ...string) string { b, _ := json.Marshal(parts); return digest(b) }
func authority(principal string, s Scope) string {
	return identity(principal, s.Namespace, s.SessionID, s.BranchID, s.CacheEpoch)
}

func (r *Runtime) optimize(ctx context.Context, principal string, req OptimizeRequest, inputDigest string) (OptimizeResponse, error) {
	if err := r.validate(req); err != nil {
		return OptimizeResponse{}, err
	}
	auth := authority(principal, req.Scope)
	transforms := slices.Clone(req.Policy.Transforms)
	slices.Sort(transforms)
	scopeID := identity(auth, req.Adapter.ID, req.Adapter.SerializationRevision, req.Policy.Revision, strings.Join(transforms, ","))
	manifest, _ := json.Marshal(req.ContextManifest)
	response := OptimizeResponse{SchemaVersion: ProtocolVersion, RequestID: req.RequestID, InputDigest: inputDigest,
		RuntimeBuild: r.cfg.Build, PolicyRevision: r.caps.PolicyRevision, Status: "bypassed", Reason: "no_candidate",
		Replacements: []Replacement{}, Skipped: []Skip{},
		Measurement: Measurement{Basis: "inferred", Tokenizer: r.counter.Name(), Scope: "segment", OverheadCoverage: "segment_and_declared_recovery_tool"},
		Stability:   Stability{Native: "persistent_choices", ProviderBytes: "unobserved", ProviderCacheHits: "unobserved"}}
	if req.RecoveryBinding != nil {
		response.Recovery.BindingID = req.RecoveryBinding.ID
		response.Measurement.RecoveryOverheadTokens = r.counter.Count([]byte(req.RecoveryBinding.OverheadText))
	}
	response.Recovery.Persistent = r.caps.Persistent
	now := r.cfg.Now().Unix()
	expires := now + r.caps.RetentionSeconds
	// Reclaim in its own transaction, ahead of any decision. Sharing the request's
	// transaction made every rejection - capacity, not_smaller, epoch_changed -
	// roll back the batch that would have freed the room, so a store at its row
	// cap could never drain: the next request hit the same cap and rolled back too.
	if err := r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error { return tx.Expire(now) }); err != nil {
		return response, err
	}
	// Read first, then perform Engine work and durable CCR writes without holding
	// the metadata writer. The final transaction rechecks scope, plan and choices;
	// a competing process's first published bytes always win.
	var replay *OptimizeResponse
	prepared := make([]preparedChoice, len(req.Segments))
	err := r.cfg.Store.ReadMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		var err error
		replay, err = previousPlan(tx, scopeID, req, inputDigest, now)
		return err
	})
	if err != nil {
		return response, err
	}
	if replay == nil {
		for i, segment := range req.Segments {
			if err := ctx.Err(); err != nil {
				return response, err
			}
			prepared[i], err = r.prepareChoice(ctx, scopeID, req, segment)
			if err != nil {
				return response, err
			}
		}
	}
	err = r.cfg.Store.WithMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		prior, err := previousPlan(tx, scopeID, req, inputDigest, now)
		if err != nil {
			return err
		}
		if prior != nil {
			response = *prior
			response.Recovery.ExpiresAt = expires
			return tx.Renew(auth, now, expires)
		}
		if replay != nil {
			return Failure{"cache_state_unavailable"}
		}
		response.Recovery.ExpiresAt = expires
		if err := tx.SaveScope(store.MiddlewareScope{ID: scopeID, Authority: auth, Manifest: manifest, Sequence: req.Sequence, ExpiresAt: expires}); err != nil {
			return err
		}
		for i, segment := range req.Segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			replacement, reason, err := r.publishChoice(tx, scopeID, segment, prepared[i])
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
				credit, err := tx.CreditOriginal(auth, segment.SHA256)
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
			// Abort all new choices as well as the plan. CCR may retain a durable
			// original, but no marker from this transaction can reach a caller.
			return Failure{"not_smaller"}
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
			if skipped.Reason == "cache_state_unavailable" || skipped.Reason == "recovery_unavailable" {
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
		b, err := json.Marshal(response)
		if err != nil {
			return err
		}
		return tx.SavePlan(scopeID, req.IdempotencyKey, inputDigest, b)
	})
	return response, err
}

// previousPlan validates a snapshot. It is called again with the write lock,
// since another process can append, revoke, or publish while Engine runs.
func previousPlan(tx *store.MiddlewareTx, scopeID string, req OptimizeRequest, inputDigest string, now int64) (*OptimizeResponse, error) {
	previous, err := tx.Scope(scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if previous.ExpiresAt <= 0 {
		return nil, Failure{"deleted"}
	}
	if previous.ExpiresAt <= now {
		return nil, Failure{"expired"}
	}
	// Exact replay may refer to a shorter, already prepared turn.
	if b, err := tx.Plan(scopeID, req.IdempotencyKey, inputDigest); err == nil {
		var response OptimizeResponse
		if json.Unmarshal(b, &response) != nil {
			return nil, Failure{"cache_state_unavailable"}
		}
		return &response, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var old []ManifestItem
	if json.Unmarshal(previous.Manifest, &old) != nil {
		return nil, Failure{"cache_state_unavailable"}
	}
	if req.Sequence < previous.Sequence || len(old) > len(req.ContextManifest) || !slices.Equal(old, req.ContextManifest[:min(len(old), len(req.ContextManifest))]) {
		return nil, Failure{"epoch_changed"}
	}
	return nil, nil
}
