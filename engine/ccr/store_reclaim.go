//go:build !js

package ccr

// Reclaim of the host recovery store (#1370). The page cap counts every page,
// not just payload, so a store can fill well below its payload budget; before
// this nothing ever removed a typed object, and every compression after that
// point was refused. A refused write now frees room under a retention policy
// and is tried once more.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Retention policy for typed objects. Nothing here runs until a write is
// refused because the store is full; a store with room keeps every object.
const (
	// archivedRetention: an object whose session ended (lifecycle archived)
	// stays recoverable at least this long after the session's last write. It
	// matches the 14 days the hooks keep a session's state, so a session that
	// can still be resumed keeps the ccr:// references in its transcript.
	archivedRetention = 14 * 24 * time.Hour
	// idleSessionRetention: a session that never reached session.end (so its
	// objects were never archived) expires once it has written nothing for
	// this long.
	idleSessionRetention = 30 * 24 * time.Hour
	// tombstoneRetention: how long an expired object id still answers
	// "expired" rather than "unknown".
	tombstoneRetention = 180 * 24 * time.Hour
	// reclaimTargetPercent: a reclaim stops once used pages and payload are at
	// or below this share of their limits, so the next writes do not each
	// trigger another reclaim.
	reclaimTargetPercent = 80
	reclaimBatchSize     = 256
	// reclaimTimeBudget bounds the time one refused write spends freeing
	// room; a later refusal continues where this one stopped.
	reclaimTimeBudget = 5 * time.Second
	// reclaimCooldown: after a reclaim that freed nothing, writes are refused
	// without another scan for this long.
	reclaimCooldown = 10 * time.Minute
)

const metaLastReclaim = "last_reclaim"

// ReclaimReport says what one reclaim freed. Every expired object id also
// stays in expired_objects, so a later retrieval reports it as expired.
type ReclaimReport struct {
	At                  time.Time `json:"at"`
	ConsolidatedObjects int       `json:"consolidated_objects"`
	ConsolidatedBytes   int64     `json:"consolidated_bytes"`
	ExpiredObjects      int       `json:"expired_objects"`
	ExpiredBytes        int64     `json:"expired_bytes"`
	ExpiredSessions     int       `json:"expired_sessions"`
	OldestExpired       string    `json:"oldest_expired_created_at,omitempty"`
	NewestExpired       string    `json:"newest_expired_created_at,omitempty"`
	// KeptOwners counts expired objects kept because a kept object reads
	// its bytes through data_ref.
	KeptOwners  int  `json:"kept_owners,omitempty"`
	ReachedGoal bool `json:"reached_goal"`
}

func (r ReclaimReport) freed() bool {
	return r.ConsolidatedObjects > 0 || r.ExpiredObjects > 0
}

// reclaimForWrite runs with mu held, after a write was refused for capacity.
// It reports whether room was freed, so the caller retries the write once.
func (s *Store) reclaimForWrite() bool {
	reason := s.refused.Load()
	if reason != refusalPageCap && reason != refusalPayloadBudget {
		return false
	}
	now := time.Now()
	if !s.futileReclaimAt.IsZero() && now.Sub(s.futileReclaimAt) < reclaimCooldown {
		return false
	}
	report, err := s.reclaim(now)
	if err != nil {
		if logger := s.log(); logger != nil && !errors.Is(err, ErrStoreDamaged) {
			logger.Warn("recovery store reclaim failed", "error", err)
		}
		if !report.freed() {
			s.futileReclaimAt = now
		}
		return report.freed()
	}
	if !report.freed() {
		s.futileReclaimAt = now
		if logger := s.log(); logger != nil {
			logger.Warn("recovery store is full and holds nothing it may expire; new recoveries pass through uncompressed",
				"path", s.path, "reason", refusalName(reason),
				"fix", "raise CAVEMAN_CCR_MAX_BYTES and restart caveman, or wait until archived sessions pass the retention floor")
		}
		return false
	}
	s.futileReclaimAt = time.Time{}
	return true
}

