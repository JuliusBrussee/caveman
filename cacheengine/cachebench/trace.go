package cachebench

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

type agentMessage struct {
	ID     string
	Kind   string
	Text   string
	Tokens int
	Turn   int
}

// GenerateTrace builds deterministic provider-native real-agent-shaped requests.
func GenerateTrace(provider ProviderConfig, scenario Scenario) (Trace, error) {
	if err := validateScenario(provider, scenario); err != nil {
		return Trace{}, err
	}
	toolsTokens := scenario.StaticTokens / 4
	systemTokens := scenario.StaticTokens - toolsTokens
	toolsText := fixtureText("workspace tool contract", toolsTokens)
	systemText := fixtureText("stable repository policy", systemTokens)
	started := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	epoch := 1
	var history []agentMessage
	requests := make([]TraceRequest, 0, scenario.Turns)
	for turn := 0; turn < scenario.Turns; turn++ {
		if turn > 0 && scenario.CompactionEvery > 0 && turn%scenario.CompactionEvery == 0 {
			epoch++
			history = []agentMessage{{
				ID: fmt.Sprintf("summary-%02d", epoch), Kind: "user",
				Text:   fixtureText(fmt.Sprintf("compacted epoch %d task state", epoch), scenario.SummaryTokens),
				Tokens: scenario.SummaryTokens, Turn: turn,
			}}
		}
		user := agentMessage{
			ID: fmt.Sprintf("turn-%03d-user", turn+1), Kind: "user", Turn: turn + 1,
			Text:   fixtureText(fmt.Sprintf("turn %d inspect next repository slice", turn+1), scenario.UserTokens),
			Tokens: scenario.UserTokens,
		}
		history = append(history, user)
		body, err := providerBody(provider, toolsText, systemText, history)
		if err != nil {
			return Trace{}, err
		}
		prefix := []PrefixSegment{{ID: "tools-v1", Tokens: toolsTokens}, {ID: "system-v1", Tokens: systemTokens}}
		for _, message := range history {
			prefix = append(prefix, PrefixSegment{ID: message.ID, Tokens: message.Tokens})
		}
		epochID := fmt.Sprintf("%s-agent-epoch-%02d", provider.Provider, epoch)
		requests = append(requests, TraceRequest{
			ID: fmt.Sprintf("%s-%03d", provider.Provider, turn+1), At: started.Add(time.Duration(turn) * scenario.Step),
			Native: nativeRequest(provider, scenario, epochID, body), Prefix: prefix, StableSegmentCount: 2,
			DeclaredInputTokens: prefixTokens(prefix), MaxOutputTokens: 256,
		})
		history = append(history,
			agentMessage{
				ID: fmt.Sprintf("turn-%03d-assistant-tool", turn+1), Kind: "assistant_tool", Turn: turn + 1,
				Text: fmt.Sprintf("call workspace inspection for turn %d", turn+1), Tokens: scenario.AssistantTokens,
			},
			agentMessage{
				ID: fmt.Sprintf("turn-%03d-tool-result", turn+1), Kind: "tool_result", Turn: turn + 1,
				Text:   fixtureText(fmt.Sprintf("workspace result turn %d", turn+1), scenario.ToolResultTokens),
				Tokens: scenario.ToolResultTokens,
			},
		)
	}
	return Trace{
		Provider: provider, Scenario: scenario, Requests: requests,
		TokenBasis:  "deterministic declared fixture tokens; provider counts required for observed evidence",
		TimingBasis: TimingSynthetic,
	}, nil
}

func validateScenario(provider ProviderConfig, scenario Scenario) error {
	if strings.TrimSpace(provider.Provider) == "" || strings.TrimSpace(provider.Model) == "" || strings.TrimSpace(provider.Endpoint) == "" {
		return errors.New("cachebench: provider, model, and endpoint required")
	}
	if scenario.Turns < 2 || scenario.StaticTokens <= 0 || scenario.UserTokens <= 0 || scenario.AssistantTokens < 0 || scenario.ToolResultTokens < 0 || scenario.SummaryTokens < 0 {
		return errors.New("cachebench: invalid scenario token or turn count")
	}
	if scenario.CompactionEvery < 0 || scenario.Step <= 0 || scenario.AssumedTTL <= 0 {
		return errors.New("cachebench: invalid scenario timing")
	}
	return nil
}

