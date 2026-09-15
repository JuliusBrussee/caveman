// Package sampling holds the one deterministic sampler every service shares.
//
// The construction is tenant-first, length-prefixed SHA-256 mapped onto the top
// 53 bits — the same mapping experiments.AssignBucket and the gateway's
// count-token baseline already used. Length prefixing matters: without it the
// key lists ("ab","c") and ("a","bc") would hash identically, and one tenant
// could steer another tenant's selection by choosing a request id. Nothing in
// the request body is hashed, so a caller can never influence its own selection.
//
// The point of sharing it is that the gateway and the worker must agree: the
// same (org, project, monitor, request) is sampled the same way in both, on
// every run, forever. Changing this hash silently reshuffles every population.
package sampling

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

// Fraction maps the ordered key list onto a stable value in [0, 1). The same
// keys always yield the same fraction; keys are length-prefixed so no two
// distinct key lists can collide by concatenation.
func Fraction(keys ...string) float64 {
	h := sha256.New()
	for _, key := range keys {
		writePart(h, key)
	}
	sum := h.Sum(nil)
	u := binary.BigEndian.Uint64(sum[:8])
	return float64(u>>11) / float64(1<<53)
}

// Sampled reports whether the key list falls inside the given rate. rate <= 0
// selects nothing and rate >= 1 selects everything; a NaN rate fails closed
// (every comparison against NaN is false, so nothing is selected). Callers are
// responsible for rejecting an incomplete key scope before calling — an empty
// key still hashes, so a missing tenant id would otherwise sample as if it were
// a real, shared scope.
func Sampled(rate float64, keys ...string) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return Fraction(keys...) < rate
}

func writePart(h hash.Hash, value string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(value)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(value))
}