func refusalName(reason int32) string {
	switch reason {
	case refusalPageCap:
		return StorageFullPageCap
	case refusalPayloadBudget:
		return StorageFullPayloadBudget
	case refusalDiskFull:
		return StorageFullDiskFull
	}
	return ""
}

// reclaim frees room in a full store, in two steps, and stops as soon as the
// store is back under its target:
//
//  1. Duplicate full copies of one repository map that nothing points at
//     become references to one verified copy (#1023 deduplicated new puts
//     only). No object is deleted.
//  2. Typed objects past the retention policy are deleted, oldest first.
//     A full row that a kept row reads through data_ref is never deleted.
//
// Neither step runs on a store that fails PRAGMA quick_check: a write into a
// damaged b-tree can spread the damage. Blob recoveries are never expired,
// because an emitted request may reference any of them.
func (s *Store) reclaim(now time.Time) (ReclaimReport, error) {
	report := ReclaimReport{At: now.UTC()}
	if err := s.refuseDamaged(); err != nil {
		return report, err
	}
	if s.integrity.Load() == integrityUnknown {
		findings, err := quickCheck(s.db)
		if isCorrupt(err) {
			findings, err = []string{err.Error()}, nil
		}
		if err != nil {
			return report, fmt.Errorf("integrity check before reclaim: %w", err)
		}
		if findings != nil {
			s.markDamaged(findings)
			return report, ErrStoreDamaged
		}
		s.integrity.CompareAndSwap(integrityUnknown, integrityOK)
	}
	objects, bytes, err := s.consolidateRepositoryMaps()
	if err != nil {
		return report, fmt.Errorf("consolidate repository maps: %w", err)
	}
	report.ConsolidatedObjects, report.ConsolidatedBytes = objects, bytes
	if objects > 0 {
		if logger := s.log(); logger != nil {
			logger.Info("recovery store consolidated duplicate repository maps; every handle still returns the same bytes",
				"objects", objects, "bytes", bytes)
		}
	}
	expireErr := s.expireObjects(now, &report)
	// Whatever committed is reported, even when a later batch failed: no
	// object is deleted without a log line and a record status can show.
	if report.ExpiredObjects > 0 {
		if logger := s.log(); logger != nil {
			logger.Warn("recovery store expired old typed objects to make room; their ccr:// references now report expired",
				"objects", report.ExpiredObjects, "bytes", report.ExpiredBytes, "sessions", report.ExpiredSessions,
				"oldest_created_at", report.OldestExpired, "newest_created_at", report.NewestExpired,
				"kept_owners", report.KeptOwners)
		}
	}
	if need, err := s.reclaimNeed(); err == nil {
		report.ReachedGoal = need == 0
	}
	if report.freed() {
		if encoded, err := json.Marshal(report); err == nil {
			_ = s.setMeta(s.db, metaLastReclaim, string(encoded))
		}
	}
	if expireErr != nil {
		return report, fmt.Errorf("expire typed objects: %w", expireErr)
	}
	if _, err := s.db.Exec(`DELETE FROM expired_objects WHERE expired_at < ?`,
		now.Add(-tombstoneRetention).UTC().Format(time.RFC3339Nano)); err != nil && !isFull(err) {
		return report, fmt.Errorf("prune expired object ids: %w", err)
	}
	return report, nil
}

// reclaimNeed returns how many bytes must be freed to bring used pages and
// payload down to the reclaim target, or 0 when the store is already there.
func (s *Store) reclaimNeed() (int64, error) {
	var limit, pages, free, pageSize int64
	for _, p := range []struct {
		pragma string
		into   *int64
	}{{"max_page_count", &limit}, {"page_count", &pages}, {"freelist_count", &free}, {"page_size", &pageSize}} {
		if err := s.db.QueryRow(`PRAGMA ` + p.pragma).Scan(p.into); err != nil {
			return 0, err
		}
	}
	var need int64
	if target := limit * reclaimTargetPercent / 100; limit > 0 && pages-free > target {
		need = (pages - free - target) * pageSize
	}
	used, err := s.storageBytes(s.db)
	if err != nil {
		return 0, err
	}
	if target := s.maxBytes * reclaimTargetPercent / 100; used-target > need {
		need = used - target
	}
	return need, nil
}

