package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// screen is a segment's reason to be left alone before any lookup; "" makes it
// a candidate.
func (r *Runtime) screen(req OptimizeRequest, s Segment) string {
	switch {
	case req.Mode == "record" || r.cfg.Mode == "record":
		return "record"
	case s.Protected || (s.Kind != "tool_result" && s.Kind != "artifact"):
		return "protected"
	case s.Opaque || !utf8.ValidString(s.Content) || strings.ContainsRune(s.Content, '\x00'):
		return "unsupported_shape"
	case len(s.Content) > r.cfg.Limits.SegmentBytes:
		return "payload_limit"
	case req.RecoveryBinding == nil || !r.caps.Persistent:
		return "recovery_unavailable"
	}
	return ""
}

// prepareChoice does a segment's Engine work, outside any transaction. reason
// is its screen result; chosen and owned are the scope's choices and the
// authority's originals as optimize's snapshot read them.
func (r *Runtime) prepareChoice(ctx context.Context, auth string, req OptimizeRequest, s Segment, reason string, chosen map[string]store.MiddlewareChoice, owned map[string]store.StoredOriginal) (preparedChoice, error) {
	p := preparedChoice{reason: reason}
	if p.reason != "" {
		p.before = r.counter.Count([]byte(s.Content))
		return p, nil
	}
	p.eligible = true
	if c, ok := chosen[choiceKey(s)]; ok {
		p.handle = c.Handle
		if json.Unmarshal(c.Payload, &p.replacement) != nil {
			return p, Failure{ReasonCacheStateUnavailable}
		}
		p.before = p.replacement.TokensBefore
		// A persisted choice is reused byte for byte only under a policy that
		// still allows its transform (§2); otherwise the segment is skipped.
		if !slices.Contains(req.Policy.Transforms, p.replacement.TransformID) {
			p.eligible, p.reason = false, CodeUnknownCapability
			return p, nil
		}
		if r.verifyOriginal(p.handle, r.held(owned, s.SHA256), s.SHA256) != nil {
			if p.handle != "" {
				// A protocol 1.0 grant recovers only from CCR, which lost it:
				// this segment goes unreplaced, the rest of the request does not.
				p.eligible, p.reason = false, CodeRecoveryUnavailable
				return p, nil
			}
			// The stored original is gone or sealed with a key this runtime
			// cannot open (rotated away, plaintext under a keyring). The
			// request carries the content, verified against the choice's
			// digest: the choice is kept and publish stores it again.
			var err error
			if p.sealed, p.keyID, err = r.cfg.Keys.seal(auth, s.SHA256, []byte(s.Content)); err != nil {
				return p, err
			}
		}
		p.replacement.Reused = true
		return p, nil
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

// publishChoice is a segment's replacement under the write lock: the choice
// another writer published first if there is one, else the prepared one.
func (r *Runtime) publishChoice(req OptimizeRequest, s Segment, p preparedChoice, chosen map[string]store.MiddlewareChoice, owned map[string]store.StoredOriginal) (Replacement, string, error) {
	if !p.eligible {
		return Replacement{}, p.reason, nil
	}
	if c, ok := chosen[choiceKey(s)]; ok {
		var existing Replacement
		if json.Unmarshal(c.Payload, &existing) != nil {
			return Replacement{}, "", Failure{ReasonCacheStateUnavailable}
		}
		if !slices.Contains(req.Policy.Transforms, existing.TransformID) {
			return Replacement{}, CodeUnknownCapability, nil
		}
		// Usually already checked outside the write lock. Only a different
		// first writer needs a fresh check here.
		if c.Handle != p.handle || existing.SHA256 != p.replacement.SHA256 {
			// p.sealed is this request's copy of the original: publish stores it
			// for a choice whose own copy is unusable.
			if err := r.verifyOriginal(c.Handle, r.held(owned, s.SHA256) || p.sealed != nil, s.SHA256); err != nil {
				return Replacement{}, "", err
			}
		}
		existing.Reused = true
		existing.UniqueOriginal = false
		return existing, "", nil
	}
	if p.replacement.Reused {
		return Replacement{}, "", Failure{ReasonCacheStateUnavailable}
	}
	if p.reason != "" {
		return Replacement{}, p.reason, nil
	}
	return p.replacement, "", nil
}
