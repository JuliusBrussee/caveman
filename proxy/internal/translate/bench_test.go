package translate

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Claude-Code-, Codex- and OpenCode-shaped sessions of a given size, for the
// per-direction benchmarks: a long system prompt, a tool list with schemas,
// then tool loops (thinking, a call, a large result) until the body reaches
// about `tokens` tokens (4 bytes a token).

const benchSystem = "You are an interactive CLI tool that helps users with software engineering tasks. "

func benchFiller(n int) string {
	words := []string{"func", "return", "if", "err", "nil", "struct", "package", "import", "for", "range", "the", "value", "\"quoted\"", "path/to/file.go", "\\n", "{", "}"}
	var out strings.Builder
	for i := 0; out.Len() < n; i++ {
		out.WriteString(words[i%len(words)])
		out.WriteByte(' ')
	}
	return out.String()
}

func benchTools(grammar string) string {
	var tools []string
	for i := range 18 {
		schema := `{"type":"object","properties":{"command":{"type":"string","description":"The command to run"},"timeout":{"type":"number"},"path":{"type":"string"}},"required":["command"],"additionalProperties":false}`
		description := fmt.Sprintf("Tool %d. %s", i, benchFiller(600))
		switch grammar {
		case Messages:
			tools = append(tools, fmt.Sprintf(`{"name":"Tool%d","description":%s,"input_schema":%s}`, i, encode(description), schema))
		case Responses:
			tools = append(tools, fmt.Sprintf(`{"type":"function","name":"Tool%d","description":%s,"parameters":%s,"strict":false}`, i, encode(description), schema))
		default:
			tools = append(tools, fmt.Sprintf(`{"type":"function","function":{"name":"Tool%d","description":%s,"parameters":%s}}`, i, encode(description), schema))
		}
	}
	return "[" + strings.Join(tools, ",") + "]"
}