// consolidateRepositoryMaps turns duplicate full copies of one map into
// references to a single copy whose bytes verify. A copy that another row
// already points at stays full, so no existing reference is re-pointed and no
// reference ever names another reference: a reader of any version that reads
// a row and then its owner gets the same bytes before and after.
func (s *Store) consolidateRepositoryMaps() (int, int64, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	hashes, err := queryStrings(tx,
		`SELECT content_hash FROM typed_objects
		 WHERE object_type = 'RepositoryMap' AND data_ref = ''
		 GROUP BY content_hash HAVING COUNT(*) > 1`)
	if err != nil {
		return 0, 0, err
	}
	var objects int
	var freed int64
	for _, hash := range hashes {
		copies, err := queryStrings(tx,
			`SELECT object_id FROM typed_objects
			 WHERE object_type = 'RepositoryMap' AND content_hash = ? AND data_ref = ''
			 ORDER BY created_at ASC, object_id ASC`, hash)
		if err != nil {
			return 0, 0, err
		}
		referenced := make(map[string]bool, len(copies))
		for _, id := range copies {
			used, err := isReferenced(tx, id)
			if err != nil {
				return 0, 0, err
			}
			referenced[id] = used
		}
		owner := ""
		for _, preferReferenced := range []bool{true, false} {
			for _, id := range copies {
				if owner != "" || (preferReferenced && !referenced[id]) {
					continue
				}
				ok, err := copyVerifies(tx, id, hash)
				if err != nil {
					return 0, 0, err
				}
				if ok {
					owner = id
				}
			}
		}
		if owner == "" {
			continue // no copy verifies; leave the group for repair
		}
		for _, id := range copies {
			if id == owner || referenced[id] {
				continue
			}
			var size int64
			if err := tx.QueryRow(`SELECT length(data) FROM typed_objects WHERE object_id = ?`, id).Scan(&size); err != nil {
				return 0, 0, err
			}
			result, err := tx.Exec(
				`UPDATE typed_objects SET data = x'', data_ref = ? WHERE object_id = ? AND data_ref = ''`, owner, id)
			if err != nil {
				return 0, 0, err
			}
			if n, _ := result.RowsAffected(); n > 0 {
				objects++
				freed += size
			}
		}
	}
	if objects == 0 {
		return 0, 0, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return objects, freed, nil
}

// referencedSQL and readersSQL repeat the data_ref index's own condition, so
// the planner can prove the partial index applies. Without it a bound
// parameter could be ”, and each lookup would read every row, walking each
// large object's overflow pages to reach data_ref, the last column.
const (
	referencedSQL = `SELECT 1 FROM typed_objects WHERE data_ref = ? AND data_ref != '' LIMIT 1`
	readersSQL    = `SELECT object_id FROM typed_objects WHERE data_ref = ? AND data_ref != ''`
)

func isReferenced(q querier, id string) (bool, error) {
	var one int
	err := q.QueryRow(referencedSQL, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func copyVerifies(q querier, id, hash string) (bool, error) {
	var data []byte
	if err := q.QueryRow(`SELECT data FROM typed_objects WHERE object_id = ?`, id).Scan(&data); err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	return "sha256:"+hex.EncodeToString(sum[:]) == hash, nil
}

func queryStrings(q querier, query string, args ...any) ([]string, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// expireCandidatesSQL lists the objects the retention policy allows to
// expire, oldest first: archived objects of a session whose last write is
// older than archivedRetention, and every object of a session whose last
// write is older than idleSessionRetention. A session's age is its last
// write, not each object's, so a long session that just ended keeps its early
// objects. It reads only columns stored before data, so it never walks a
// large object's overflow pages.
const expireCandidatesSQL = `SELECT o.object_id FROM typed_objects o
 JOIN (SELECT session_id, MAX(created_at) AS last_write FROM typed_objects GROUP BY session_id) s
   ON s.session_id = o.session_id
 WHERE (o.lifecycle = 'archived' AND s.last_write < ?) OR s.last_write < ?
 ORDER BY o.created_at ASC, o.object_id ASC`

// expireObjects deletes candidates, oldest first, in short write
// transactions until the store is under its target, the candidates run out,
// or the time budget ends.
func (s *Store) expireObjects(now time.Time, report *ReclaimReport) error {
	need, err := s.reclaimNeed()
	if err != nil || need == 0 {
		return err
	}
	candidates, err := queryStrings(s.db, expireCandidatesSQL,
		now.Add(-archivedRetention).UTC().Format(time.RFC3339Nano),
		now.Add(-idleSessionRetention).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	eligible := make(map[string]bool, len(candidates))
	for _, id := range candidates {
		eligible[id] = true
	}
	deadline := now.Add(reclaimTimeBudget)
	sessions := map[string]struct{}{}
	tombstones := true
	for next := 0; next < len(candidates) && need > 0 && time.Now().Before(deadline); {
		batch := candidates[next:min(next+reclaimBatchSize, len(candidates))]
		consumed, err := s.expireBatch(batch, eligible, need, now, tombstones, report, sessions)
		if isFull(err) && tombstones {
			// A batch of small rows can free no whole page, and then the
			// tombstones themselves need one. Expire without them.
			tombstones = false
			consumed, err = s.expireBatch(batch, eligible, need, now, tombstones, report, sessions)
		}
		if err != nil {
			return err
		}
		next += consumed
		if need, err = s.reclaimNeed(); err != nil {
			return err
		}
	}
	report.ExpiredSessions = len(sessions)
	return nil
}

// expireBatch deletes candidates from batch in order until the bytes it
// removed reach need, and returns how many candidates it consumed. A full row
// that other rows read through data_ref goes only together with all of those
// rows, and only when every one of them may expire too; otherwise it stays.
func (s *Store) expireBatch(batch []string, eligible map[string]bool, need int64, now time.Time, tombstones bool, report *ReclaimReport, sessions map[string]struct{}) (int, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	type row struct {
		id, created, session string
		size                 int64
	}
	var expired []row
	var freed int64
	kept := 0
	remove := func(id string) (bool, error) {
		var r row
		r.id = id
		err := tx.QueryRow(`SELECT length(data)+length(dependencies_json), created_at, session_id
		 FROM typed_objects WHERE object_id = ?`, id).Scan(&r.size, &r.created, &r.session)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // already gone: this batch or another process
		}
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM typed_objects WHERE object_id = ?`, id); err != nil {
			return false, err
		}
		expired = append(expired, r)
		freed += r.size
		return true, nil
	}
	consumed := 0
	for _, id := range batch {
		if freed >= need {
			break
		}
		consumed++
		readers, err := queryStrings(tx, readersSQL, id)
		if err != nil {
			return 0, err
		}
		keep := false
		for _, reader := range readers {
			keep = keep || !eligible[reader]
		}
		if keep {
			kept++
			continue
		}
		for _, reader := range readers {
			if _, err := remove(reader); err != nil {
				return 0, err
			}
		}
		if _, err := remove(id); err != nil {
			return 0, err
		}
	}
	if tombstones {
		expiredAt := now.UTC().Format(time.RFC3339Nano)
		for _, r := range expired {
			if _, err := tx.Exec(`INSERT INTO expired_objects (object_id, expired_at) VALUES (?, ?)
			 ON CONFLICT(object_id) DO NOTHING`, r.id, expiredAt); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	report.KeptOwners += kept
	for _, r := range expired {
		report.ExpiredObjects++
		report.ExpiredBytes += r.size
		sessions[r.session] = struct{}{}
		if report.OldestExpired == "" || r.created < report.OldestExpired {
			report.OldestExpired = r.created
		}
		if r.created > report.NewestExpired {
			report.NewestExpired = r.created
		}
	}
	return consumed, nil
}
