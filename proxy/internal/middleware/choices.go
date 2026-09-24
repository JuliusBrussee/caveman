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
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type preparedChoice struct {
	replacement Replacement
	// handle is the CCR handle of a choice protocol 1.0 made; "" otherwise.
	handle   string
	before   int
	reason   string
	eligible bool
	// sealed is the original as it will be stored (encrypted when a key is
	// configured) and keyID the key that sealed it.
	sealed []byte
	keyID  string
}

func (r *Runtime) prepareChoice(ctx context.Context, auth, scopeID string, req OptimizeRequest, s Segment) (preparedChoice, error) {
	p := preparedChoice{}
	if req.Mode == "record" || r.cfg.Mode == "record" {
		p.reason = "record"
	} else if s.Protected || (s.Kind != "tool_result" && s.Kind != "artifact") {
		p.reason = "protected"
	} else if s.Opaque || !utf8.ValidString(s.Content) || strings.ContainsRune(s.Content, '\x00') {
		p.reason = "unsupported_shape"
	} else if len(s.Content) > r.cfg.Limits.SegmentBytes {
		p.reason = "payload_limit"
	} else if req.RecoveryBinding == nil || !r.caps.Persistent {
		p.reason = "recovery_unavailable"
	}
	if p.reason != "" {
		p.before = r.counter.Count([]byte(s.Content))
		return p, nil
	}
	p.eligible = true
	var body []byte
	stored := false
	err := r.cfg.Store.ReadMiddleware(ctx, func(tx *store.MiddlewareTx) error {
		var err error
		body, p.handle, err = tx.Choice(scopeID, identity(s.ID, s.SourceID, s.SHA256))
		if err == nil && p.handle == "" {
			stored, err = tx.HasOriginal(auth, s.SHA256)
		}
		return err
	})
	if err == nil {
		if json.Unmarshal(body, &p.replacement) != nil {
			return p, Failure{ReasonCacheStateUnavailable}
		}
		p.before = p.replacement.TokensBefore
		// A persisted choice is reused byte for byte only under a policy that
		// still allows its transform (§2); otherwise the segment is skipped.
		if !slices.Contains(req.Policy.Transforms, p.replacement.TransformID) {
			p.eligible, p.reason = false, CodeUnknownCapability
			return p, nil
		}
		if err := r.verifyOriginal(p.handle, stored, s.SHA256); err != nil {
			return p, err
		}
		p.replacement.Reused = true
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.before = r.counter.Count([]byte(s.Content))
	if s.CacheRegion == "frozen_prefix" {
		p.reason = ReasonCacheStateUnavailable
		return p, nil
	}
	ct := r.eng.Detect([]byte(s.Content))
	transform := "caveman.engine." + ct + ".v1"
	cap, ok := r.transforms[transform]
	if !ok || !slices.Contains(req.Policy.Transforms, transform) || !slices.Contains(cap.EligibleSegmentKinds, s.Kind) {
		p.reason = CodeUnknownCapability
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
		p.reason = ReasonNotSmaller
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
		p.reason = ReasonNotSmaller
		return p, nil
	}
	// The original is sealed here, off the writer, and stored only by the
	// transaction that publishes a plan referencing it.
	if p.sealed, p.keyID, err = r.cfg.Keys.seal(auth, s.SHA256, []byte(s.Content)); err != nil {
		return p, err
	}
	p.replacement = Replacement{SegmentID: s.ID, SourceID: s.SourceID, OriginalSHA256: s.SHA256, Text: text, SHA256: digest([]byte(text)),
		TransformID: transform, TransformVersion: cap.ImplementationVersion, RecoveryHandle: grant, TokensBefore: result.TokensBefore, TokensAfter: after}
	return p, nil
}

// verifyOriginal refuses to reuse a choice whose original is gone: its marker
// would point at nothing. stored reports the middleware store's copy; a choice
// protocol 1.0 made keeps its original in CCR under handle.
func (r *Runtime) verifyOriginal(handle string, stored bool, originalDigest string) error {
	if handle == "" {
		if !stored {
			return Failure{CodeRecoveryUnavailable}
		}
		return nil
	}
	original, err := r.eng.Retrieve(handle)
	if err != nil || digest(original) != originalDigest {
		return Failure{CodeRecoveryUnavailable}
	}
	return nil
}

func (r *Runtime) publishChoice(tx *store.MiddlewareTx, auth, scopeID string, req OptimizeRequest, s Segment, p preparedChoice) (Replacement, string, error) {
	if !p.eligible {
		return Replacement{}, p.reason, nil
	}
	key := identity(s.ID, s.SourceID, s.SHA256)
	if body, handle, err := tx.Choice(scopeID, key); err == nil {
		var chosen Replacement
		if json.Unmarshal(body, &chosen) != nil {
			return Replacement{}, "", Failure{ReasonCacheStateUnavailable}
		}
		if !slices.Contains(req.Policy.Transforms, chosen.TransformID) {
			return Replacement{}, CodeUnknownCapability, nil
		}
		// Usually already checked outside the write lock. Only a different
		// first writer needs a fresh check here.
		if handle != p.handle || chosen.SHA256 != p.replacement.SHA256 {
			stored := false
			if handle == "" {
				if stored, err = tx.HasOriginal(auth, s.SHA256); err != nil {
					return Replacement{}, "", err
				}
			}
			if err := r.verifyOriginal(handle, stored, s.SHA256); err != nil {
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
		return Replacement{}, "", Failure{ReasonCacheStateUnavailable}
	}
	if p.reason != "" {
		return Replacement{}, p.reason, nil
	}
	body, _ := json.Marshal(p.replacement)
	if err := tx.SaveChoice(scopeID, key, p.replacement.RecoveryHandle, "", body); err != nil {
		return Replacement{}, "", err
	}
	return p.replacement, "", nil
}