func nativeRequest(provider ProviderConfig, scenario Scenario, epoch string, body []byte) cacheengine.NativeRequest {
	expectedCalls := scenario.Turns
	if scenario.CompactionEvery > 0 && expectedCalls > scenario.CompactionEvery {
		expectedCalls = scenario.CompactionEvery
	}
	return cacheengine.NativeRequest{
		Scope: "cachebench/real-agent", Epoch: epoch, PartitionKey: "agent-session-1",
		ExpectedRequestsPerMinute: 20, ExpectedCalls: expectedCalls,
		Provider: provider.Provider, Model: provider.Model, Region: provider.Region, Endpoint: provider.Endpoint,
		Body: body, RuntimeMode: "optimize", AuthMode: "payg", PrefixTokens: scenario.StaticTokens,
	}
}

func fixtureText(label string, declaredTokens int) string {
	words := declaredTokens
	if words > 32_768 {
		words = 32_768
	}
	if words < 1 {
		words = 1
	}
	return label + ": " + strings.Repeat("stable ", words)
}

func providerBody(provider ProviderConfig, toolsText, systemText string, history []agentMessage) ([]byte, error) {
	var body map[string]any
	switch provider.Provider {
	case "anthropic":
		messages := make([]any, 0, len(history))
		for _, message := range history {
			messages = append(messages, anthropicMessage(message))
		}
		body = map[string]any{
			"model": provider.Model, "max_tokens": 256, "system": systemText,
			"tools":    []any{map[string]any{"name": "workspace", "description": toolsText, "input_schema": map[string]any{"type": "object"}}},
			"messages": messages,
		}
	case "openai":
		messages := []any{map[string]any{"role": "system", "content": systemText}}
		for _, message := range history {
			messages = append(messages, openAIMessage(message))
		}
		body = map[string]any{
			"model": provider.Model, "max_completion_tokens": 256,
			"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "workspace", "description": toolsText, "parameters": map[string]any{"type": "object"}}}},
			"messages": messages,
		}
	case "bedrock":
		messages := make([]any, 0, len(history))
		for _, message := range history {
			messages = append(messages, bedrockMessage(message))
		}
		body = map[string]any{
			"system":     []any{map[string]any{"text": systemText}},
			"toolConfig": map[string]any{"tools": []any{map[string]any{"toolSpec": map[string]any{"name": "workspace", "description": toolsText, "inputSchema": map[string]any{"json": map[string]any{"type": "object"}}}}}},
			"messages":   messages, "inferenceConfig": map[string]any{"maxTokens": 256},
		}
	case "gemini":
		contents := make([]any, 0, len(history))
		for _, message := range history {
			contents = append(contents, geminiMessage(message))
		}
		body = map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": systemText}}},
			"tools":             []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "workspace", "description": toolsText, "parameters": map[string]any{"type": "object"}}}}},
			"contents":          contents, "generationConfig": map[string]any{"maxOutputTokens": 256},
		}
	default:
		return nil, fmt.Errorf("cachebench: unsupported trace provider %q", provider.Provider)
	}
	return json.Marshal(body)
}

func anthropicMessage(message agentMessage) map[string]any {
	switch message.Kind {
	case "assistant_tool":
		return map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": fmt.Sprintf("call-%03d", message.Turn), "name": "workspace", "input": map[string]any{},
		}}}
	case "tool_result":
		return map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": fmt.Sprintf("call-%03d", message.Turn), "content": message.Text,
		}}}
	default:
		return map[string]any{"role": "user", "content": message.Text}
	}
}

