package cachebench

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const (
	CorpusFormatLMCacheJSONL = "lmcache-jsonl"
	CorpusFormatHFRows       = "hf-rows"
	BasisCorpusSimulated     = "benchmark_public_corpus_simulated"
	maxCorpusGapSeconds      = 365 * 24 * 60 * 60
)

// CorpusLimits bounds retained public-corpus rows, sessions, and bytes.
type CorpusLimits struct {
	MaxRows               int
	MaxSessions           int
	MaxMessagesPerRequest int
	MaxRowBytes           int
	MaxMessageBytes       int
	MaxInputBytes         int64
	MaxRetainedBytes      int64
}

// DefaultCorpusLimits returns conservative local-import bounds.
func DefaultCorpusLimits() CorpusLimits {
	return CorpusLimits{
		MaxRows: 100_000, MaxSessions: 10_000, MaxMessagesPerRequest: 4_096,
		MaxRowBytes: 64 << 20, MaxMessageBytes: 16 << 20,
		MaxInputBytes: 1 << 30, MaxRetainedBytes: 1 << 30,
	}
}

// CorpusMetadata binds source name, license, and immutable revision.
type CorpusMetadata struct {
	Name     string `json:"name"`
	License  string `json:"license,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// CorpusToolFunction is normalized function-call payload from public corpus.
type CorpusToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CorpusToolCall is normalized tool invocation from public corpus.
type CorpusToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function CorpusToolFunction `json:"function"`
}

// CorpusMessage is one normalized agent message.
type CorpusMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content,omitempty"`
	ToolCalls  []CorpusToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// CorpusRow is one recorded request history and session-local gap.
type CorpusRow struct {
	RowIndex     int             `json:"row_index,omitempty"`
	SessionID    string          `json:"session_id"`
	Model        string          `json:"model"`
	Input        []CorpusMessage `json:"input"`
	OutputLength int             `json:"output_length"`
	PreGap       float64         `json:"pre_gap"`
}

// AgentCorpus is validated retained corpus plus content digest.
type AgentCorpus struct {
	Metadata CorpusMetadata
	Rows     []CorpusRow
	SHA256   string
	Bytes    int64
}

// CorpusSummary describes normalized corpus population and provenance.
type CorpusSummary struct {
	Name                           string         `json:"name"`
	License                        string         `json:"license,omitempty"`
	Revision                       string         `json:"revision,omitempty"`
	SHA256                         string         `json:"sha256"`
	Sessions                       int            `json:"sessions"`
	Requests                       int            `json:"requests"`
	Sources                        map[string]int `json:"sources"`
	MinTurns                       int            `json:"min_turns"`
	MedianTurns                    int            `json:"median_turns"`
	P95Turns                       int            `json:"p95_turns"`
	MaxTurns                       int            `json:"max_turns"`
	StrictPrefixExtensions         int            `json:"strict_prefix_extensions"`
	MutatedOrCompactedTransitions  int            `json:"mutated_or_compacted_transitions"`
	ColdStartCeilingRequestHitRate float64        `json:"cold_start_ceiling_request_hit_rate"`
	OpportunityRequestHitRate      float64        `json:"opportunity_request_hit_rate"`
	OpportunityTokenHitRate        float64        `json:"opportunity_token_hit_rate"`
	EstimatedInputTokens           int64          `json:"estimated_input_tokens"`
	EstimatedReusableTokens        int64          `json:"estimated_reusable_tokens"`
	RetainedBytes                  int64          `json:"retained_bytes"`
	TokenBasis                     string         `json:"token_basis"`
}

type lmcacheRowWire struct {
	SessionID    string          `json:"session_id"`
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	OutputLength int             `json:"output_length"`
	PreGap       float64         `json:"pre_gap"`
}

type hfRowWire struct {
	RowIndex int            `json:"row_idx"`
	Row      lmcacheRowWire `json:"row"`
}

// ReadAgentCorpus imports supported JSONL formats under explicit limits.
func ReadAgentCorpus(reader io.Reader, format string, metadata CorpusMetadata, limits CorpusLimits) (AgentCorpus, error) {
	if reader == nil {
		return AgentCorpus{}, errors.New("cachebench: nil corpus reader")
	}
	if !validBoundedText(metadata.Name, 512, false) || !validBoundedText(metadata.License, 256, true) || !validBoundedText(metadata.Revision, 512, true) {
		return AgentCorpus{}, errors.New("cachebench: invalid corpus metadata")
	}
	var err error
	limits, err = normalizedCorpusLimits(limits)
	if err != nil {
		return AgentCorpus{}, err
	}
	limited := &io.LimitedReader{R: reader, N: limits.MaxInputBytes + 1}
	var rows []CorpusRow
	switch format {
	case CorpusFormatLMCacheJSONL:
		rows, err = readLMCacheJSONL(limited, limits)
	case CorpusFormatHFRows:
		rows, err = readHFRows(limited, limits)
	default:
		return AgentCorpus{}, fmt.Errorf("cachebench: unsupported corpus format %q", format)
	}
	if limited.N <= 0 {
		return AgentCorpus{}, fmt.Errorf("cachebench: corpus input exceeds byte limit %d", limits.MaxInputBytes)
	}
	if err != nil {
		return AgentCorpus{}, err
	}
	if len(rows) == 0 {
		return AgentCorpus{}, errors.New("cachebench: empty corpus")
	}
	return AgentCorpus{Metadata: metadata, Rows: rows, SHA256: corpusDigest(rows), Bytes: corpusRetainedBytes(rows)}, nil
}

func normalizedCorpusLimits(limits CorpusLimits) (CorpusLimits, error) {
	defaults := DefaultCorpusLimits()
	if limits.MaxRows <= 0 {
		limits.MaxRows = defaults.MaxRows
	}
	if limits.MaxSessions <= 0 {
		limits.MaxSessions = defaults.MaxSessions
	}
	if limits.MaxMessagesPerRequest <= 0 {
		limits.MaxMessagesPerRequest = defaults.MaxMessagesPerRequest
	}
	if limits.MaxRowBytes <= 0 {
		limits.MaxRowBytes = defaults.MaxRowBytes
	}
	if limits.MaxMessageBytes <= 0 {
		limits.MaxMessageBytes = defaults.MaxMessageBytes
	}
	if limits.MaxRetainedBytes <= 0 {
		limits.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if limits.MaxInputBytes <= 0 {
		limits.MaxInputBytes = limits.MaxRetainedBytes
	}
	if limits.MaxRows > 1_000_000 || limits.MaxSessions > 1_000_000 || limits.MaxMessagesPerRequest > 100_000 || limits.MaxRowBytes > 256<<20 || limits.MaxMessageBytes > 128<<20 || limits.MaxInputBytes > 16<<30 || limits.MaxRetainedBytes > 16<<30 {
		return CorpusLimits{}, errors.New("cachebench: corpus limits exceed hard resource ceilings")
	}
	return limits, nil
}

func readLMCacheJSONL(reader io.Reader, limits CorpusLimits) ([]CorpusRow, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), limits.MaxRowBytes)
	rows := make([]CorpusRow, 0)
	sessions := map[string]bool{}
	var retainedBytes int64
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var wire lmcacheRowWire
		if err := decodeOneJSON(raw, &wire); err != nil {
			return nil, fmt.Errorf("cachebench: corpus line %d: %w", line, err)
		}
		row, err := validateCorpusWire(wire, line-1, limits)
		if err != nil {
			return nil, fmt.Errorf("cachebench: corpus line %d: %w", line, err)
		}
		if err := appendCorpusRow(&rows, sessions, &retainedBytes, row, limits); err != nil {
			return nil, fmt.Errorf("cachebench: corpus line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cachebench: read corpus: %w", err)
	}
	return rows, nil
}

