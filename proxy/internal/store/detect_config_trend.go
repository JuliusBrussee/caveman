package store

import (
	"fmt"
	"sort"
	"time"
)

// detect_config_trend.go turns the config snapshot history into the one thing a
// static threshold can never give: a comparison against THIS user's own past.
//
// "Trim CLAUDE.md to 150 lines" is advice for a stranger. "Your project config
// grew 34% since 2026-07-14, all of it in one file" is about them, and it is the
// finding that makes re-running learn worth doing weekly. The history table
// already records a row per observed change, so this costs one query and no
// corpus walk.
//
// It reports GROWTH, not size — size is already the claude_md_weight sink's job,
// and repeating it here would double-count the same tokens in the ranking.

const (
	configTrendMinGrowthTokens = 400  // ignore drift
	configTrendMinGrowthPct    = 20.0 // and ignore noise
	configTrendMinObservations = 2
	configTrendMaxFiles        = 5
)

type configTrendRow struct {
	Scope        string
	Path         string
	Kind         string
	FirstTokens  int
	LastTokens   int
	FirstSeen    string
	LastSeen     string
	Observations int
}

func (r configTrendRow) growth() int { return r.LastTokens - r.FirstTokens }

func (r configTrendRow) growthPct() float64 {
	if r.FirstTokens <= 0 {
		return 0
	}
	return float64(r.LastTokens-r.FirstTokens) * 100 / float64(r.FirstTokens)
}

// configTrendRows reads per-file first and last observations inside the window.
// A file with a single observation has no trend and is omitted rather than
// reported as flat, which would imply a measurement nobody made.
func (s *Store) configTrendRows(since time.Time) ([]configTrendRow, error) {
	query := `SELECT scope, path, kind, COUNT(*) AS observations,
		    MIN(observed_at) AS first_seen, MAX(observed_at) AS last_seen
		  FROM config_snapshot_history
		  WHERE observed_at >= ?
		  GROUP BY scope, path, kind
		  HAVING observations >= ?`
	cutoff := "0000-01-01T00:00:00Z"
	if !since.IsZero() {
		cutoff = since.UTC().Format(time.RFC3339)
	}
	rows, err := s.db.Query(query, cutoff, configTrendMinObservations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []configTrendRow
	for rows.Next() {
		var row configTrendRow
		if err := rows.Scan(&row.Scope, &row.Path, &row.Kind, &row.Observations, &row.FirstSeen, &row.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		first, last, err := s.configTrendEndpoints(out[i])
		if err != nil {
			continue
		}
		out[i].FirstTokens, out[i].LastTokens = first, last
	}
	return out, nil
}

func (s *Store) configTrendEndpoints(row configTrendRow) (first, last int, err error) {
	err = s.db.QueryRow(`SELECT tokens FROM config_snapshot_history
		WHERE scope = ? AND path = ? AND kind = ? AND observed_at = ?
		ORDER BY id ASC LIMIT 1`, row.Scope, row.Path, row.Kind, row.FirstSeen).Scan(&first)
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRow(`SELECT tokens FROM config_snapshot_history
		WHERE scope = ? AND path = ? AND kind = ? AND observed_at = ?
		ORDER BY id DESC LIMIT 1`, row.Scope, row.Path, row.Kind, row.LastSeen).Scan(&last)
	if err != nil {
		return 0, 0, err
	}
	return first, last, nil
}

// configTrendSink names what grew. Paths ARE included here, unlike in the
// digest: this sink is for the user's own eyes and a growth finding without the
// filename is unactionable.
func configTrendSink(rows []configTrendRow, beh behaviorScan, turnsPerDay float64, spend *LearnSpend) []Sink {
	var grown []configTrendRow
	for _, row := range rows {
		if row.FirstTokens <= 0 || row.growth() < configTrendMinGrowthTokens || row.growthPct() < configTrendMinGrowthPct {
			continue
		}
		grown = append(grown, row)
	}
	if len(grown) == 0 {
		return nil
	}
	sort.Slice(grown, func(i, j int) bool {
		if grown[i].growth() != grown[j].growth() {
			return grown[i].growth() > grown[j].growth()
		}
		return grown[i].Path < grown[j].Path
	})
	if len(grown) > configTrendMaxFiles {
		grown = grown[:configTrendMaxFiles]
	}
	totalGrowth := 0
	var perDay int64
	bases := map[string]bool{}
	files := make([]map[string]any, 0, len(grown))
	for _, row := range grown {
		totalGrowth += row.growth()
		// Each file is charged at the turn rate of the sessions that load it,
		// matching its own weight finding.
		fileTurns, basis := configTurnsPerDay(row.Scope, row.Kind, row.Path, beh, turnsPerDay)
		perDay += rate(row.growth(), fileTurns)
		bases[basis] = true
		files = append(files, map[string]any{
			"scope": row.Scope, "path": row.Path, "kind": row.Kind,
			"tokens_before": row.FirstTokens, "tokens_after": row.LastTokens,
			"growth_tokens": row.growth(), "growth_pct": roundPct(row.growthPct()),
			"first_seen": row.FirstSeen, "last_seen": row.LastSeen,
			"turns_per_day_basis": basis,
		})
	}
	basis := "per_file"
	if len(bases) == 1 {
		basis = files[0]["turns_per_day_basis"].(string)
	}
	evidence := map[string]any{
		"files":               files,
		"growth_tokens_total": totalGrowth,
		"turns_per_day_basis": basis,
		"comparison":          "against this machine's own recorded config history, not a general guideline",
		"note":                "growth only; the current size of each file is reported by its own weight finding",
	}
	sink := Sink{
		SinkID: "config_growth",
		Title: fmt.Sprintf("Instructions loaded with every message grew by %s tokens across %s",
			humanTokens(int64(totalGrowth)), plural(len(grown), "file")),
		Class: classBehavioral, Basis: observedLocal, Framing: framingForward,
		TokensPerTurn:    int64(totalGrowth),
		TokensPerDayRate: perDay,
		Evidence:         evidence,
		Suggestion:       "Every token added here is sent again with every message of every session, so growth adds up fast. Check whether the new parts still earn their place.",
	}
	if spend != nil {
		if usd := spend.priceInputTokens(sink.TokensPerDayRate); usd > 0 {
			evidence["spend_usd_per_day"] = usd
		}
	}
	return []Sink{sink}
}