func openAIMessage(message agentMessage) map[string]any {
	switch message.Kind {
	case "assistant_tool":
		return map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"id": fmt.Sprintf("call-%03d", message.Turn), "type": "function",
			"function": map[string]any{"name": "workspace", "arguments": "{}"},
		}}}
	case "tool_result":
		return map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("call-%03d", message.Turn), "content": message.Text}
	default:
		return map[string]any{"role": "user", "content": message.Text}
	}
}

func bedrockMessage(message agentMessage) map[string]any {
	switch message.Kind {
	case "assistant_tool":
		return map[string]any{"role": "assistant", "content": []any{map[string]any{"toolUse": map[string]any{
			"toolUseId": fmt.Sprintf("call-%03d", message.Turn), "name": "workspace", "input": map[string]any{},
		}}}}
	case "tool_result":
		return map[string]any{"role": "user", "content": []any{map[string]any{"toolResult": map[string]any{
			"toolUseId": fmt.Sprintf("call-%03d", message.Turn), "content": []any{map[string]any{"text": message.Text}},
		}}}}
	default:
		return map[string]any{"role": "user", "content": []any{map[string]any{"text": message.Text}}}
	}
}

func geminiMessage(message agentMessage) map[string]any {
	switch message.Kind {
	case "assistant_tool":
		return map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": "workspace", "args": map[string]any{}}}}}
	case "tool_result":
		return map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "workspace", "response": map[string]any{"output": message.Text}}}}}
	default:
		return map[string]any{"role": "user", "parts": []any{map[string]any{"text": message.Text}}}
	}
}