func readHFRows(reader io.Reader, limits CorpusLimits) ([]CorpusRow, error) {
	decoder := json.NewDecoder(reader)
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("cachebench: Hugging Face rows root must be object")
	}
	rows := make([]CorpusRow, 0)
	sessions := map[string]bool{}
	var retainedBytes int64
	foundRows := false
	rootKeys := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("cachebench: read Hugging Face rows key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("cachebench: invalid Hugging Face rows key")
		}
		if rootKeys[key] {
			return nil, fmt.Errorf("cachebench: duplicate Hugging Face field %q", key)
		}
		rootKeys[key] = true
		if key != "rows" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return nil, fmt.Errorf("cachebench: skip Hugging Face field %q: %w", key, err)
			}
			continue
		}
		foundRows = true
		start, err := decoder.Token()
		if err != nil || start != json.Delim('[') {
			return nil, errors.New("cachebench: Hugging Face rows field must be array")
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return nil, fmt.Errorf("cachebench: decode Hugging Face row: %w", err)
			}
			if len(raw) > limits.MaxRowBytes {
				return nil, fmt.Errorf("cachebench: Hugging Face row exceeds %d bytes", limits.MaxRowBytes)
			}
			var wire hfRowWire
			if err := decodeOneJSON(raw, &wire); err != nil {
				return nil, fmt.Errorf("cachebench: decode Hugging Face row: %w", err)
			}
			row, err := validateCorpusWire(wire.Row, wire.RowIndex, limits)
			if err != nil {
				return nil, fmt.Errorf("cachebench: Hugging Face row %d: %w", wire.RowIndex, err)
			}
			if err := appendCorpusRow(&rows, sessions, &retainedBytes, row, limits); err != nil {
				return nil, fmt.Errorf("cachebench: Hugging Face row %d: %w", wire.RowIndex, err)
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, errors.New("cachebench: malformed Hugging Face rows array")
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("cachebench: malformed Hugging Face rows object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("cachebench: trailing JSON after Hugging Face rows object")
	}
	if !foundRows {
		return nil, errors.New("cachebench: Hugging Face response has no rows field")
	}
	return rows, nil
}

