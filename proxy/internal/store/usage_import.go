package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

type ImportSummary struct {
	Source         string `json:"source"`
	EventsImported int    `json:"events_imported"`
	QuotaImported  int    `json:"quota_imported"`
	Basis          string `json:"basis"`
}

func (s *Store) ImportCodex(root, sinceExpr string) (ImportSummary, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ImportSummary{}, err
		}
		root = filepath.Join(home, ".codex")
	}
	since := parseSince(sinceExpr)
	paths, err := codexPaths(root)
	if err != nil {
		return ImportSummary{}, err
	}
	var events []UsageEvent
	var quotas []QuotaEvent
	for _, path := range paths {
		es, qs, err := parseCodexFile(path, since)
		if err != nil {
			logStoreWarning(s.logger, "codex usage import skipped file", err)
			continue
		}
		events = append(events, es...)
		quotas = append(quotas, qs...)
	}
	n, err := s.InsertUsageEvents(events)
	if err != nil {
		return ImportSummary{}, err
	}
	q, err := s.InsertQuotaEvents(quotas)
	if err != nil {
		return ImportSummary{}, err
	}
	return ImportSummary{Source: "codex", EventsImported: n, QuotaImported: q, Basis: aggregateImportBasis(events)}, nil
}

func (s *Store) ImportClaude(root, sinceExpr string) (ImportSummary, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ImportSummary{}, err
		}
		root = filepath.Join(home, ".claude")
	}
	since := parseSince(sinceExpr)
	paths, err := claudePaths(root)
	if err != nil {
		return ImportSummary{}, err
	}
	var events []UsageEvent
	for _, path := range paths {
		es, err := parseClaudeFile(path, since)
		if err != nil {
			logStoreWarning(s.logger, "claude usage import skipped file", err)
			continue
		}
		events = append(events, es...)
	}
	n, err := s.InsertUsageEvents(events)
	if err != nil {
		return ImportSummary{}, err
	}
	return ImportSummary{Source: "claude", EventsImported: n, Basis: aggregateImportBasis(events)}, nil
}

func aggregateImportBasis(events []UsageEvent) string {
	if len(events) == 0 {
		return "estimated"
	}
	for _, event := range events {
		if event.Basis != observedLocal && event.Basis != "observed_provider" {
			return "estimated"
		}
	}
	return observedLocal
}

func (s *Store) RefreshClaudeUsageFromEnv() (ImportSummary, error) {
	raw := os.Getenv("CAVEMAN_CLAUDE_USAGE_JSON")
	if raw == "" {
		fetched, err := fetchClaudeUsageJSON()
		if err != nil {
			return ImportSummary{}, err
		}
		raw = string(fetched)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return ImportSummary{}, fmt.Errorf("parse Claude usage JSON: %w", err)
	}
	quotas := quotaEventsFromAny("anthropic", "claude_usage_link", "linked_api", time.Now().UTC().Format(time.RFC3339), v)
	n, err := s.InsertQuotaEvents(quotas)
	if err != nil {
		return ImportSummary{}, err
	}
	return ImportSummary{Source: "claude", QuotaImported: n, Basis: "linked_api"}, nil
}

