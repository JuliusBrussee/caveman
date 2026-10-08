package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// prefixCacheMaxEntries bounds the replacement cache. The store keeps the most
// recently used entries and drops the rest; an evicted entry is a plain lookup
// miss, so the gateway forwards that message's original bytes from then on. That
// costs one prompt-cache rebuild and is byte-stable afterwards — never a
// half-applied prefix. Compressed rows average a few KB and raw decisions carry
// no replacement at all; pixel rows hold base64 PNG parts, larger than the text
// they replace, and have their own cap below. Together that is a soft ceiling of
// a few hundred MB in ~/.caveman/caveman.db for text, plus at most
// prefixCachePixelMaxEntries pixel rows (tens of KB each).
const prefixCacheMaxEntries = 100000

// prefixCacheTouchInterval is how stale a row's last_used_at may get before a
// lookup hit refreshes it: eviction needs recency to the interval, not to the
// request.
const prefixCacheTouchInterval = 10 * time.Minute

// prefixCachePixelMaxEntries bounds the pixel rows inside that cap, least
// recently used first like the rest. One rendered block is one row.
const prefixCachePixelMaxEntries = 4096

// LookupReplacement returns the replacement bytes this proxy previously emitted
// for these exact original bytes, plus the CCR handle they disclose. It implements
// the read half of gateway.PrefixCache and fails open: any miss or SQL error is
// reported as ok=false so the caller forwards the client's original bytes.
func (s *Store) LookupReplacement(scope string, original []byte) ([]byte, string, bool) {
	key := prefixCacheKey(scope, original)
	replacement, handle, lastUsed, ok := s.readReplacement(key)
	if !ok {
		return nil, "", false
	}
	// Touch for LRU eviction only, and only a row older than the touch interval:
	// every block of every turn has a row, and a write per hit put one fsynced
	// commit per block on the request path. A failed touch changes nothing the
	// caller can observe this turn, so it is logged and swallowed rather than
	// turned into a miss.
	now := time.Now().UTC()
	if lastUsed < now.Add(-prefixCacheTouchInterval).Format(storeTSLayout) {
		if _, err := s.db.Exec(`UPDATE prefix_replacements SET last_used_at = ? WHERE original_sha256 = ?`, now.Format(storeTSLayout), key); err != nil && s.logger != nil {
			s.logger.Warn("prefix replacement touch failed", "error", err)
		}
	}
	return replacement, handle, true
}

// RememberReplacement durably records original→replacement and returns the
// authoritative bytes for that original. Storage is first-write-wins: if another
// in-flight request already stored a replacement for the same block, that one is
// returned and the caller forwards it, so two requests can never put two different
// prefixes on the wire for one logical message. A nil replacement under
// gateway.RawDecisionHandle records that the block went out raw; nil comes back
// whenever raw is the authoritative decision.
func (s *Store) RememberReplacement(scope string, original, replacement []byte, handle string) ([]byte, error) {
	raw := handle == gateway.RawDecisionHandle && len(replacement) == 0
	if scope == "" || len(original) == 0 || (len(replacement) == 0 && !raw) || handle == "" {
		return nil, errors.New("prefix replacement: incomplete entry")
	}
	if raw {
		replacement = []byte{} // the column is NOT NULL
	}
	key := prefixCacheKey(scope, original)
	now := prefixCacheNow()
	if _, err := s.db.Exec(
		`INSERT INTO prefix_replacements (original_sha256, handle, replacement, created_at, last_used_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(original_sha256) DO UPDATE SET last_used_at=excluded.last_used_at`,
		key, handle, replacement, now, now,
	); err != nil {
		return nil, fmt.Errorf("prefix replacement put: %w", err)
	}
	stored, _, _, ok := s.readReplacement(key)
	if !ok {
		return nil, errors.New("prefix replacement: entry unreadable after write")
	}
	s.evictPrefixReplacements()
	return stored, nil
}

func (s *Store) readReplacement(key string) (replacement []byte, handle, lastUsed string, ok bool) {
	row := s.db.QueryRow(`SELECT handle, replacement, last_used_at FROM prefix_replacements WHERE original_sha256 = ?`, key)
	switch err := row.Scan(&handle, &replacement, &lastUsed); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", "", false
	case err != nil:
		// NOT a miss: the entry may well exist. The caller still has to fail safe and
		// forward the original bytes (there is nothing else it can send), so the real
		// guarantee comes from the store's WAL + busy_timeout DSN — log this distinctly
		// so contention that would flip an upstream prefix is visible, not silent.
		if s.logger != nil {
			s.logger.Warn("prefix replacement lookup errored (treated as a miss; upstream prefix may flip)", "error", err)
		}
		return nil, "", "", false
	}
	if handle == gateway.RawDecisionHandle && len(replacement) == 0 {
		return nil, handle, lastUsed, true
	}
	if handle == "" || len(replacement) == 0 {
		if s.logger != nil {
			s.logger.Warn("prefix replacement entry incomplete (treated as a miss)")
		}
		return nil, "", "", false
	}
	return replacement, handle, lastUsed, true
}

// prefixCacheEvictEvery spaces eviction out: it runs on the first write and
// then once per this many, so the cap is soft by at most that many rows and the
// hot path does not re-count the table on every write.
const prefixCacheEvictEvery = 256

// evictPrefixReplacements keeps the table at prefixCacheMaxEntries, dropping the
// least recently used rows through the LRU index. Eviction failure is logged,
// never propagated: an oversized cache is a disk-space problem, not a
// correctness one.
func (s *Store) evictPrefixReplacements() {
	if (s.prefixWrites.Add(1)-1)%prefixCacheEvictEvery != 0 {
		return
	}
	// Raw pins never leave: nothing refreshes a pin row while the gateway
	// serves it from memory, and losing one re-substitutes a conversation over
	// the raw prefix the provider cached. They are rare (one per accepted raw
	// retry), so they do not count against the cap either.
	if _, err := s.db.Exec(
		`DELETE FROM prefix_replacements WHERE original_sha256 IN (
		   SELECT original_sha256 FROM prefix_replacements WHERE handle <> ?1
		   ORDER BY last_used_at ASC, original_sha256 ASC
		   LIMIT max(0, (SELECT COUNT(*) FROM prefix_replacements WHERE handle <> ?1) - ?2)
		 )`, gateway.RawPinHandle, prefixCacheMaxEntries,
	); err != nil && s.logger != nil {
		s.logger.Warn("prefix replacement eviction failed", "error", err)
	}
	if _, err := s.db.Exec(
		`DELETE FROM prefix_replacements WHERE original_sha256 IN (
		   SELECT original_sha256 FROM prefix_replacements WHERE handle = ?1
		   ORDER BY last_used_at ASC, original_sha256 ASC
		   LIMIT max(0, (SELECT COUNT(*) FROM prefix_replacements WHERE handle = ?1) - ?2)
		 )`, gateway.PixelHandle, prefixCachePixelMaxEntries,
	); err != nil && s.logger != nil {
		s.logger.Warn("prefix replacement pixel eviction failed", "error", err)
	}
}

// prefixCacheKey binds replacement to semantic plan/transform scope. Same bytes
// selected for a different locked transform must never replay an older winner.
func prefixCacheKey(scope string, original []byte) string {
	sum := sha256.Sum256(append(append([]byte(scope), 0), original...))
	return hex.EncodeToString(sum[:])
}

func prefixCacheNow() string { return time.Now().UTC().Format(storeTSLayout) }