func decodeOneJSON(raw []byte, destination any) error {
	if !validUniqueJSONObject(raw) {
		return errors.New("duplicate or invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func validateCorpusWire(wire lmcacheRowWire, index int, limits CorpusLimits) (CorpusRow, error) {
	if !validBoundedText(wire.SessionID, 2048, false) || !validBoundedText(wire.Model, 512, false) {
		return CorpusRow{}, errors.New("session_id and model required")
	}
	if wire.OutputLength < 0 || math.IsNaN(wire.PreGap) || math.IsInf(wire.PreGap, 0) || wire.PreGap < 0 || wire.PreGap > maxCorpusGapSeconds {
		return CorpusRow{}, fmt.Errorf("output_length must be non-negative and pre_gap must be within 0..%d seconds", maxCorpusGapSeconds)
	}
	if len(wire.Input) == 0 || !json.Valid(wire.Input) {
		return CorpusRow{}, errors.New("input must be valid non-empty message array")
	}
	if len(wire.SessionID)+len(wire.Model)+len(wire.Input) > limits.MaxRowBytes {
		return CorpusRow{}, fmt.Errorf("row exceeds byte limit %d", limits.MaxRowBytes)
	}
	var messages []CorpusMessage
	if err := json.Unmarshal(wire.Input, &messages); err != nil {
		return CorpusRow{}, fmt.Errorf("decode input: %w", err)
	}
	if len(messages) == 0 || len(messages) > limits.MaxMessagesPerRequest {
		return CorpusRow{}, fmt.Errorf("message count %d outside 1..%d", len(messages), limits.MaxMessagesPerRequest)
	}
	for messageIndex, message := range messages {
		if err := validateCorpusMessage(message, limits); err != nil {
			return CorpusRow{}, fmt.Errorf("message %d: %w", messageIndex, err)
		}
	}
	return CorpusRow{
		RowIndex: index, SessionID: wire.SessionID, Model: wire.Model, Input: messages,
		OutputLength: wire.OutputLength, PreGap: wire.PreGap,
	}, nil
}

func validateCorpusMessage(message CorpusMessage, limits CorpusLimits) error {
	switch message.Role {
	case "system", "developer", "user", "assistant", "tool":
	default:
		return fmt.Errorf("unsupported role %q", message.Role)
	}
	if !validBoundedText(message.ToolCallID, 2048, true) || !validBoundedText(message.Name, 512, true) {
		return errors.New("invalid message identity")
	}
	if len(message.Content) == 0 && len(message.ToolCalls) == 0 {
		return errors.New("content or tool_calls required")
	}
	if len(message.Content) > limits.MaxMessageBytes || (len(message.Content) > 0 && !json.Valid(message.Content)) {
		return errors.New("content invalid or exceeds limit")
	}
	messageBytes := len(message.Content) + len(message.Role) + len(message.ToolCallID) + len(message.Name)
	if len(message.Content) > 0 {
		var content any
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return errors.New("content must decode")
		}
		if content != nil {
			if _, ok := content.(string); !ok {
				return errors.New("content must be string or null")
			}
		}
	}
	for _, call := range message.ToolCalls {
		if call.Type != "function" || !validBoundedText(call.ID, 2048, false) || !validBoundedText(call.Function.Name, 512, false) || !validUniqueJSONObject([]byte(call.Function.Arguments)) {
			return errors.New("tool call requires function type, id, function name, and JSON arguments")
		}
		messageBytes += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	if messageBytes > limits.MaxMessageBytes {
		return errors.New("message exceeds byte limit")
	}
	if message.Role == "tool" && message.ToolCallID == "" {
		return errors.New("tool message requires tool_call_id")
	}
	return nil
}

func appendCorpusRow(rows *[]CorpusRow, sessions map[string]bool, retainedBytes *int64, row CorpusRow, limits CorpusLimits) error {
	if len(*rows) >= limits.MaxRows {
		return fmt.Errorf("row limit %d exceeded", limits.MaxRows)
	}
	if !sessions[row.SessionID] {
		if len(sessions) >= limits.MaxSessions {
			return fmt.Errorf("session limit %d exceeded", limits.MaxSessions)
		}
		sessions[row.SessionID] = true
	}
	rowBytes := corpusRowRetainedBytes(row)
	if rowBytes > limits.MaxRetainedBytes-*retainedBytes {
		return fmt.Errorf("retained corpus byte limit %d exceeded", limits.MaxRetainedBytes)
	}
	*retainedBytes += rowBytes
	*rows = append(*rows, row)
	return nil
}

func corpusRetainedBytes(rows []CorpusRow) int64 {
	var total int64
	for _, row := range rows {
		total += corpusRowRetainedBytes(row)
	}
	return total
}

func corpusRowRetainedBytes(row CorpusRow) int64 {
	total := int64(len(row.SessionID) + len(row.Model))
	for _, message := range row.Input {
		total += int64(len(message.Role) + len(message.Content) + len(message.ToolCallID) + len(message.Name))
		for _, call := range message.ToolCalls {
			total += int64(len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments))
		}
	}
	return total
}

func corpusDigest(rows []CorpusRow) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	encoder.SetEscapeHTML(false)
	for _, row := range rows {
		_ = encoder.Encode(row)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// RunCorpus evaluates one validated public corpus across provider compilers.
func RunCorpus(ctx context.Context, engine *cacheengine.Engine, corpus AgentCorpus, providers []ProviderConfig, target Target) (Report, error) {
	if engine == nil {
		return Report{}, errors.New("cachebench: nil cache engine")
	}
	if err := validateTarget(target); err != nil {
		return Report{}, err
	}
	if len(corpus.Rows) == 0 || corpus.SHA256 == "" || strings.TrimSpace(corpus.Metadata.Name) == "" {
		return Report{}, errors.New("cachebench: invalid corpus")
	}
	if len(providers) == 0 {
		return Report{}, errors.New("cachebench: no providers")
	}
	if len(providers) > 1024 {
		return Report{}, errors.New("cachebench: provider population exceeds 1024")
	}
	counter := tokens.Default()
	cachedCounter := &corpusTokenCounter{Counter: counter, counts: map[[sha256.Size]byte]int{}}
	summary, err := analyzeCorpus(corpus, cachedCounter)
	if err != nil {
		return Report{}, err
	}
	scenario := Scenario{Name: "public-agent-corpus", Turns: len(corpus.Rows), Step: time.Second, AssumedTTL: 5 * time.Minute}
	report := baseReport(BasisCorpusSimulated, scenario, target, QualityEquivalence)
	report.Corpus = &summary
	report.Scenario.TokenBasis = counter.Name() + " estimate over normalized OpenAI messages"
	report.Scenario.Step = "corpus pre_gap"
	report.Scenario.AssumedTTL = "provider profile TTL"
	for _, provider := range providers {
		trace, buildErr := buildCorpusTrace(provider, corpus, cachedCounter)
		if buildErr != nil {
			return Report{}, buildErr
		}
		report.Providers = append(report.Providers, evaluateSimulatedTrace(ctx, engine, trace, target))
	}
	report.Overall = aggregateProviders(report.Providers, target)
	if report.Overall.GatePassed {
		report.Status = "pass"
	}
	report.EvidenceLimitations = []string{
		"public corpus replays recorded request shapes through deterministic provider-cache simulation; no provider request was sent",
		"o200k_base counts are local estimates, not original-model or provider-counted tokens",
		"model-visible equivalence proves metadata-only request transformation, not retained task quality",
		"cache capacity, provider eviction, throttling, server latency, and concurrent tenant pressure require live replay",
		"corpus result is benchmark evidence only, never production prevalence, invoice spend, or verified savings",
	}
	return report, nil
}

// BuildCorpusTrace converts one validated agent corpus into provider-native
// request bodies for simulation or authenticated external replay. Session
// partitions remain isolated unless caller explicitly changes trace assumption.
func BuildCorpusTrace(provider ProviderConfig, corpus AgentCorpus) (Trace, error) {
	if len(corpus.Rows) == 0 || corpus.SHA256 == "" || strings.TrimSpace(corpus.Metadata.Name) == "" {
		return Trace{}, errors.New("cachebench: invalid corpus")
	}
	counter := tokens.Default()
	return buildCorpusTrace(provider, corpus, &corpusTokenCounter{Counter: counter, counts: map[[sha256.Size]byte]int{}})
}

type corpusSession struct {
	ID   string
	Rows []CorpusRow
}

type corpusTokenCounter struct {
	tokens.Counter
	counts map[[sha256.Size]byte]int
}

func (c *corpusTokenCounter) count(raw []byte) ([sha256.Size]byte, int) {
	digest := sha256.Sum256(raw)
	if count, ok := c.counts[digest]; ok {
		return digest, count
	}
	count := c.Count(raw)
	c.counts[digest] = count
	return digest, count
}

func analyzeCorpus(corpus AgentCorpus, counter *corpusTokenCounter) (CorpusSummary, error) {
	sessions := map[string]*corpusSession{}
	order := make([]string, 0)
	for _, row := range corpus.Rows {
		session := sessions[row.SessionID]
		if session == nil {
			session = &corpusSession{ID: row.SessionID}
			sessions[row.SessionID] = session
			order = append(order, row.SessionID)
		}
		session.Rows = append(session.Rows, row)
	}
	if len(sessions) == 0 {
		return CorpusSummary{}, errors.New("cachebench: corpus has no sessions")
	}
	summary := CorpusSummary{
		Name: corpus.Metadata.Name, License: corpus.Metadata.License, Revision: corpus.Metadata.Revision,
		SHA256: corpus.SHA256, Sessions: len(sessions), Requests: len(corpus.Rows), Sources: map[string]int{},
		RetainedBytes: corpus.Bytes,
		TokenBasis:    counter.Name() + " estimate over canonical message JSON",
	}
	if summary.RetainedBytes == 0 {
		summary.RetainedBytes = corpusRetainedBytes(corpus.Rows)
	}
	turns := make([]int, 0, len(sessions))
	for _, id := range order {
		session := sessions[id]
		turns = append(turns, len(session.Rows))
		summary.Sources[corpusSource(id)]++
		var previous []PrefixSegment
		for index, row := range session.Rows {
			prefix, err := corpusPrefix(row.Input, counter)
			if err != nil {
				return CorpusSummary{}, fmt.Errorf("cachebench: session %q row %d: %w", id, index, err)
			}
			eligible := prefixTokens(prefix)
			summary.EstimatedInputTokens += int64(eligible)
			if index > 0 {
				reused := commonPrefixTokens(previous, prefix)
				summary.EstimatedReusableTokens += int64(reused)
				if reused > 0 {
					summary.OpportunityRequestHitRate++
				}
				if len(prefix) > len(previous) && samePrefix(previous, prefix) {
					summary.StrictPrefixExtensions++
				} else {
					summary.MutatedOrCompactedTransitions++
				}
			}
			previous = prefix
		}
	}
	sort.Ints(turns)
	summary.MinTurns = turns[0]
	summary.MedianTurns = percentileInt(turns, 0.50)
	summary.P95Turns = percentileInt(turns, 0.95)
	summary.MaxTurns = turns[len(turns)-1]
	if summary.Requests > 0 {
		summary.ColdStartCeilingRequestHitRate = float64(summary.Requests-summary.Sessions) / float64(summary.Requests)
		summary.OpportunityRequestHitRate /= float64(summary.Requests)
	}
	if summary.EstimatedInputTokens > 0 {
		summary.OpportunityTokenHitRate = float64(summary.EstimatedReusableTokens) / float64(summary.EstimatedInputTokens)
	}
	return summary, nil
}

func samePrefix(previous, current []PrefixSegment) bool {
	if len(current) < len(previous) {
		return false
	}
	for index := range previous {
		if previous[index] != current[index] {
			return false
		}
	}
	return true
}

func percentileInt(sortedValues []int, quantile float64) int {
	if len(sortedValues) == 0 {
		return 0
	}
	index := int(math.Ceil(quantile*float64(len(sortedValues)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sortedValues) {
		index = len(sortedValues) - 1
	}
	return sortedValues[index]
}

func corpusSource(sessionID string) string {
	if before, _, ok := strings.Cut(sessionID, "__"); ok && before != "" {
		return before
	}
	return "unknown"
}

func corpusPrefix(messages []CorpusMessage, counter *corpusTokenCounter) ([]PrefixSegment, error) {
	prefix := make([]PrefixSegment, 0, len(messages))
	for _, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		digest, count := counter.count(raw)
		prefix = append(prefix, PrefixSegment{ID: hex.EncodeToString(digest[:]), Tokens: count})
	}
	return prefix, nil
}

func buildCorpusTrace(provider ProviderConfig, corpus AgentCorpus, counter *corpusTokenCounter) (Trace, error) {
	started := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	requests := make([]TraceRequest, 0, len(corpus.Rows))
	requestNumber := map[string]int{}
	elapsed := map[string]time.Duration{}
	epochVersion := map[string]int{}
	stableIdentity := map[string]string{}
	for _, row := range corpus.Rows {
		requestNumber[row.SessionID]++
		if requestNumber[row.SessionID] > 1 {
			elapsed[row.SessionID] += time.Duration(row.PreGap * float64(time.Second))
		}
		body, err := corpusProviderBody(provider, row.Input)
		if err != nil {
			return Trace{}, fmt.Errorf("cachebench: session %q request %d: %w", row.SessionID, requestNumber[row.SessionID], err)
		}
		prefix, err := corpusPrefix(row.Input, counter)
		if err != nil {
			return Trace{}, err
		}
		stableCount := 0
		for _, message := range row.Input {
			if message.Role != "system" && message.Role != "developer" {
				break
			}
			stableCount++
		}
		identity, identityErr := corpusStableIdentity(row.Input, stableCount)
		if identityErr != nil {
			return Trace{}, identityErr
		}
		if epochVersion[row.SessionID] == 0 {
			epochVersion[row.SessionID] = 1
			stableIdentity[row.SessionID] = identity
		} else if stableIdentity[row.SessionID] != identity {
			epochVersion[row.SessionID]++
			stableIdentity[row.SessionID] = identity
		}
		epoch := row.SessionID + "#" + strconv.Itoa(epochVersion[row.SessionID])
		native := cacheengine.NativeRequest{
			Scope: "cachebench/public-corpus", Epoch: epoch, PartitionKey: row.SessionID,
			ExpectedRequestsPerMinute: 20, ExpectedCalls: 2,
			Provider: provider.Provider, Model: provider.Model, Region: provider.Region, Endpoint: provider.Endpoint,
			Body: body, RuntimeMode: "optimize", AuthMode: "payg", PrefixTokens: prefixTokens(prefix),
		}
		requests = append(requests, TraceRequest{
			ID: fmt.Sprintf("%s/%s/%04d", provider.Provider, row.SessionID, requestNumber[row.SessionID]),
			At: started.Add(elapsed[row.SessionID]), Native: native, Prefix: prefix, StableSegmentCount: stableCount,
			DeclaredInputTokens: prefixTokens(prefix), MaxOutputTokens: 256,
		})
	}
	sort.SliceStable(requests, func(i, j int) bool { return requests[i].At.Before(requests[j].At) })
	return Trace{
		Provider: provider,
		Scenario: Scenario{Name: corpus.Metadata.Name, Turns: len(requests), AssumedTTL: 5 * time.Minute, Step: time.Second},
		Requests: requests, TokenBasis: counter.Name() + " estimate over normalized OpenAI messages",
		TimingBasis: TimingPerPartition,
	}, nil
}

func corpusStableIdentity(messages []CorpusMessage, stableCount int) (string, error) {
	if len(messages) == 0 {
		return "", errors.New("cachebench: empty stable identity")
	}
	if stableCount == 0 {
		stableCount = 1
	}
	if stableCount > len(messages) {
		return "", errors.New("cachebench: invalid stable identity boundary")
	}
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	encoder.SetEscapeHTML(false)
	for _, message := range messages[:stableCount] {
		if err := encoder.Encode(message); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func corpusProviderBody(provider ProviderConfig, messages []CorpusMessage) ([]byte, error) {
	switch provider.Provider {
	case "openai":
		return json.Marshal(map[string]any{"model": provider.Model, "max_completion_tokens": 256, "messages": messages})
	case "anthropic":
		return anthropicCorpusBody(provider.Model, messages)
	case "bedrock":
		return bedrockCorpusBody(messages)
	case "gemini":
		return geminiCorpusBody(messages)
	default:
		return nil, fmt.Errorf("unsupported corpus provider %q", provider.Provider)
	}
}

func contentText(content json.RawMessage) (string, error) {
	if len(content) == 0 || bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text, nil
	}
	return "", errors.New("corpus provider conversion supports string or null content")
}

func anthropicCorpusBody(model string, messages []CorpusMessage) ([]byte, error) {
	var system []string
	converted := make([]any, 0, len(messages))
	for _, message := range messages {
		text, err := contentText(message.Content)
		if err != nil {
			return nil, err
		}
		switch message.Role {
		case "system", "developer":
			if text != "" {
				system = append(system, text)
			}
		case "assistant":
			blocks := make([]any, 0, len(message.ToolCalls)+1)
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			for _, call := range message.ToolCalls {
				var input any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
					return nil, err
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
			}
			converted = append(converted, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			converted = append(converted, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": text}}})
		default:
			converted = append(converted, map[string]any{"role": "user", "content": text})
		}
	}
	return json.Marshal(map[string]any{"model": model, "max_tokens": 256, "system": strings.Join(system, "\n\n"), "messages": converted})
}

func bedrockCorpusBody(messages []CorpusMessage) ([]byte, error) {
	var system []any
	converted := make([]any, 0, len(messages))
	for _, message := range messages {
		text, err := contentText(message.Content)
		if err != nil {
			return nil, err
		}
		switch message.Role {
		case "system", "developer":
			if text != "" {
				system = append(system, map[string]any{"text": text})
			}
		case "assistant":
			content := make([]any, 0, len(message.ToolCalls)+1)
			if text != "" {
				content = append(content, map[string]any{"text": text})
			}
			for _, call := range message.ToolCalls {
				var input any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
					return nil, err
				}
				content = append(content, map[string]any{"toolUse": map[string]any{"toolUseId": call.ID, "name": call.Function.Name, "input": input}})
			}
			converted = append(converted, map[string]any{"role": "assistant", "content": content})
		case "tool":
			converted = append(converted, map[string]any{"role": "user", "content": []any{map[string]any{"toolResult": map[string]any{"toolUseId": message.ToolCallID, "content": []any{map[string]any{"text": text}}}}}})
		default:
			converted = append(converted, map[string]any{"role": "user", "content": []any{map[string]any{"text": text}}})
		}
	}
	return json.Marshal(map[string]any{"system": system, "messages": converted, "inferenceConfig": map[string]any{"maxTokens": 256}})
}

func geminiCorpusBody(messages []CorpusMessage) ([]byte, error) {
	var system []string
	contents := make([]any, 0, len(messages))
	for _, message := range messages {
		text, err := contentText(message.Content)
		if err != nil {
			return nil, err
		}
		switch message.Role {
		case "system", "developer":
			if text != "" {
				system = append(system, text)
			}
		case "assistant":
			parts := make([]any, 0, len(message.ToolCalls)+1)
			if text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
			for _, call := range message.ToolCalls {
				var args any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					return nil, err
				}
				parts = append(parts, map[string]any{"functionCall": map[string]any{"name": call.Function.Name, "args": args}})
			}
			contents = append(contents, map[string]any{"role": "model", "parts": parts})
		case "tool":
			contents = append(contents, map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": message.Name, "response": map[string]any{"output": text}}}}})
		default:
			contents = append(contents, map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}})
		}
	}
	return json.Marshal(map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": strings.Join(system, "\n\n")}}},
		"contents":          contents, "generationConfig": map[string]any{"maxOutputTokens": 256},
	})
}