func fetchClaudeUsageJSON() ([]byte, error) {
	sessionKey := os.Getenv("CAVEMAN_CLAUDE_SESSION_KEY")
	orgID := os.Getenv("CAVEMAN_CLAUDE_ORG_ID")
	if sessionKey == "" || orgID == "" {
		return nil, fmt.Errorf("Claude usage refresh needs CAVEMAN_CLAUDE_USAGE_JSON or CAVEMAN_CLAUDE_SESSION_KEY plus CAVEMAN_CLAUDE_ORG_ID")
	}
	if strings.Contains(orgID, "/") || strings.Contains(orgID, "..") {
		return nil, fmt.Errorf("invalid Claude organization id")
	}
	req, err := http.NewRequest("GET", "https://claude.ai/api/organizations/"+orgID+"/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("cookie", "sessionKey="+sessionKey)
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Claude usage request failed with HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func codexPaths(root string) ([]string, error) {
	paths, _, err := codexPathsUntil(root, nil)
	return paths, err
}

// codexPathsUntil preserves codexPaths' discovery contract while allowing the
// first-run behavior scan to stop a huge cold tree. Other import callers pass no
// deadline and retain their existing exhaustive walk.
func codexPathsUntil(root string, expired func() bool) ([]string, bool, error) {
	var paths []string
	timeBoxed := false
	stopped := func() bool {
		if expired == nil || !expired() {
			return false
		}
		timeBoxed = true
		return true
	}
	_ = filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, d os.DirEntry, err error) error {
		if stopped() {
			return fs.SkipAll
		}
		if err == nil && !d.IsDir() && strings.HasPrefix(filepath.Base(path), "rollout-") && strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	archived := filepath.Join(root, "archived_sessions")
	_ = filepath.WalkDir(archived, func(path string, d os.DirEntry, err error) error {
		if stopped() {
			return fs.SkipAll
		}
		if err != nil {
			return nil
		}
		if path != archived && d.IsDir() {
			return fs.SkipDir // archived_sessions/*.jsonl is intentionally one level
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if !stopped() {
		index := filepath.Join(root, "session_index.jsonl")
		if info, err := os.Stat(index); err == nil && !info.IsDir() {
			paths = append(paths, index)
		}
	}
	return uniqStrings(paths), timeBoxed, nil
}

func claudePaths(root string) ([]string, error) {
	var paths []string
	// projects/<slug>/*.jsonl is where real Claude Code session transcripts live,
	// carrying the per-turn usage block. This is the primary source.
	filepath.WalkDir(filepath.Join(root, "projects"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	// Legacy/secondary sources kept for back-compat (no usage block → estimated).
	if matches, _ := filepath.Glob(filepath.Join(root, "transcripts", "*.jsonl")); len(matches) > 0 {
		paths = append(paths, matches...)
	}
	filepath.WalkDir(filepath.Join(root, "tasks"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
			paths = append(paths, path)
		}
		return nil
	})
	return uniqStrings(paths), nil
}

func parseCodexFile(path string, since time.Time) ([]UsageEvent, []QuotaEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var events []UsageEvent
	var quotas []QuotaEvent
	var prevTotal int64
	lineNo := 0
	for sc.Scan() {
		lineNo++
		var obj map[string]any
		if err := json.Unmarshal(sc.Bytes(), &obj); err != nil {
			continue
		}
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		payload := asMap(obj["payload"])
		info := asMap(payload["info"])
		model := firstString(payload["model"], info["model"], obj["model"])
		provider := firstString(payload["model_provider"], info["model_provider"], obj["model_provider"])
		if provider == "" {
			provider = "openai"
		}
		usage := asMap(info["last_token_usage"])
		if len(usage) == 0 {
			usage = asMap(payload["last_token_usage"])
		}
		input := int64FromAny(usage["input_tokens"])
		output := int64FromAny(usage["output_tokens"])
		cached := int64FromAny(usage["cached_input_tokens"])
		cacheCreate := int64FromAny(usage["cache_creation_input_tokens"])
		reasoning := int64FromAny(usage["reasoning_output_tokens"])
		total := int64FromAny(usage["total_tokens"])
		basis := observedLocal
		bucketedUsage := true
		if total == 0 {
			total = int64FromAny(info["total_token_usage"])
			if total > 0 && prevTotal > 0 && total > prevTotal {
				// This fallback is an observed cumulative TOTAL with no provider
				// input/output/cache split. Preserve its delta as an estimate for
				// local volume, but never price it as if every token were input.
				input = total - prevTotal
				basis = "estimated"
				bucketedUsage = false
			}
			if total > 0 {
				prevTotal = total
			}
		}
		if anyPositiveInt64(input, output, cached, cacheCreate, reasoning) {
			// A local Codex transcript proves token volume, not how the turn was
			// paid for. It may be ChatGPT subscription traffic, PAYG, batch/flex,
			// or a regional endpoint. Pricing it at the standard API list rate would
			// manufacture spend, so cost stays the honest zero without explicit
			// provider billing evidence.
			meta := compactMeta(map[string]any{
				"cost_basis":    "unavailable_auth_and_service_tier",
				"token_buckets": map[bool]string{true: "provider", false: "unclassified_cumulative_delta"}[bucketedUsage],
			})
			events = append(events, UsageEvent{
				// Codex moves completed rollouts from sessions/ to
				// archived_sessions/. Identity must follow the immutable event bytes,
				// not the mutable filesystem path, or one session is counted twice.
				SourceKind: "codex_session", SourcePath: path, EventID: contentEventID(sc.Bytes()),
				Timestamp: tsOrNow(ts), AgentSlug: "codex", Provider: provider, Model: model,
				Requests: 1, InputTokens: input, OutputTokens: output, CachedInputTokens: cached, CacheCreationInputTokens: cacheCreate, ReasoningTokens: reasoning,
				TotalCostUSD: 0, Basis: basis, MetadataJSON: meta,
			})
		}
		if rl := payload["rate_limits"]; rl != nil {
			quotas = append(quotas, quotaEventsFromAny("openai", "codex_session", "local_session", tsOrNow(ts), rl)...)
		}
	}
	return events, quotas, sc.Err()
}

func parseClaudeFile(path string, since time.Time) ([]UsageEvent, error) {
	if strings.HasSuffix(path, ".jsonl") {
		return parseClaudeTranscript(path, since)
	}
	return parseClaudeTask(path, since)
}

func parseClaudeTranscript(path string, since time.Time) ([]UsageEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	lineNo := 0
	projects := strings.Contains(filepath.ToSlash(path), "/projects/")
	var events []UsageEvent
	for sc.Scan() {
		lineNo++
		var obj map[string]any
		if err := json.Unmarshal(sc.Bytes(), &obj); err != nil {
			continue
		}
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		// Real session transcripts (projects/) carry a per-turn usage block. Read
		// the real counts and emit observed_local — only for turns that have usage,
		// so we never double-count the running context across non-assistant lines.
		if ev, ok := claudeUsageEvent(obj, path, lineNo, sc.Bytes(), ts); ok {
			events = append(events, ev)
			continue
		}
		if projects {
			continue
		}
		// Legacy transcripts/ + tasks/ have no usage block: estimate per line.
		typ := firstString(obj["type"], obj["role"])
		if typ == "" {
			typ = "event"
		}
		estimatedTokens := int64(roughJSONSize(obj) / 4)
		if estimatedTokens == 0 {
			continue
		}
		requests := int64(0)
		if typ == "user" || typ == "tool_use" || typ == "tool_result" {
			requests = 1
		}
		events = append(events, UsageEvent{
			SourceKind: "claude_transcript", SourcePath: path, EventID: eventID(path, lineNo, sc.Bytes()),
			Timestamp: tsOrNow(ts), AgentSlug: "claude", Provider: "anthropic", Model: firstString(obj["model"], "unknown"),
			Requests: requests, InputTokens: estimatedTokens, Basis: "estimated",
			MetadataJSON: compactMeta(map[string]any{"event_type": typ}),
		})
	}
	return events, sc.Err()
}

// claudeUsageEvent builds an observed_local event from a real assistant turn's
// usage block. InputTokens follows the shared provider-neutral contract: total
// effective input including cache reads and cache creation. Both cache counters
// are retained as subsets for the cache breakdown and must not be added again.
func claudeUsageEvent(obj map[string]any, path string, lineNo int, raw []byte, ts time.Time) (UsageEvent, bool) {
	msg := asMap(obj["message"])
	usage := asMap(msg["usage"])
	if len(usage) == 0 {
		usage = asMap(obj["usage"])
	}
	if len(usage) == 0 {
		return UsageEvent{}, false
	}
	input := int64FromAny(usage["input_tokens"])
	cacheCreate := int64FromAny(usage["cache_creation_input_tokens"])
	cacheRead := int64FromAny(usage["cache_read_input_tokens"])
	output := int64FromAny(usage["output_tokens"])
	if !anyPositiveInt64(input, cacheCreate, cacheRead, output) {
		return UsageEvent{}, false
	}
	totalInput, ok := checkedNonNegativeSum(input, cacheCreate, cacheRead)
	if !ok {
		return UsageEvent{}, false
	}
	model := firstString(msg["model"], obj["model"], "unknown")
	return UsageEvent{
		SourceKind: "claude_transcript", SourcePath: path, EventID: eventID(path, lineNo, raw),
		Timestamp: tsOrNow(ts), AgentSlug: "claude", Provider: "anthropic", Model: model,
		Requests: 1, InputTokens: totalInput, OutputTokens: output, CachedInputTokens: cacheRead, CacheCreationInputTokens: cacheCreate,
		// Claude Code history likewise proves provider token counts but not whether
		// the turn was subscription, OAuth, PAYG, batch, or regional. Never turn a
		// private usage transcript into a fabricated API bill.
		TotalCostUSD: 0, Basis: observedLocal,
		MetadataJSON: compactMeta(map[string]any{"event_type": "assistant", "cost_basis": "unavailable_auth_and_service_tier"}),
	}, true
}

func parseClaudeTask(path string, since time.Time) ([]UsageEvent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	ts := timestampFromObject(obj)
	if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
		return nil, nil
	}
	size := roughJSONSize(obj)
	if size == 0 {
		return nil, nil
	}
	return []UsageEvent{{
		SourceKind: "claude_transcript", SourcePath: path, EventID: eventID(path, 1, raw),
		Timestamp: tsOrNow(ts), AgentSlug: "claude", Provider: "anthropic", Model: "unknown",
		Requests: 1, InputTokens: int64(size / 4), Basis: "estimated",
		MetadataJSON: compactMeta(map[string]any{"event_type": "task", "status": firstString(obj["status"])}),
	}}, nil
}

func quotaEventsFromAny(provider, sourceKind, basis, observedAt string, v any) []QuotaEvent {
	var out []QuotaEvent
	var walk func(any, string, string)
	walk = func(v any, path, planType string) {
		switch x := v.(type) {
		case map[string]any:
			if p := firstString(x["plan_type"], x["plan"], x["tier"]); p != "" {
				planType = p
			}
			used := float64FromAny(x["used_percentage"])
			if used == 0 {
				used = float64FromAny(x["used_pct"])
			}
			if used == 0 {
				used = float64FromAny(x["percent_used"])
			}
			if used > 0 {
				window := firstString(x["window"], x["limit_id"], x["period"], path)
				if window == "" {
					window = "unknown"
				}
				out = append(out, QuotaEvent{
					Provider: provider, PlanType: nonEmpty(planType, "unknown"), Window: safeWindow(window),
					UsedPct: used, ResetsAt: firstString(x["resets_at"], x["reset_at"], x["resetsAt"]),
					Basis: basis, SourceKind: sourceKind, ObservedAt: observedAt,
					MetadataJSON: compactMeta(map[string]any{"window": safeWindow(window)}),
				})
			}
			for k, child := range x {
				walk(child, nonEmpty(path, k), planType)
			}
		case []any:
			for _, child := range x {
				walk(child, path, planType)
			}
		}
	}
	walk(v, "", "")
	return out
}

var sinceClock = time.Now

func parseSince(expr string) time.Time {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return time.Time{}
	}
	if strings.HasSuffix(expr, "d") {
		n, _ := strconv.Atoi(strings.TrimSuffix(expr, "d"))
		if n > 0 {
			return sinceClock().UTC().Add(-time.Duration(n) * 24 * time.Hour)
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, expr); err == nil {
			return t
		}
	}
	return time.Time{}
}

func timestampFromObject(obj map[string]any) time.Time {
	for _, key := range []string{"timestamp", "ts", "created_at", "createdAt", "updated_at"} {
		if s := firstString(obj[key]); s != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
				if t, err := time.Parse(layout, s); err == nil {
					return t.UTC()
				}
			}
		}
	}
	return time.Time{}
}

func tsOrNow(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return t.UTC().Format(time.RFC3339)
}

func eventID(path string, line int, raw []byte) string {
	sum := sha256.Sum256(append([]byte(filepath.Clean(path)+":"+strconv.Itoa(line)+":"), raw...))
	return hex.EncodeToString(sum[:])
}

func contentEventID(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func firstString(values ...any) string {
	for _, v := range values {
		switch x := v.(type) {
		case string:
			if x != "" {
				return x
			}
		case fmt.Stringer:
			if s := x.String(); s != "" {
				return s
			}
		}
	}
	return ""
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int64:
		return nonNegativeInt64(x)
	case int:
		return nonNegativeInt64(int64(x))
	case float64:
		if x < 0 || math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x || x >= 9223372036854775808.0 {
			return 0
		}
		return int64(x)
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return 0
		}
		return nonNegativeInt64(n)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0
		}
		return nonNegativeInt64(n)
	default:
		return 0
	}
}

