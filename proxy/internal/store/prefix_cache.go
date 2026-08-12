package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// prefixCacheMaxEntries bounds the replacement cache. The store keeps the most
// recently used entries and drops the rest; an evicted entry is a plain lookup
// miss, so the gateway forwards that message's original bytes from then on. That
// costs one prompt-cache rebuild and is byte-stable afterwards — never a
// half-applied prefix. Live blocks average a few KB, so the cap is a soft ceiling
// of tens of MB in ~/.caveman/caveman.db.
const prefixCacheMaxEntries = 10000

// LookupReplacement returns the replacement bytes this proxy previously emitted
// for these exact original bytes, plus the CCR handle they disclose. It implements
// the read half of gateway.PrefixCache and fails open: any miss or SQL error is
// reported as ok=false so the caller forwards the client's original bytes.
func (s *Store) LookupReplacement(scope string, original []byte) ([]byte, string, bool) {
	key := prefixCacheKey(scope, original)
	replacement, handle, ok := s.readReplacement(key)
	if !ok {
		return nil, "", false
	}
	// Touch for LRU eviction only. A failed touch changes nothing the caller can
	// observe this turn, so it is logged and swallowed rather than turned into a miss.
	if _, err := s.db.Exec(`UPDATE prefix_replacements SET last_used_at = ? WHERE original_sha256 = ?`, prefixCacheNow(), key); err != nil && s.logger != nil {
		s.logger.Warn("prefix replacement touch failed", "error", err)
	}
	return replacement, handle, true
}

// RememberReplacement durably records original→replacement and returns the
// authoritative bytes for that original. Storage is first-write-wins: if another
// in-flight request already stored a replacement for the same block, that one is
// returned and the caller forwards it, so two requests can never put two different
// prefixes on the wire for one logical message.
func (s *Store) RememberReplacement(scope string, original, replacement []byte, handle string) ([]byte, error) {
	if scope == "" || len(original) == 0 || len(replacement) == 0 || handle == "" {
		return nil, errors.New("prefix replacement: incomplete entry")
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
	stored, _, ok := s.readReplacement(key)
	if !ok {
		return nil, errors.New("prefix replacement: entry unreadable after write")
	}
	s.evictPrefixReplacements()
	return stored, nil
}

func (s *Store) readReplacement(key string) ([]byte, string, bool) {
	var handle string
	var replacement []byte
	row := s.db.QueryRow(`SELECT handle, replacement FROM prefix_replacements WHERE original_sha256 = ?`, key)
	switch err := row.Scan(&handle, &replacement); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", false
	case err != nil:
		// NOT a miss: the entry may well exist. The caller still has to fail safe and
		// forward the original bytes (there is nothing else it can send), so the real
		// guarantee comes from the store's WAL + busy_timeout DSN — log this distinctly
		// so contention that would flip an upstream prefix is visible, not silent.
		if s.logger != nil {
			s.logger.Warn("prefix replacement lookup errored (treated as a miss; upstream prefix may flip)", "error", err)
		}
		return nil, "", false
	}
	if handle == "" || len(replacement) == 0 {
		if s.logger != nil {
			s.logger.Warn("prefix replacement entry incomplete (treated as a miss)")
		}
		return nil, "", false
	}
	return replacement, handle, true
}

// evictPrefixReplacements keeps the table at prefixCacheMaxEntries, dropping the
// least recently used rows. Eviction failure is logged, never propagated: an
// oversized cache is a disk-space problem, not a correctness one.
func (s *Store) evictPrefixReplacements() {
	if _, err := s.db.Exec(
		`DELETE FROM prefix_replacements WHERE original_sha256 NOT IN (
		   SELECT original_sha256 FROM prefix_replacements ORDER BY last_used_at DESC, original_sha256 DESC LIMIT ?
		 )`, prefixCacheMaxEntries,
	); err != nil && s.logger != nil {
		s.logger.Warn("prefix replacement eviction failed", "error", err)
	}
}

// prefixCacheKey binds replacement to semantic plan/transform scope. Same bytes
// selected for a different locked transform must never replay an older winner.
func prefixCacheKey(scope string, original []byte) string {
	sum := sha256.Sum256(append(append([]byte(scope), 0), original...))
	return hex.EncodeToString(sum[:])
}

func prefixCacheNow() string { return time.Now().UTC().Format(storeTSLayout) }
