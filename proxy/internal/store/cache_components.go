package store

import (
	"context"
	"time"
)

// cacheComponentRetention is how long a request's cache component digest list
// stays available as evidence. It matches the 14 days the hooks keep session
// state (SESSION_TTL_MS). Expiry deletes only the list; the request row, its
// tokens, prices and every report built from them never change.
const cacheComponentRetention = 14 * 24 * time.Hour

// cacheComponentMaintenanceInterval is how often a running proxy repeats the
// migration of legacy lists and the expiry.
const cacheComponentMaintenanceInterval = 6 * time.Hour

// cacheComponentsMigratedKey records the highest requests.id whose legacy
// list the migration has handled. Rows above it are checked on every run:
// an older proxy (after a downgrade) still writes the legacy column.
const cacheComponentsMigratedKey = "cache_components_migrated_through"

// One migration batch is one write transaction. Small batches and a pause
// between them keep the request writes of the running proxy from waiting.
var (
	cacheComponentBatchRows  int64 = 500
	cacheComponentBatchPause       = 50 * time.Millisecond
)

// RunCacheComponentMaintenance moves legacy digest lists out of the requests
// rows and expires lists older than cacheComponentRetention, now and every
// cacheComponentMaintenanceInterval, until ctx ends. Only the long-running
// proxy calls it: a short command (stats, agent-evidence) must not pay for a
// migration of a large store.
func (s *Store) RunCacheComponentMaintenance(ctx context.Context) {
	for {
		s.maintainCacheComponents(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(cacheComponentMaintenanceInterval):
		}
	}
}

func (s *Store) maintainCacheComponents(ctx context.Context, now time.Time) {
	if err := s.migrateCacheComponents(ctx, now); err != nil {
		if ctx.Err() == nil && s.logger != nil {
			s.logger.Warn("local spend store: moving cache component lists failed; retrying later", "error", err)
		}
		return
	}
	if _, err := s.expireCacheComponents(ctx, now); err != nil && ctx.Err() == nil && s.logger != nil {
		s.logger.Warn("local spend store: expiring cache component lists failed; retrying later", "error", err)
	}
}

// migrateCacheComponents moves every legacy list above the mark, batch by
// batch, until none is left or ctx ends. It is idempotent and safe to stop at
// any point: a batch either commits whole, mark included, or not at all.
func (s *Store) migrateCacheComponents(ctx context.Context, now time.Time) error {
	cutoff := cacheComponentCutoff(now)
	for {
		done, err := s.migrateCacheComponentBatch(ctx, cutoff)
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cacheComponentBatchPause):
		}
	}
}

// migrateCacheComponentBatch handles the next cacheComponentBatchRows ids
// above the mark in one transaction: a non-empty legacy list of a row inside
// the retention window is copied to request_cache_components (an existing
// side row wins), every legacy value in the range becomes NULL, which frees
// its overflow pages for reuse, and the mark moves. A list outside the window
// is cleared without a copy, because expiry would delete it next. It reports
// done when no row above the mark is left.
func (s *Store) migrateCacheComponentBatch(ctx context.Context, cutoff string) (bool, error) {
	// The mark and the highest id are hints read before the transaction. A
	// stale mark only repeats work that is idempotent, and every statement in
	// the transaction writes, so it takes the write lock through busy_timeout
	// instead of failing to upgrade a read snapshot.
	var mark, maxID int64
	if err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT value FROM store_maintenance WHERE key = ?), 0),
		COALESCE((SELECT MAX(id) FROM requests), 0)`, cacheComponentsMigratedKey).Scan(&mark, &maxID); err != nil {
		return false, err
	}
	if mark >= maxID {
		return true, nil
	}
	hi := min(mark+cacheComponentBatchRows, maxID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_cache_components (request_id, digests)
		SELECT id, provider_cache_component_sha256 FROM requests
		 WHERE id > ? AND id <= ? AND provider_cache_component_sha256 <> ''
		   AND julianday(ts) >= julianday(?)
		ON CONFLICT(request_id) DO NOTHING`, mark, hi, cutoff); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET provider_cache_component_sha256 = NULL
		 WHERE id > ? AND id <= ? AND provider_cache_component_sha256 IS NOT NULL`, mark, hi); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO store_maintenance (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`, cacheComponentsMigratedKey, hi); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return hi >= maxID, nil
}

// expireCacheComponents deletes the lists of requests older than the retention
// window, and in the same transaction clears any legacy list of such a row the
// migration has not reached, so the reader cannot fall back to it. It returns
// how many side rows it deleted. Request rows are otherwise untouched.
func (s *Store) expireCacheComponents(ctx context.Context, now time.Time) (int64, error) {
	cutoff := cacheComponentCutoff(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM request_cache_components WHERE request_id IN (
		SELECT c.request_id FROM request_cache_components c JOIN requests r ON r.id = c.request_id
		 WHERE julianday(r.ts) < julianday(?))`, cutoff)
	if err != nil {
		return 0, err
	}
	expired, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET provider_cache_component_sha256 = NULL
		 WHERE id > COALESCE((SELECT value FROM store_maintenance WHERE key = ?), 0)
		   AND provider_cache_component_sha256 IS NOT NULL
		   AND julianday(ts) < julianday(?)`, cacheComponentsMigratedKey, cutoff); err != nil {
		return 0, err
	}
	return expired, tx.Commit()
}

func cacheComponentCutoff(now time.Time) string {
	return now.Add(-cacheComponentRetention).UTC().Format(time.RFC3339Nano)
}