// WriteTraceJSONL writes strict replayable trace v3 records.
func WriteTraceJSONL(writer io.Writer, trace Trace) error {
	if err := validateTrace(trace); err != nil {
		return err
	}
	buffered := bufio.NewWriter(writer)
	encoder := json.NewEncoder(buffered)
	for _, request := range trace.Requests {
		bodySHA := sha256.Sum256(request.Native.Body)
		record := TraceRecord{
			Schema: TraceSchema, RequestID: request.ID, At: request.At.Format(time.RFC3339Nano),
			Provider: request.Native.Provider, Model: request.Native.Model, Region: request.Native.Region,
			Endpoint: request.Native.Endpoint, Epoch: request.Native.Epoch, Scope: request.Native.Scope,
			TokenBasis: trace.TokenBasis, TimingBasis: trace.TimingBasis,
			PartitionKey: request.Native.PartitionKey, ExpectedRPM: request.Native.ExpectedRequestsPerMinute,
			ExpectedCalls: request.Native.ExpectedCalls, RuntimeMode: request.Native.RuntimeMode, AuthMode: request.Native.AuthMode,
			PrefixTokens: request.Native.PrefixTokens, DeclaredInputTokens: request.DeclaredInputTokens,
			MaxOutputTokens: request.MaxOutputTokens, Prefix: request.Prefix,
			StableSegmentCount: request.StableSegmentCount, Body: json.RawMessage(request.Native.Body),
			BodySHA256: fmt.Sprintf("%x", bodySHA[:]),
		}
		if _, err := record.NativeRequest(); err != nil {
			return err
		}
		if !requestBudgetMatchesBody(record) {
			return fmt.Errorf("cachebench: request %q output ceiling does not match provider body", record.RequestID)
		}
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return buffered.Flush()
}

// TraceReadLimits bounds JSONL decoding before replay preflight.
type TraceReadLimits struct {
	MaxLineBytes int
	MaxRecords   int
	MaxBodyBytes int
}

// DefaultTraceReadLimits supports public-corpus traces while bounding retained memory.
func DefaultTraceReadLimits() TraceReadLimits {
	return TraceReadLimits{MaxLineBytes: 96 << 20, MaxRecords: 100_000, MaxBodyBytes: 64 << 20}
}

// ReadTraceJSONL reads trace records using conservative default resource limits.
func ReadTraceJSONL(reader io.Reader) ([]TraceRecord, error) {
	return ReadTraceJSONLWithLimits(reader, DefaultTraceReadLimits())
}

// ReadTraceJSONLWithLimits reads strict trace JSONL under explicit resource limits.
func ReadTraceJSONLWithLimits(reader io.Reader, limits TraceReadLimits) ([]TraceRecord, error) {
	if limits.MaxLineBytes <= 0 || limits.MaxLineBytes > 512<<20 || limits.MaxRecords <= 0 || limits.MaxRecords > 1_000_000 || limits.MaxBodyBytes <= 0 || limits.MaxBodyBytes > 256<<20 {
		return nil, errors.New("cachebench: invalid trace read limits")
	}
	scanner := bufio.NewScanner(reader)
	initial := 64 * 1024
	if limits.MaxLineBytes < initial {
		initial = limits.MaxLineBytes
	}
	scanner.Buffer(make([]byte, initial), limits.MaxLineBytes)
	seen := map[string]bool{}
	var records []TraceRecord
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if len(records) >= limits.MaxRecords {
			return nil, fmt.Errorf("cachebench: trace exceeds record limit %d", limits.MaxRecords)
		}
		if !validUniqueJSONObject(raw) {
			return nil, fmt.Errorf("cachebench: trace line %d: duplicate or invalid JSON", line)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var record TraceRecord
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("cachebench: trace line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("cachebench: trace line %d: trailing JSON", line)
		}
		if record.Schema != TraceSchema && record.Schema != TraceSchemaV2 && record.Schema != TraceSchemaV1 || !validBoundedText(record.RequestID, 512, false) || seen[record.RequestID] {
			return nil, fmt.Errorf("cachebench: trace line %d: invalid schema or request_id", line)
		}
		if _, err := time.Parse(time.RFC3339Nano, record.At); err != nil || !validTraceIdentity(record) || record.PrefixTokens < 0 {
			return nil, fmt.Errorf("cachebench: trace line %d: incomplete request identity", line)
		}
		if len(record.Body) > limits.MaxBodyBytes || !json.Valid(record.Body) || record.BodySHA256 == "" || record.BodySHA256 != bodyDigest(record.Body) {
			return nil, fmt.Errorf("cachebench: trace line %d: invalid body or digest", line)
		}
		if record.StableSegmentCount < 0 || record.StableSegmentCount > len(record.Prefix) || !validPrefix(record.Prefix) {
			return nil, fmt.Errorf("cachebench: trace line %d: invalid prefix", line)
		}
		if record.Schema == TraceSchema || record.Schema == TraceSchemaV2 {
			if record.ExpectedRPM <= 0 || record.ExpectedCalls <= 0 || !validTimingBasis(record.TimingBasis) {
				return nil, fmt.Errorf("cachebench: trace line %d: incomplete replay metadata", line)
			}
		}
		if record.Schema == TraceSchema {
			if record.DeclaredInputTokens <= 0 || record.DeclaredInputTokens < record.PrefixTokens || record.MaxOutputTokens <= 0 || !requestBudgetMatchesBody(record) {
				return nil, fmt.Errorf("cachebench: trace line %d: incomplete or mismatched billed-token budget", line)
			}
		}
		seen[record.RequestID] = true
		record.Body = append(json.RawMessage(nil), record.Body...)
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cachebench: read trace: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("cachebench: no trace records")
	}
	return records, nil
}

// NativeRequest reconstructs exact optimizer input captured by v2 or v3 trace.
// Legacy v1 traces remain readable for observation joins, but cannot drive live
// replay because they omitted routing and economics inputs.
func (record TraceRecord) NativeRequest() (cacheengine.NativeRequest, error) {
	if record.Schema != TraceSchema && record.Schema != TraceSchemaV2 {
		return cacheengine.NativeRequest{}, fmt.Errorf("cachebench: request %q needs %s or %s for reconstruction", record.RequestID, TraceSchemaV2, TraceSchema)
	}
	if !validTraceIdentity(record) || !validTimingBasis(record.TimingBasis) || record.ExpectedRPM <= 0 || record.ExpectedCalls <= 0 || record.PrefixTokens < 0 || !validUniqueJSONObject(record.Body) || record.BodySHA256 != bodyDigest(record.Body) || record.StableSegmentCount < 0 || record.StableSegmentCount > len(record.Prefix) || !validPrefix(record.Prefix) {
		return cacheengine.NativeRequest{}, fmt.Errorf("cachebench: request %q has incomplete or invalid replay identity", record.RequestID)
	}
	return cacheengine.NativeRequest{
		Scope: record.Scope, Epoch: record.Epoch, PartitionKey: record.PartitionKey,
		ExpectedRequestsPerMinute: record.ExpectedRPM, ExpectedCalls: record.ExpectedCalls,
		Provider: record.Provider, Model: record.Model, Region: record.Region, Endpoint: record.Endpoint,
		Body: append([]byte(nil), record.Body...), RuntimeMode: record.RuntimeMode, AuthMode: record.AuthMode,
		PrefixTokens: record.PrefixTokens,
	}, nil
}

func validTraceIdentity(record TraceRecord) bool {
	return validBoundedText(record.RequestID, 512, false) &&
		validBoundedText(record.Provider, 64, false) &&
		validBoundedText(record.Model, 512, false) &&
		validBoundedText(record.Region, 64, true) &&
		validBoundedText(record.Endpoint, 512, false) &&
		validBoundedText(record.Epoch, 1024, false) &&
		validBoundedText(record.Scope, 1024, false) &&
		validBoundedText(record.TokenBasis, 128, false) &&
		validBoundedText(record.TimingBasis, 128, false) &&
		validBoundedText(record.PartitionKey, 2048, false) &&
		validBoundedText(record.RuntimeMode, 128, false) &&
		validBoundedText(record.AuthMode, 128, false)
}

func requestBudgetMatchesBody(record TraceRecord) bool {
	if !validUniqueJSONObject(record.Body) {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(record.Body, &root) != nil {
		return false
	}
	if raw, exists := root["stream"]; exists {
		var streaming bool
		if json.Unmarshal(raw, &streaming) != nil || streaming {
			return false
		}
	}
	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	if provider == "openai" || provider == "anthropic" {
		raw, exists := root["model"]
		var model string
		if !exists || json.Unmarshal(raw, &model) != nil || model != record.Model {
			return false
		}
	}
	var raw json.RawMessage
	switch provider {
	case "openai":
		field := "max_completion_tokens"
		if strings.Contains(strings.ToLower(record.Endpoint), "responses") {
			field = "max_output_tokens"
		}
		var exists bool
		raw, exists = root[field]
		if !exists {
			return false
		}
		for _, ambiguous := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			if ambiguous != field {
				if _, duplicate := root[ambiguous]; duplicate {
					return false
				}
			}
		}
	case "anthropic":
		raw = root["max_tokens"]
	case "bedrock":
		if strings.HasPrefix(strings.ToLower(record.Endpoint), "converse") {
			var inference map[string]json.RawMessage
			if !validUniqueJSONObject(root["inferenceConfig"]) || json.Unmarshal(root["inferenceConfig"], &inference) != nil {
				return false
			}
			raw = inference["maxTokens"]
		} else {
			raw = root["max_tokens"]
		}
	case "gemini":
		var generation map[string]json.RawMessage
		if !validUniqueJSONObject(root["generationConfig"]) || json.Unmarshal(root["generationConfig"], &generation) != nil {
			return false
		}
		raw = generation["maxOutputTokens"]
	default:
		return false
	}
	var maximum int
	return len(raw) > 0 && json.Unmarshal(raw, &maximum) == nil && maximum == record.MaxOutputTokens
}

func validTimingBasis(value string) bool {
	switch value {
	case TimingGrounded, TimingPerPartition, TimingSynthetic:
		return true
	default:
		return false
	}
}

func bodyDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:])
}
