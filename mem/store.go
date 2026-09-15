// Package mem is cavemem: durable, cross-session agent memory. It stores raw
// memories in a local SQLite database, recalls them with deterministic BM25
// behind a conservative threshold (it fails toward no recall), and compresses
// each recalled memory through the engine so the inferred token cost of
// injecting it is honest and the dropped detail stays recoverable via CCR.
// Everything it reports is `inferred`.
//
// Byte-safety: a memory's raw text is written to SQLite as the durable source of
// truth before any compression is attempted, so the engine can never cause a
// memory to be lost; compression happens only at recall time and is transient.
package mem

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/contextwindow"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const memSchema = `
CREATE TABLE IF NOT EXISTS memories (
  id TEXT PRIMARY KEY,
  text TEXT NOT NULL,
  created_at TEXT NOT NULL,
  valid_from TEXT NOT NULL,
  valid_until TEXT,
  supersedes TEXT,
  superseded_by TEXT
);`

// DefaultThreshold is the conservative recall floor: a hit scoring below it is
// dropped, so an off-topic query recalls nothing rather than injecting noise.
const DefaultThreshold = 0.1

// DefaultLimit is the default number of hits returned.
const DefaultLimit = 5

// DefaultTokenBudget bounds the total inferred tokens a single Recall may inject
// across all its hits. Without it, recall loaded and returned every matching
// memory whole — a single 2.5 MB memory came back as tokens_added=440,000 in one
// result. Callers override it per-call through RecallOptions.TokenBudget.
const DefaultTokenBudget = 2000

// UnlimitedTokenBudget disables Recall's aggregate token cap. It is explicit:
// zero retains the safe DefaultTokenBudget for existing Go callers. Public
// adapters map their documented token_budget=0 sentinel to this value.
const UnlimitedTokenBudget = -1

// MaxMemoryBytes is the largest single memory Remember accepts. A memory is a
// fact or note meant to be recalled and injected, not a file dump; anything
// larger fails closed with ErrMemoryTooLarge rather than being stored and later
// blowing the recall budget.
const MaxMemoryBytes = 256 * 1024

// ErrMemoryTooLarge is returned when new memory text exceeds MaxMemoryBytes.
// It carries the cave_ error idiom so callers surface cave_memory_too_large.
var ErrMemoryTooLarge = errors.New("cave_memory_too_large")

// tokenCounter is the shared offline BPE estimator (o200k_base). It is read-only
// and safe for concurrent use, matching the engine's inferred token accounting.
var tokenCounter = tokens.Default()

// Store is a cavemem instance: a SQLite memory table plus an engine (with its
// own CCR store) used to compress recalls.
type Store struct {
	db  *sql.DB
	eng *engine.Engine
	ccr *ccr.Store
}

// Options configures Open.
type Options struct {
	// Dir is the data directory; default ~/.caveman/mem (honoring CAVEMAN_HOME).
	Dir string
	// InMemory uses an ephemeral database for both memories and CCR (tests).
	InMemory bool
}

// Open opens (creating if needed) a cavemem store.
func Open(opts Options) (*Store, error) {
	memPath, ccrPath := ":memory:", ":memory:"
	if !opts.InMemory {
		dir := opts.Dir
		if dir == "" {
			dir = defaultDir()
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		memPath = filepath.Join(dir, "mem.db")
		ccrPath = filepath.Join(dir, "ccr.db")
	}
	canonicalMemPath, err := ccr.PrepareSQLitePathCanonical(memPath)
	if err != nil {
		return nil, fmt.Errorf("secure memories db: %w", err)
	}
	// Same embedded-SQLite-behind-a-multi-process-CLI discipline as the recovery
	// store: a single writer connection plus busy_timeout(5000)+WAL. Several
	// cavemem processes (and the MCP server) share one mem.db, and the JS client
	// fires Promise.all(facts.map(remember)); without this, concurrent writes
	// returned SQLITE_BUSY and were silently dropped. See ccr.SQLiteDSN.
	db, err := sql.Open("sqlite", ccr.SQLiteDSN(canonicalMemPath))
	if err != nil {
		return nil, fmt.Errorf("open memories db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// The cold-start migration is multi-statement DDL; under a fan-out of fresh
	// processes it can outlast a single busy_timeout, so retry it on SQLITE_BUSY
	// (runtime writes rely on busy_timeout alone). Both steps are idempotent.
	if err := ccr.RetryOnBusy(func() error { _, e := db.Exec(memSchema); return e }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate memories db: %w", err)
	}
	if err := ccr.RetryOnBusy(func() error { return migrateMemorySchema(db) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate memories db: %w", err)
	}
	store, err := ccr.Open(ccrPath)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open ccr: %w", err)
	}
	return &Store{db: db, eng: engine.New(store, nil), ccr: store}, nil
}

// Close releases the databases.
func (s *Store) Close() error {
	err := s.db.Close()
	if cerr := s.ccr.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// Memory is one stored memory.
type Memory struct {
	ID           string  `json:"id"`
	Text         string  `json:"text"`
	CreatedAt    string  `json:"created_at"`
	ValidFrom    string  `json:"valid_from"`
	ValidUntil   *string `json:"valid_until,omitempty"`
	Supersedes   string  `json:"supersedes,omitempty"`
	SupersededBy string  `json:"superseded_by,omitempty"`
}

// Remember stores text durably and returns it with a content-addressed id.
// Remembering identical current text twice is idempotent (same id, stored once).
// Text that exists only as an expired historical version is rejected: returning
// that row as success would claim it was remembered while Recall still hides it.
// The raw text is written before any other work, so a memory is never lost.
func (s *Store) Remember(text string) (Memory, error) {
	if strings.TrimSpace(text) == "" {
		return Memory{}, fmt.Errorf("cannot remember empty text")
	}
	if err := validateMemorySize(text); err != nil {
		return Memory{}, err
	}
	id := memID(text)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	// INSERT OR IGNORE keeps the original created_at on a repeat remember.
	// RetryOnBusy: under a fan-out of cavemem processes, a queued writer can
	// outlast a single busy_timeout (Windows file locking is the slowest case).
	if err := ccr.RetryOnBusy(func() error {
		_, e := s.db.Exec(
			`INSERT OR IGNORE INTO memories (id, text, created_at, valid_from) VALUES (?, ?, ?, ?)`,
			id, text, created, created,
		)
		return e
	}); err != nil {
		return Memory{}, fmt.Errorf("store memory: %w", err)
	}
	var stored Memory
	row := s.db.QueryRow(memorySelect+` WHERE id = ?`, id)
	if err := scanMemory(row, &stored); err != nil {
		return Memory{}, fmt.Errorf("read back memory: %w", err)
	}
	if stored.ValidUntil != nil {
		return Memory{}, fmt.Errorf("memory %s exists only as an expired historical version and cannot be re-remembered as current", stored.ID)
	}
	return stored, nil
}

// Supersede atomically replaces one current memory with a new current version.
// The old row remains durable for history and byte-exact audit, but normal recall
// excludes it. An already-expired or unknown id fails closed.
func (s *Store) Supersede(oldID, newText string) (Memory, error) {
	if strings.TrimSpace(oldID) == "" {
		return Memory{}, fmt.Errorf("supersede requires old memory id")
	}
	if strings.TrimSpace(newText) == "" {
		return Memory{}, fmt.Errorf("cannot supersede with empty text")
	}
	if err := validateMemorySize(newText); err != nil {
		return Memory{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Memory{}, fmt.Errorf("supersede begin: %w", err)
	}
	defer tx.Rollback()

	var old Memory
	if err := scanMemory(tx.QueryRow(memorySelect+` WHERE id = ? AND valid_until IS NULL`, oldID), &old); err != nil {
		if err == sql.ErrNoRows {
			return Memory{}, fmt.Errorf("memory %s is not current", oldID)
		}
		return Memory{}, fmt.Errorf("read current memory: %w", err)
	}
	if old.Text == newText {
		return Memory{}, fmt.Errorf("replacement text is identical to current memory")
	}
	newID := memID(newText)
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM memories WHERE id = ?`, newID).Scan(&exists); err != nil {
		return Memory{}, fmt.Errorf("check replacement memory: %w", err)
	}
	if exists > 0 {
		return Memory{}, fmt.Errorf("replacement memory %s already exists", newID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`INSERT INTO memories
		   (id, text, created_at, valid_from, supersedes)
		 VALUES (?, ?, ?, ?, ?)`,
		newID, newText, now, now, oldID,
	); err != nil {
		return Memory{}, fmt.Errorf("insert replacement memory: %w", err)
	}
	res, err := tx.Exec(
		`UPDATE memories
		    SET valid_until = ?, superseded_by = ?
		  WHERE id = ? AND valid_until IS NULL`,
		now, newID, oldID,
	)
	if err != nil {
		return Memory{}, fmt.Errorf("expire old memory: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Memory{}, fmt.Errorf("memory %s changed during supersede", oldID)
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, fmt.Errorf("supersede commit: %w", err)
	}
	return Memory{
		ID:         newID,
		Text:       newText,
		CreatedAt:  now,
		ValidFrom:  now,
		Supersedes: oldID,
	}, nil
}

func validateMemorySize(text string) error {
	if len(text) > MaxMemoryBytes {
		return fmt.Errorf("%w: memory is %d bytes, over the %d-byte cap", ErrMemoryTooLarge, len(text), MaxMemoryBytes)
	}
	return nil
}

// History returns the complete oldest-to-newest supersession chain containing
// id. Broken/cyclic lineage is rejected rather than returning partial history.
func (s *Store) History(id string) ([]Memory, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("history requires memory id")
	}
	current, err := s.memoryByID(id)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{current.ID: true}
	var before []Memory
	cursor := current
	for cursor.Supersedes != "" {
		prev, err := s.memoryByID(cursor.Supersedes)
		if err != nil {
			return nil, fmt.Errorf("broken supersession history at %s: %w", cursor.ID, err)
		}
		if seen[prev.ID] {
			return nil, fmt.Errorf("cyclic supersession history at %s", prev.ID)
		}
		seen[prev.ID] = true
		before = append(before, prev)
		cursor = prev
	}
	history := make([]Memory, 0, len(before)+1)
	for i := len(before) - 1; i >= 0; i-- {
		history = append(history, before[i])
	}
	history = append(history, current)
	cursor = current
	for cursor.SupersededBy != "" {
		next, err := s.memoryByID(cursor.SupersededBy)
		if err != nil {
			return nil, fmt.Errorf("broken supersession history at %s: %w", cursor.ID, err)
		}
		if seen[next.ID] {
			return nil, fmt.Errorf("cyclic supersession history at %s", next.ID)
		}
		seen[next.ID] = true
		history = append(history, next)
		cursor = next
	}
	return history, nil
}

