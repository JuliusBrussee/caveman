package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"slices"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// A raw pin records that the provider rejected a request in transformed form
// and accepted its original bytes. What the provider cached is then the raw
// request, so a later request that extends it goes out raw as well —
// re-substituting earlier replacements would bust that entry on the next turn.
//
// The longest lineage wins. A pin covers what the retried request cached, and
// another conversation can share that prefix: two subagents given one task,
// repeated `claude -p` runs. A sibling that had already cached a LONGER prefix
// in replaced form keeps following it; holding it to the pin would bust its
// own entry for a shorter one. Every accepted request whose cached prefix went
// out replaced is therefore recorded as a lineage, and a pin applies only when
// no longer lineage of the request exists.
//
// A pin is taken only on a conversation's own bytes. A retried request that
// cached nothing past the agent-wide components (system and tools) re-cached
// bytes every conversation of the agent shares; pinning those would stop the
// whole agent compressing, while their replaced form stays warm from everyone
// else's traffic.
//
// Pins and lineages live in the PrefixCache (they survive restarts) and in
// memory (a failing store cannot unpin a conversation this process pinned).
// The store never evicts pin rows (RawPinHandle).
const (
	rawPinScope = "rawpin"
	// rawPinSlots bounds the pins kept per conversation identity.
	rawPinSlots = 64
	// rawPinIdentities bounds the in-memory copy; the store keeps the rest.
	rawPinIdentities = 4096

	lineageScope  = "lineage"
	lineageHandle = "lineage"
	// lineageReplaced marks a lineage whose cached prefix went out replaced, and
	// lineageStreamRaw one a PAYG stream sent raw on the server-side retrieve
	// path (see proxy.go): the conversations it opens stay raw.
	lineageReplaced  = 'r'
	lineageStreamRaw = 's'
	// lineageMemory bounds the in-memory lineage set; the store keeps the rest.
	lineageMemory = 65536
)

// RawPinHandle marks a PrefixCache row holding a raw pin. The store keeps these
// out of its LRU eviction: losing one re-substitutes over the raw prefix the
// provider cached.
const RawPinHandle = "rawpin"

type rawPin struct {
	n      uint32
	digest [32]byte
}

func (p rawPin) encode() []byte {
	return append(binary.BigEndian.AppendUint32(nil, p.n), p.digest[:]...)
}

func decodeRawPin(b []byte) (rawPin, bool) {
	if len(b) != 4+32 {
		return rawPin{}, false
	}
	p := rawPin{n: binary.BigEndian.Uint32(b)}
	copy(p.digest[:], b[4:])
	return p, p.n > 0
}

func (p rawPin) matches(prefix *prefixDigests) bool {
	return int(p.n) <= len(prefix.components) && prefix.at(int(p.n)) == p.digest
}

// rawPins holds the pins of each conversation identity this process has seen.
// An identity is loaded from the store once; pins taken here are added to it.
type rawPins struct {
	mu  sync.Mutex
	ids map[[32]byte]*pinSet
}

type pinSet struct {
	loaded bool
	pins   []rawPin
}

func (r *rawPins) set(identity [32]byte) *pinSet {
	if r.ids == nil || len(r.ids) >= rawPinIdentities {
		r.ids = map[[32]byte]*pinSet{}
	}
	set := r.ids[identity]
	if set == nil {
		set = &pinSet{}
		r.ids[identity] = set
	}
	return set
}

func (r *rawPins) add(identity [32]byte, pins ...rawPin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.set(identity)
	for _, p := range pins {
		if !slices.Contains(set.pins, p) {
			set.pins = append(set.pins, p)
		}
	}
}

// pinsFor returns identity's pins, reading its store slots the first time.
func (s *Server) pinsFor(identity [32]byte) []rawPin {
	s.rawPins.mu.Lock()
	if set, ok := s.rawPins.ids[identity]; ok && set.loaded {
		pins := append([]rawPin(nil), set.pins...)
		s.rawPins.mu.Unlock()
		return pins
	}
	s.rawPins.mu.Unlock()
	var stored []rawPin
	// Pin rows are never evicted and a slot is filled only after every slot
	// before it, so the first empty slot ends the scan.
	for slot := 0; slot < rawPinSlots; slot++ {
		value, _, hit := s.prefixCache.LookupReplacement(rawPinScope, rawPinKey(identity, slot))
		if !hit {
			break
		}
		if p, ok := decodeRawPin(value); ok {
			stored = append(stored, p)
		}
	}
	s.rawPins.add(identity, stored...)
	s.rawPins.mu.Lock()
	defer s.rawPins.mu.Unlock()
	set := s.rawPins.set(identity)
	set.loaded = true
	return append([]rawPin(nil), set.pins...)
}

