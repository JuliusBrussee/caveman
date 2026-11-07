//go:build !js

package ccr

// Integrity of the host recovery store (#1369). A store damaged before an
// upgrade stays damaged across it, so a new release checks it once, every
// write into a damaged b-tree is refused, and the store reports why.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

const (
	integrityUnknown int32 = iota
	integrityOK
	integrityDamaged
)

const metaIntegrityCheck = "integrity_check"

// SetLogger sets where the store reports maintenance: expired objects,
// consolidated maps and integrity damage. A nil logger silences it.
func (s *Store) SetLogger(logger *slog.Logger) { s.logger.Store(logger) }

func (s *Store) log() *slog.Logger { return s.logger.Load() }

// quickCheck runs PRAGMA quick_check. It returns nil for an intact database,
// else the first findings.
func quickCheck(q querier) ([]string, error) {
	rows, err := q.Query(`PRAGMA quick_check(20)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var findings []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		findings = append(findings, line)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(findings) == 1 && findings[0] == "ok" {
		return nil, nil
	}
	if len(findings) == 0 {
		return nil, errors.New("quick_check returned no result")
	}
	return findings, nil
}

// isCorrupt reports an error by which SQLite says the file itself is damaged.
func isCorrupt(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "malformed") || strings.Contains(text, "sqlite_corrupt") ||
		strings.Contains(text, "file is not a database") || strings.Contains(text, "sqlite_notadb")
}

// markDamaged makes every later write fail with ErrStoreDamaged. It is final
// for this Store: only a repaired file, opened by a new process, clears it.
func (s *Store) markDamaged(findings []string) {
	detail := strings.Join(findings, "; ")
	s.integrityDetail.Store(&detail)
	if s.integrity.Swap(integrityDamaged) != integrityDamaged {
		if logger := s.log(); logger != nil {
			logger.Error("recovery store is damaged; new recovery writes are refused until it is repaired",
				"path", s.path, "findings", detail, "fix", "stop caveman, then run caveman-proxy recovery-store repair")
		}
	}
}

func (s *Store) refuseDamaged() error {
	if s.integrity.Load() == integrityDamaged {
		return ErrStoreDamaged
	}
	return nil
}

// IntegrityReport is the result of one integrity check.
type IntegrityReport struct {
	Result    string    `json:"result"`
	Findings  []string  `json:"findings,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	// CheckedBy is the build label that ran the check.
	CheckedBy string `json:"checked_by,omitempty"`
	// Skipped is true when CheckIntegrityOnce found a passing check by the
	// same build and did not read the file again.
	Skipped bool `json:"skipped,omitempty"`
}

// CheckIntegrity runs PRAGMA quick_check on a separate read-only connection,
// so the store's own connection keeps serving while the whole file is read.
// A damaged result is final for this Store: every later write is refused.
func (s *Store) CheckIntegrity() (IntegrityReport, error) {
	findings, err := s.quickCheckSeparately()
	if err != nil {
		if isCorrupt(err) {
			findings, err = []string{err.Error()}, nil
		} else {
			return IntegrityReport{}, err
		}
	}
	report := IntegrityReport{Result: IntegrityOK, CheckedAt: time.Now().UTC()}
	if findings != nil {
		report.Result, report.Findings = IntegrityDamaged, findings
		s.markDamaged(findings)
		return report, nil
	}
	s.integrity.CompareAndSwap(integrityUnknown, integrityOK)
	return report, nil
}

// CheckIntegrityOnce checks the store unless the same build already found it
// intact. A new release therefore checks an older store once, on its first
// start (#1369), and later starts skip the full-file read. Only a passing
// check is recorded: nothing is written to a damaged store.
func (s *Store) CheckIntegrityOnce(label string) (IntegrityReport, error) {
	var recorded IntegrityReport
	raw, err := withStore(s, func() (string, error) { return s.metaValue(s.db, metaIntegrityCheck) })
	if err != nil {
		return IntegrityReport{}, err
	}
	if raw != "" && json.Unmarshal([]byte(raw), &recorded) == nil &&
		recorded.Result == IntegrityOK && label != "" && recorded.CheckedBy == label {
		s.integrity.CompareAndSwap(integrityUnknown, integrityOK)
		recorded.Skipped = true
		return recorded, nil
	}
	report, err := s.CheckIntegrity()
	if err != nil || report.Result != IntegrityOK {
		return report, err
	}
	report.CheckedBy = label
	encoded, err := json.Marshal(report)
	if err != nil {
		return report, nil
	}
	// Best effort: a full store refuses even this row, and the next start
	// simply checks again.
	_, _ = withStore(s, func() (struct{}, error) {
		return struct{}{}, s.setMeta(s.db, metaIntegrityCheck, string(encoded))
	})
	return report, nil
}

func (s *Store) quickCheckSeparately() ([]string, error) {
	if s.path == ":memory:" {
		return withStore(s, func() ([]string, error) { return quickCheck(s.db) })
	}
	s.mu.Lock()
	if err := s.checkGeneration(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	db, err := sql.Open("sqlite", readOnlyDSN(s.path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return quickCheck(db)
}

// readOnlyDSN opens path without the journal-mode pragma: a read-only
// connection cannot switch modes, and it must never create the file.
func readOnlyDSN(path string) string {
	u := url.URL{Scheme: "file", OmitHost: true, Path: path}
	return u.String() + "?mode=ro&_pragma=busy_timeout(5000)"
}

func (s *Store) metaValue(q querier, key string) (string, error) {
	var value string
	err := q.QueryRow(`SELECT value FROM ccr_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) setMeta(q querier, key, value string) error {
	if err := s.refuseDamaged(); err != nil {
		return err
	}
	_, err := q.Exec(`INSERT INTO ccr_meta (key, value) VALUES (?, ?)
	  ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