// Forget deletes a memory by id and atomically repairs neighboring lineage
// pointers. Expired predecessors stay expired: deleting a current version means
// forgetting that fact, never silently resurrecting an older one.
func (s *Store) Forget(id string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("forget begin: %w", err)
	}
	defer tx.Rollback()

	var supersedes, supersededBy sql.NullString
	if err := tx.QueryRow(
		`SELECT supersedes, superseded_by FROM memories WHERE id = ?`, id,
	).Scan(&supersedes, &supersededBy); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("read forgotten memory: %w", err)
	}
	if supersedes.Valid {
		if _, err := tx.Exec(
			`UPDATE memories SET superseded_by = ? WHERE id = ?`,
			nullableLineageID(supersededBy), supersedes.String,
		); err != nil {
			return false, fmt.Errorf("repair forgotten predecessor: %w", err)
		}
	}
	if supersededBy.Valid {
		if _, err := tx.Exec(
			`UPDATE memories SET supersedes = ? WHERE id = ?`,
			nullableLineageID(supersedes), supersededBy.String,
		); err != nil {
			return false, fmt.Errorf("repair forgotten successor: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM memories WHERE id = ?`, id); err != nil {
		return false, fmt.Errorf("forget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("forget commit: %w", err)
	}
	return true, nil
}

func nullableLineageID(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

// Count returns the number of durable memory blocks without loading their
// contents. Callers omit this number on error rather than presenting zero.
func (s *Store) Count() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE valid_until IS NULL`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return count, nil
}

// Hit is one recall result: the recalled memory compressed for injection, with
// the inferred token cost, the match score, and a CCR handle to recover the full
// original when the compression dropped detail.
type Hit struct {
	ID             string  `json:"id"`
	Text           string  `json:"text"`
	Score          float64 `json:"score"`
	TokensAdded    int     `json:"tokens_added"`
	Basis          string  `json:"basis"`
	RecoveryHandle string  `json:"recovery_handle,omitempty"`
}

// RecallOptions configures Recall. A zero Threshold means DefaultThreshold; a
// zero Limit means DefaultLimit; a zero TokenBudget means DefaultTokenBudget.
// UnlimitedTokenBudget explicitly disables token packing while retaining Limit.
type RecallOptions struct {
	Limit     int
	Threshold float64
	// TokenBudget caps the total inferred tokens Recall injects across all hits.
	// Hits are packed greedily in BM25 rank order until the budget is exhausted;
	// a single top hit that alone exceeds the budget is returned as a compressed
	// head plus a CCR recovery handle, never as its whole body.
	TokenBudget int
}

// Recall returns the memories most relevant to query, ranked by BM25, filtered
// by the threshold, and compressed for injection. It fails toward no recall: a
// query with no term overlap (or that clears nothing above the threshold)
// returns an empty slice, never a guess.
func (s *Store) Recall(query string, opts RecallOptions) ([]Hit, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	budget := opts.TokenBudget
	unlimited := budget == UnlimitedTokenBudget
	if budget == 0 {
		budget = DefaultTokenBudget
	} else if budget < 0 && !unlimited {
		return nil, fmt.Errorf("token budget must be positive, zero (default), or UnlimitedTokenBudget")
	}

	all, err := s.all()
	if err != nil {
		return nil, err
	}
	scores := bm25Scores(query, all)

	type scored struct {
		mem   Memory
		score float64
	}
	ranked := make([]scored, 0, len(all))
	for _, m := range all {
		if sc := scores[m.ID]; sc >= threshold {
			ranked = append(ranked, scored{mem: m, score: sc})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].mem.ID < ranked[j].mem.ID // deterministic tie-break
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	if len(ranked) == 0 {
		return []Hit{}, nil
	}

	// Compress each candidate in rank order. The compressed form is what actually
	// gets injected, so its inferred token count is what the budget accounts for.
	type compressed struct {
		mem    Memory
		score  float64
		text   []byte
		tokens int
		handle string
	}
	comp := make([]compressed, 0, len(ranked))
	for _, r := range ranked {
		res, err := s.eng.Compress([]byte(r.mem.Text), engine.Options{Mode: engine.ModeCompress})
		if err != nil {
			// Engine failure is already fail-closed: its result retains the raw
			// bytes and the original token count. Preserve that accounting instead
			// of replacing it with zero, which would understate injected spend.
		}
		comp = append(comp, compressed{
			mem: r.mem, score: r.score,
			text: res.Output, tokens: res.TokensAfter, handle: res.RecoveryHandle,
		})
	}
	if unlimited {
		hits := make([]Hit, 0, len(comp))
		for _, c := range comp {
			hits = append(hits, Hit{
				ID:             c.mem.ID,
				Text:           string(c.text),
				Score:          c.score,
				TokensAdded:    c.tokens,
				Basis:          engine.BasisInferred,
				RecoveryHandle: c.handle,
			})
		}
		return hits, nil
	}

	// The single most relevant memory alone exceeds the whole budget. Return its
	// compressed head plus a CCR handle to the byte-exact original, rather than
	// injecting a 440k-token body (or dropping the most relevant hit entirely).
	if comp[0].tokens > budget {
		head, err := s.headHit(comp[0].mem, comp[0].text, comp[0].handle, comp[0].score, budget)
		if err != nil {
			return nil, err
		}
		return []Hit{head}, nil
	}

	// Greedy budget packing in BM25 rank order, delegated to the engine's
	// deterministic context packer so cavemem and the gateway share one budgeting
	// implementation. Priority encodes mem's rank (query left empty so the packer
	// adds no second ranking signal); Pack returns the fitting items in order.
	items := make([]contextwindow.Item, len(comp))
	byID := make(map[string]compressed, len(comp))
	for i, c := range comp {
		items[i] = contextwindow.Item{
			ID:       c.mem.ID,
			Text:     string(c.text),
			Tokens:   c.tokens,
			Priority: float64(len(comp)-i) * 1000,
		}
		byID[c.mem.ID] = c
	}
	packed := contextwindow.Pack("", items, contextwindow.Options{MaxTokens: budget})

	hits := make([]Hit, 0, len(packed.Items))
	for _, sel := range packed.Items {
		c := byID[sel.ID]
		hits = append(hits, Hit{
			ID:             c.mem.ID,
			Text:           string(c.text),
			Score:          c.score,
			TokensAdded:    c.tokens,
			Basis:          engine.BasisInferred,
			RecoveryHandle: c.handle,
		})
	}
	return hits, nil
}

// headHit builds a single recall hit for a memory whose compressed form exceeds
// the entire token budget: it truncates the compressed text to a head that fits
// and guarantees a CCR handle to the byte-exact original. When the engine passed
// the payload through without compressing (so it left no handle), the original
// is stored here — a head drops the tail, so the dropped detail must stay
// recoverable, upholding cavemem's reversible invariant.
func (s *Store) headHit(m Memory, compressedText []byte, handle string, score float64, budget int) (Hit, error) {
	head := truncateToTokens(compressedText, budget)
	if handle == "" {
		h, err := s.ccr.Put(ccr.Recovery{
			ContentType:  "text",
			Compressor:   "cavemem-head",
			TokensBefore: tokenCounter.Count([]byte(m.Text)),
			TokensAfter:  tokenCounter.Count(head),
			Original:     []byte(m.Text),
		})
		if err != nil {
			return Hit{}, fmt.Errorf("store recovery for oversized memory %s: %w", m.ID, err)
		}
		handle = h
	}
	return Hit{
		ID:             m.ID,
		Text:           string(head),
		Score:          score,
		TokensAdded:    tokenCounter.Count(head),
		Basis:          engine.BasisInferred,
		RecoveryHandle: handle,
	}, nil
}

// truncateToTokens returns the longest byte prefix of b whose inferred token
// count does not exceed budget, ending on a UTF-8 rune boundary. Recovery of the
// dropped tail is byte-exact through the hit's CCR handle.
func truncateToTokens(b []byte, budget int) []byte {
	if budget <= 0 {
		return nil
	}
	if tokenCounter.Count(b) <= budget {
		return b
	}
	lo, hi := 0, len(b)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if tokenCounter.Count(b[:mid]) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && lo < len(b) && !utf8.RuneStart(b[lo]) {
		lo--
	}
	return b[:lo]
}

// Recover returns the byte-exact original for a recall hit's recovery handle.
func (s *Store) Recover(handle string) ([]byte, error) {
	return s.eng.Retrieve(handle)
}

// all loads every memory.
func (s *Store) all() ([]Memory, error) {
	rows, err := s.db.Query(memorySelect + ` WHERE valid_until IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := scanMemory(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const memorySelect = `SELECT id, text, created_at, valid_from, valid_until, supersedes, superseded_by FROM memories`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemory(row rowScanner, memory *Memory) error {
	var validUntil, supersedes, supersededBy sql.NullString
	if err := row.Scan(
		&memory.ID,
		&memory.Text,
		&memory.CreatedAt,
		&memory.ValidFrom,
		&validUntil,
		&supersedes,
		&supersededBy,
	); err != nil {
		return err
	}
	if validUntil.Valid {
		memory.ValidUntil = &validUntil.String
	}
	memory.Supersedes = supersedes.String
	memory.SupersededBy = supersededBy.String
	return nil
}

func (s *Store) memoryByID(id string) (Memory, error) {
	var memory Memory
	if err := scanMemory(s.db.QueryRow(memorySelect+` WHERE id = ?`, id), &memory); err != nil {
		if err == sql.ErrNoRows {
			return Memory{}, fmt.Errorf("memory %s not found", id)
		}
		return Memory{}, fmt.Errorf("read memory %s: %w", id, err)
	}
	return memory, nil
}

// migrateMemorySchema upgrades pre-supersession stores in place. SQLite cannot
// add several columns in one statement, so each missing column is added
// independently and legacy created_at becomes valid_from.
func migrateMemorySchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(memories)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	additions := []struct {
		name string
		sql  string
	}{
		{"valid_from", `ALTER TABLE memories ADD COLUMN valid_from TEXT NOT NULL DEFAULT ''`},
		{"valid_until", `ALTER TABLE memories ADD COLUMN valid_until TEXT`},
		{"supersedes", `ALTER TABLE memories ADD COLUMN supersedes TEXT`},
		{"superseded_by", `ALTER TABLE memories ADD COLUMN superseded_by TEXT`},
	}
	for _, addition := range additions {
		if columns[addition.name] {
			continue
		}
		if _, err := db.Exec(addition.sql); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE memories SET valid_from = created_at WHERE valid_from = ''`); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_memories_current ON memories(valid_until)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_supersedes ON memories(supersedes)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_superseded_by ON memories(superseded_by)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func memID(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "mem_" + hex.EncodeToString(sum[:8])
}

func defaultDir() string {
	home := os.Getenv("CAVEMAN_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".caveman")
		} else {
			home = ".caveman"
		}
	}
	return filepath.Join(home, "mem")
}