// pinRaw records that the provider accepted body, the client's original bytes,
// after rejecting transformed. It pins only a conversation's own bytes, and
// only when the two differ inside what body caches; otherwise the next turn's
// substitutions already extend it.
func (s *Server) pinRaw(adapter providers.Adapter, meta providers.RequestMetadata, body, transformed []byte) {
	if s.prefixCache == nil {
		return // nothing was ever substituted, so raw already extends raw
	}
	components, cached, ok := cachedPrefix(adapter, meta, body)
	shared := sharedComponents(adapter)
	if !ok || cached <= shared {
		return
	}
	if sent, _, ok := cachedPrefix(adapter, meta, transformed); ok && len(sent) >= cached && digestComponents(sent[:cached]) == digestComponents(components[:cached]) {
		return
	}
	prefix := newPrefixDigests(components)
	identity := prefix.at(shared + 1)
	pin := rawPin{n: uint32(cached), digest: prefix.at(cached)}
	s.pinsFor(identity) // merge with the stored pins before adding this one
	s.rawPins.add(identity, pin)
	value := pin.encode()
	for slot := 0; slot < rawPinSlots; slot++ {
		stored, err := s.prefixCache.RememberReplacement(rawPinScope, rawPinKey(identity, slot), value, RawPinHandle)
		if err != nil || bytes.Equal(stored, value) {
			return
		}
	}
}

// rawPinned reports whether body must go out raw: it extends a raw pin and no
// longer prefix of it went out replaced (the longest lineage wins).
func (s *Server) rawPinned(adapter providers.Adapter, meta providers.RequestMetadata, body []byte) bool {
	if s.prefixCache == nil {
		return false
	}
	components, _, ok := cachedPrefix(adapter, meta, body)
	if !ok {
		return false
	}
	prefix := newPrefixDigests(components)
	pinned := s.rawPinCoverage(adapter, prefix)
	if pinned == 0 {
		return false
	}
	n, form := s.longestLineage(prefix, pinned)
	return n == 0 || form != lineageReplaced
}

// heldRawByStream reports whether the longest lineage body extends was cached
// raw by a PAYG stream, so the server-side retrieve path must not start
// compressing it.
func (s *Server) heldRawByStream(adapter providers.Adapter, meta providers.RequestMetadata, body []byte) bool {
	if s.prefixCache == nil {
		return false
	}
	components, _, ok := cachedPrefix(adapter, meta, body)
	if !ok {
		return false
	}
	_, form := s.longestLineage(newPrefixDigests(components), sharedComponents(adapter))
	return form == lineageStreamRaw
}

// rawPinCoverage returns how many leading components the longest raw pin a
// request extends covers, 0 when it extends none.
func (s *Server) rawPinCoverage(adapter providers.Adapter, prefix *prefixDigests) int {
	shared := sharedComponents(adapter)
	if s.prefixCache == nil || len(prefix.components) <= shared {
		return 0
	}
	n := 0
	for _, p := range s.pinsFor(prefix.at(shared + 1)) {
		if p.matches(prefix) {
			n = max(n, int(p.n))
		}
	}
	return n
}

func rawPinKey(identity [32]byte, slot int) []byte {
	return append(identity[:len(identity):len(identity)], byte(slot))
}

// lineages is the in-memory copy of the lineage rows, plus the prefixes the
// store was asked about and did not have (lineageAbsent), so a pinned
// conversation reads the store only for lengths it has not asked about.
type lineages struct {
	mu    sync.Mutex
	forms map[[32]byte]byte
	order [][32]byte
}

// lineageAbsent marks a prefix the store holds no lineage for.
const lineageAbsent = 0

// put records form for digest. The first real form wins; an absence is only
// a cached miss and gives way to it.
func (l *lineages) put(digest [32]byte, form byte) (recorded bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.forms == nil {
		l.forms = map[[32]byte]byte{}
	}
	have, ok := l.forms[digest]
	if ok && (have != lineageAbsent || form == lineageAbsent) {
		return false
	}
	l.forms[digest] = form
	if !ok {
		if l.order = append(l.order, digest); len(l.order) > lineageMemory {
			delete(l.forms, l.order[0])
			l.order = l.order[1:]
		}
	}
	return true
}