func float64FromAny(v any) float64 {
	valid := func(n float64) float64 {
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return n
	}
	switch x := v.(type) {
	case float64:
		return valid(x)
	case int:
		return valid(float64(x))
	case int64:
		return valid(float64(x))
	case json.Number:
		n, err := x.Float64()
		if err != nil {
			return 0
		}
		return valid(n)
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(x, "%")), 64)
		if err != nil {
			return 0
		}
		return valid(n)
	default:
		return 0
	}
}

// importedUsageCost converts provider usage's overlapping detail fields into
// non-overlapping priced buckets. OpenAI-family input includes cached reads;
// Anthropic input excludes cache reads and cache creation. Reasoning is passed
// separately only when the provider reports it outside visible output.
func importedUsageCost(provider string, price cost.Price, input, output, cached, cacheCreate, reasoning int64) float64 {
	input = nonNegativeInt64(input)
	output = nonNegativeInt64(output)
	cached = nonNegativeInt64(cached)
	cacheCreate = nonNegativeInt64(cacheCreate)
	reasoning = nonNegativeInt64(reasoning)
	if provider == "openai" || provider == "azure_openai" || provider == "openai_compatible" {
		input -= cached
		if input < 0 {
			input = 0
		}
	}
	// The shared usage contract treats output as inclusive of reasoning. Split
	// that total into disjoint visible/reasoning buckets so a provider-specific
	// reasoning rate is honored without charging the detail twice. A missing
	// dedicated rate means the normal output rate, never free reasoning.
	if reasoning > output {
		return 0
	}
	output -= reasoning
	reasoningRate := price.ReasoningPerMillion
	if reasoningRate == 0 {
		reasoningRate = price.OutputPerMillion
	}
	price.ReasoningPerMillion = reasoningRate
	return cost.EstimateUSD(price, cost.Usage{
		InputTokens:         int(input),
		OutputTokens:        int(output),
		CachedInputTokens:   int(cached),
		CacheCreationTokens: int(cacheCreate),
		ReasoningTokens:     int(reasoning),
	})
}

func anyPositiveInt64(values ...int64) bool {
	for _, value := range values {
		if value > 0 {
			return true
		}
	}
	return false
}

func checkedNonNegativeSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || value > math.MaxInt64-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

func roughJSONSize(v any) int {
	raw, _ := json.Marshal(v)
	return len(raw)
}

func compactMeta(v map[string]any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func safeWindow(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(" ", "_", "-", "_", ".", "_", "/", "_")
	s = replacer.Replace(s)
	if s == "" {
		return "unknown"
	}
	return s
}

func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

var _ = bytes.Equal
