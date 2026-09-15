package middleware

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type preparedChoice struct {
	replacement Replacement
	handle      string
	before      int
	reason      string
	eligible    bool
}

func (r *Runtime) prepareChoice(ctx context.Context, scopeID string, req OptimizeRequest, s Segment) (preparedChoice, error) {
	p := preparedChoice{}
	if req.Mode == "record" || r.cfg.Mode == "record" {
		p.reason = "record"
	} else if s.Protected || (s.Kind != "tool_result" && s.Kind != "artifact") {
		p.reason = "protected"
	} else if s.Opaque || !utf8.ValidString(s.Content) || strings.ContainsRune(s.Content, '\x00') {
		p.reason = "unsupported_shape"
	} else if len(s.Content) > r.cfg.Limits.SegmentBytes {
		p.reason = "payload_limit"
	} else if req.RecoveryBinding == nil || r.cfg.Recovery == nil || !r.caps.Persistent {
		p.reason = "recovery_unavailable"
	}
	if p.reason != "" {
		p.before = r.counter.Count([]byte(s.Content))
		return p, nil
	}
	p.eligible = true
	var body []byte
	err := r.cfg.Store.ReadMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		var err error
		body, p.handle, err = tx.Choice(scopeID, identity(s.ID, s.SourceID, s.SHA256))
		return err
	})
	if err == nil {
		if json.Unmarshal(body, &p.replacement) != nil {
			return p, Failure{"cache_state_unavailable"}
		}
		if err := r.verifyOriginal(p.handle, s.SHA256); err != nil {
			return p, err
		}
		p.replacement.Reused = true
		p.before = p.replacement.TokensBefore
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.before = r.counter.Count([]byte(s.Content))
	if s.CacheRegion == "frozen_prefix" {
		p.reason = "cache_state_unavailable"
		return p, nil
	}
	ct := r.eng.Detect([]byte(s.Content))
	transform := "caveman.engine." + ct + ".v1"
	cap, ok := r.transforms[transform]
	if !ok || !slices.Contains(req.Policy.Transforms, transform) || !slices.Contains(cap.EligibleSegmentKinds, s.Kind) {
		p.reason = "unknown_capability"
		return p, nil
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	result, err := r.eng.Compress([]byte(s.Content), engine.Options{Mode: engine.ModeCompress, Type: ct, ExternalRecovery: true})
	if err != nil {
		return p, err
	}
	if result.TokensAfter >= result.TokensBefore {
		p.reason = "not_smaller"
		return p, nil
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return p, err
	}
	grant := "cmw_" + hex.EncodeToString(random)
	text := "[caveman: shortened; exact original via caveman_retrieve handle=" + grant + "]\n" + string(result.Output)
	after := r.counter.Count([]byte(text))
	if after >= result.TokensBefore {
		p.reason = "not_smaller"
		return p, nil
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	// Only CCR writes originals. Durable storage precedes publishing any grant;
	// its global content-addressed handle never reaches the application.
	// CCR describes the Engine transform. Scoped marker overhead belongs only
	// to the request plan; including its random handle here would rewrite the
	// same original on every scope despite an unchanged Engine result.
	p.handle, err = r.cfg.Recovery.Put(ccr.Recovery{Original: []byte(s.Content), ContentType: result.ContentType,
		Compressor: result.ContentType, TokensBefore: result.TokensBefore, TokensAfter: result.TokensAfter})
	if err != nil {
		return p, err
	}
	p.replacement = Replacement{SegmentID: s.ID, SourceID: s.SourceID, OriginalSHA256: s.SHA256, Text: text, SHA256: digest([]byte(text)),
		TransformID: transform, TransformVersion: cap.ImplementationVersion, RecoveryHandle: grant, TokensBefore: result.TokensBefore, TokensAfter: after}
	return p, nil
}

func (r *Runtime) verifyOriginal(handle, originalDigest string) error {
	original, err := r.eng.Retrieve(handle)
	if err != nil || digest(original) != originalDigest {
		return Failure{"recovery_unavailable"}
	}
	return nil
}

func (r *Runtime) publishChoice(tx *store.MiddlewareTx, scopeID string, s Segment, p preparedChoice) (Replacement, string, error) {
	if !p.eligible {
		return Replacement{}, p.reason, nil
	}
	key := identity(s.ID, s.SourceID, s.SHA256)
	if body, handle, err := tx.Choice(scopeID, key); err == nil {
		var chosen Replacement
		if json.Unmarshal(body, &chosen) != nil {
			return Replacement{}, "", Failure{"cache_state_unavailable"}
		}
		// Usually already checked outside the write lock. Only a different
		// first writer needs a fresh CCR read here.
		if handle != p.handle || chosen.SHA256 != p.replacement.SHA256 {
			if err := r.verifyOriginal(handle, s.SHA256); err != nil {
				return Replacement{}, "", err
			}
		}
		chosen.Reused = true
		chosen.UniqueOriginal = false
		return chosen, "", nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Replacement{}, "", err
	}
	if p.replacement.Reused {
		return Replacement{}, "", Failure{"cache_state_unavailable"}
	}
	if p.reason != "" {
		return Replacement{}, p.reason, nil
	}
	body, _ := json.Marshal(p.replacement)
	if err := tx.SaveChoice(scopeID, key, p.replacement.RecoveryHandle, p.handle, body); err != nil {
		return Replacement{}, "", err
	}
	return p.replacement, "", nil
}