func (l *lineages) form(digest [32]byte) (byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	form, ok := l.forms[digest]
	return form, ok
}

// recordLineage records that an accepted request cached its prefix
// [:cached] (client components) in form. The first record of a prefix wins.
func (s *Server) recordLineage(adapter providers.Adapter, client [][]byte, cached int, form byte) {
	if s.prefixCache == nil || cached <= sharedComponents(adapter) {
		return
	}
	digest := digestComponents(client[:cached])
	if s.lineages.put(digest, form) {
		_, _ = s.prefixCache.RememberReplacement(lineageScope, digest[:], []byte{form}, lineageHandle)
	}
}

// longestLineage returns the length and form of the longest prefix of the
// request, longer than above, that an accepted request recorded as a lineage;
// 0 when there is none.
func (s *Server) longestLineage(prefix *prefixDigests, above int) (int, byte) {
	for n := len(prefix.components); n > above; n-- {
		digest := prefix.at(n)
		form, ok := s.lineages.form(digest)
		if !ok {
			form = lineageAbsent
			if value, handle, hit := s.prefixCache.LookupReplacement(lineageScope, digest[:]); hit && handle == lineageHandle && len(value) == 1 {
				form = value[0]
			}
			s.lineages.put(digest, form)
		}
		if form != lineageAbsent {
			return n, form
		}
	}
	return 0, lineageAbsent
}

// sharedComponents is how many leading cached-prefix components every
// conversation of an agent shares: system and tools in a CachedPrefixInspector
// split (Anthropic), everything outside the conversation array in the
// whole-prompt split. The next component opens the conversation itself.
func sharedComponents(adapter providers.Adapter) int {
	if _, ok := adapter.(CachedPrefixInspector); ok {
		return 2
	}
	return 1
}

// cachedPrefix splits a request into the components a provider prompt cache
// keys on, plus how many of them this request caches. Adapters that know their
// cache markers say so (Anthropic); the rest cache the whole prompt
// implicitly, so every component counts.
func cachedPrefix(adapter providers.Adapter, meta providers.RequestMetadata, body []byte) ([][]byte, int, bool) {
	if inspector, ok := adapter.(CachedPrefixInspector); ok {
		return inspector.CachedPrefixComponents(body, meta)
	}
	return wholePromptComponents(body)
}

// wholePromptComponents is the provider-agnostic split: everything outside the
// conversation array as one component, then each conversation item.
func wholePromptComponents(body []byte) ([][]byte, int, bool) {
	root, ok := gatewayRootObjectSpan(body)
	if !ok {
		return nil, 0, false
	}
	for _, field := range []string{"messages", "input", "contents"} {
		list, found := gatewayFindObjectField(body, root, field)
		if !found || list.start >= list.end || body[list.start] != '[' {
			continue
		}
		items, ok := gatewayArrayElements(body, list)
		if !ok {
			return nil, 0, false
		}
		rest := append(append([]byte(nil), body[:list.start]...), body[list.end:]...)
		out := [][]byte{frameComponent(rest)}
		for _, item := range items {
			out = append(out, frameComponent(body[item.start:item.end]))
		}
		return out, len(out), true
	}
	return [][]byte{frameComponent(body)}, 1, true
}

func frameComponent(b []byte) []byte {
	return append(binary.BigEndian.AppendUint64(nil, uint64(len(b))), b...)
}

// digestComponents hashes length-framed components, so no two different
// component lists share a digest.
func digestComponents(components [][]byte) [32]byte {
	h := sha256.New()
	for _, c := range components {
		_, _ = h.Write(c)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// prefixDigests hashes a request's components incrementally: at(n) is
// digestComponents(components[:n]), and each component is hashed once.
type prefixDigests struct {
	components [][]byte
	h          hash.Hash
	sums       [][32]byte
}

func newPrefixDigests(components [][]byte) *prefixDigests {
	return &prefixDigests{components: components, h: sha256.New()}
}

func (d *prefixDigests) at(n int) [32]byte {
	for len(d.sums) < n {
		_, _ = d.h.Write(d.components[len(d.sums)])
		var sum [32]byte
		d.h.Sum(sum[:0])
		d.sums = append(d.sums, sum)
	}
	return d.sums[n-1]
}