// benchBody is a caller body in grammar of about tokens tokens.
func benchBody(grammar string, tokens int) []byte {
	target := tokens * 4
	system := benchSystem + benchFiller(min(target/4, 40000))
	result := benchFiller(6000)
	var history []string
	size := len(system) + 12000
	switch grammar {
	case Messages:
		history = append(history, `{"role":"user","content":[{"type":"text","text":"fix the failing test"}]}`)
		for i := 0; size < target; i++ {
			turn := fmt.Sprintf(`{"role":"assistant","content":[{"type":"thinking","thinking":%s,"signature":"EqQBCkgIBRABGAIiQ%dAnthropicSignature=="},{"type":"text","text":"Running it."},{"type":"tool_use","id":"toolu_%04d","name":"Tool1","input":{"command":"go test ./... -run X%d"}}]}`,
				encode(benchFiller(400)), i, i, i)
			reply := fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%04d","content":%s}]}`, i, encode(result))
			history = append(history, turn, reply)
			size += len(turn) + len(reply)
		}
		return []byte(`{"model":"claude-opus-5-5","max_tokens":32000,"stream":true,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},` +
			`"metadata":{"user_id":"user_abc_account_def_session_123"},` +
			`"system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":` + encode(system) + `,"cache_control":{"type":"ephemeral"}}],` +
			`"tools":` + benchTools(Messages) + `,"messages":[` + strings.Join(history, ",") + `]}`)
	case Responses:
		history = append(history, `{"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]}`,
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the failing test"}]}`)
		for i := 0; size < target; i++ {
			turn := fmt.Sprintf(`{"type":"reasoning","id":"rs_%04d","summary":[{"type":"summary_text","text":%s}],"encrypted_content":"gAAAAAB%dEncryptedReasoningBlobFromOpenAI%s"},`+
				`{"type":"function_call","id":"fc_%04d","call_id":"call_%04d","name":"Tool1","arguments":"{\"command\":\"go test ./... -run X%d\"}"}`,
				i, encode(benchFiller(400)), i, strings.Repeat("A", 800), i, i, i)
			reply := fmt.Sprintf(`{"type":"function_call_output","call_id":"call_%04d","output":%s}`, i, encode(result))
			history = append(history, turn, reply)
			size += len(turn) + len(reply)
		}
		return []byte(`{"model":"gpt-6-sol","instructions":` + encode(system) + `,"input":[` + strings.Join(history, ",") + `],` +
			`"tools":` + benchTools(Responses) + `,"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"high","summary":"auto"},` +
			`"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"thread-1"}`)
	}
	history = append(history, `{"role":"system","content":`+encode(system)+`}`, `{"role":"user","content":"fix the failing test"}`)
	for i := 0; size < target; i++ {
		turn := fmt.Sprintf(`{"role":"assistant","content":"Running it.","reasoning_content":%s,"tool_calls":[{"id":"call_%04d","type":"function","function":{"name":"Tool1","arguments":"{\"command\":\"go test ./... -run X%d\"}"}}]}`,
			encode(benchFiller(400)), i, i)
		reply := fmt.Sprintf(`{"role":"tool","tool_call_id":"call_%04d","content":%s}`, i, encode(result))
		history = append(history, turn, reply)
		size += len(turn) + len(reply)
	}
	return []byte(`{"model":"gpt-x","messages":[` + strings.Join(history, ",") + `],"tools":` + benchTools(Chat) +
		`,"tool_choice":"auto","reasoning_effort":"high","stream":true,"stream_options":{"include_usage":true}}`)
}

// benchAnswer is a short streamed answer on wire to.
func benchAnswer(to string) string {
	switch to {
	case Messages:
		return anthropicText("ok")
	case Responses:
		return fullResponsesStream
	}
	return chatStream(`{"id":"c1","choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
		`{"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
}

var benchTargets = map[string]Options{
	Messages:  {Model: "claude-opus-5-5", Route: "anthropic"},
	Chat:      {Model: "deepseek-v4-pro", Route: "deepseek", Dialect: "deepseek", Replay: true},
	Responses: {Model: "gpt-6-sol", Route: "openai"},
}

// nopFlusher is a ResponseWriter that discards the answer.
type nopWriter struct{ header http.Header }

func (w *nopWriter) Header() http.Header         { return w.header }
func (w *nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nopWriter) WriteHeader(int)             {}
func (w *nopWriter) Flush()                      {}

// BenchmarkTranslate is Request plus Serve per direction and body size; the
// ms/100k-tok metric is the time scaled to a 100k-token body.
func BenchmarkTranslate(b *testing.B) {
	grammars := []string{Messages, Chat, Responses}
	for _, from := range grammars {
		for _, to := range grammars {
			if !Supported(from, to) {
				continue
			}
			for _, tokens := range []int{2000, 50000, 200000} {
				body := benchBody(from, tokens)
				answer := benchAnswer(to)
				opts := benchTargets[to]
				b.Run(fmt.Sprintf("%s>%s/%dk", from[:1], to[:1], tokens/1000), func(b *testing.B) {
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					for b.Loop() {
						_, reply, err := Request(from, to, body, opts)
						if err != nil {
							b.Fatal(err)
						}
						if _, err := reply.Serve(&nopWriter{header: http.Header{}}, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer))}); err != nil {
							b.Fatal(err)
						}
					}
					perOp := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
					b.ReportMetric(perOp/1e6*100000*4/float64(len(body)), "ms/100k-tok")
				})
			}
		}
	}
}

// BenchmarkNative is the harness path's hygiene on a body with nothing to
// strip (every request to Anthropic's, OpenAI's or a chat API): it must cost
// a scan, not a parse.
func BenchmarkNative(b *testing.B) {
	for name, run := range map[string]struct {
		grammar string
		strip   func([]byte) []byte
	}{"anthropic": {Messages, AnthropicNative}, "openai": {Responses, OpenAINative}, "chat": {Chat, ChatNative}} {
		body := benchBody(run.grammar, 200000)
		b.Run(name+"/200k", func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				run.strip(body)
			}
		})
	}
}
