package translate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sessions of one caller grammar that grow a turn at a time, the way a coding
// agent resends its whole conversation: tool loops, images, parallel calls,
// and reasoning signed for every kind of host.
type sessionGen struct {
	rnd     *rand.Rand
	grammar string
	items   []string
	turn    int
}

func (s *sessionGen) pick(options ...string) string { return options[s.rnd.Intn(len(options))] }

func (s *sessionGen) text() string {
	words := []string{"fix", "the", "test", "\"quoted\"", "line\nbreak", "tab\there", "ünïcode", "</script>", "\\back", "emoji 😀"}
	var out []string
	for range 3 + s.rnd.Intn(12) {
		out = append(out, words[s.rnd.Intn(len(words))])
	}
	return strings.Join(out, " ")
}

const pngData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// envelopeOf is a runtime envelope carrying blocks, or a route's blob.
func envelopeOf(blocks, route, blob string) string {
	raw := `{"caveman":"v1","blocks":` + blocks
	if route != "" {
		raw += `,"route":` + encode(route) + `,"blob":` + encode(blob)
	}
	return base64.StdEncoding.EncodeToString([]byte(raw + "}"))
}

// step appends one assistant step and what answers it.
func (s *sessionGen) step() {
	s.turn++
	calls := 1 + s.rnd.Intn(2)
	ids := make([]string, calls)
	for i := range ids {
		ids[i] = s.pick("toolu_", "call_", "functions.Bash:") + fmt.Sprint(s.turn*10+i)
	}
	switch s.grammar {
	case Messages:
		var blocks []string
		switch s.pick("anthropic", "r1", "v1", "ns", "unsigned", "none") {
		case "anthropic":
			blocks = append(blocks, `{"type":"thinking","thinking":`+encode(s.text())+`,"signature":"EqAnthropic`+fmt.Sprint(s.turn)+`"}`)
		case "r1":
			blocks = append(blocks, `{"type":"thinking","thinking":"r","signature":"caveman:r1:chatgpt:ENC`+fmt.Sprint(s.turn)+`"}`)
		case "v1":
			blocks = append(blocks, `{"type":"thinking","thinking":"v","signature":"caveman:v1:deepseek:deepseek-v4-pro"}`)
		case "ns":
			blocks = append(blocks, `{"type":"thinking","thinking":"n","signature":"caveman:moonshot:sigK`+fmt.Sprint(s.turn)+`"}`)
		case "unsigned":
			blocks = append(blocks, `{"type":"thinking","thinking":"u","signature":""}`)
		}
		blocks = append(blocks, `{"type":"text","text":`+encode(s.text())+`}`)
		var results []string
		for _, id := range ids {
			blocks = append(blocks, `{"type":"tool_use","id":`+encode(id)+`,"name":"Bash","input":{"command":`+encode(s.text())+`}}`)
			results = append(results, `{"type":"tool_result","tool_use_id":`+encode(id)+`,"content":`+encode(s.text())+`}`)
		}
		if s.rnd.Intn(3) == 0 {
			results = append(results, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+pngData+`"}}`)
		}
		results = append(results, `{"type":"text","text":`+encode(s.text())+`}`)
		s.items = append(s.items, `{"role":"assistant","content":[`+strings.Join(blocks, ",")+`]}`, `{"role":"user","content":[`+strings.Join(results, ",")+`]}`)
	case Responses:
		switch s.pick("openai", "anthropic", "route", "chat", "none") {
		case "openai":
			s.items = append(s.items, `{"type":"reasoning","id":"rs_`+fmt.Sprint(s.turn)+`","summary":[],"encrypted_content":"gAAAAB`+fmt.Sprint(s.turn)+`"}`)
		case "anthropic":
			s.items = append(s.items, `{"type":"reasoning","summary":[],"encrypted_content":"`+envelopeOf(`[{"type":"thinking","thinking":"t","signature":"EqA`+fmt.Sprint(s.turn)+`"}]`, "", "")+`"}`)
		case "route":
			s.items = append(s.items, `{"type":"reasoning","summary":[],"encrypted_content":"`+envelopeOf(`[]`, "chatgpt", "BLOB"+fmt.Sprint(s.turn))+`"}`)
		case "chat":
			s.items = append(s.items, `{"type":"reasoning","summary":[],"encrypted_content":"`+envelopeOf(`[{"type":"thinking","thinking":"c","signature":"caveman:v1:deepseek:deepseek-v4-pro"}]`, "", "")+`"}`)
		}
		if s.rnd.Intn(2) == 0 {
			s.items = append(s.items, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":`+encode(s.text())+`}]}`)
		}
		for _, id := range ids {
			s.items = append(s.items, `{"type":"function_call","call_id":`+encode(id)+`,"name":"shell","arguments":`+encode(`{"cmd":`+encode(s.text())+`}`)+`}`)
		}
		for _, id := range ids {
			s.items = append(s.items, `{"type":"function_call_output","call_id":`+encode(id)+`,"output":`+encode(s.text())+`}`)
		}
		if s.rnd.Intn(3) == 0 {
			s.items = append(s.items, `{"type":"message","role":"user","content":[{"type":"input_text","text":`+encode(s.text())+`},{"type":"input_image","image_url":"data:image/png;base64,`+pngData+`"}]}`)
		}
	default:
		var calls []string
		for _, id := range ids {
			calls = append(calls, `{"id":`+encode(id)+`,"type":"function","function":{"name":"Bash","arguments":`+encode(`{"command":`+encode(s.text())+`}`)+`}}`)
		}
		message := `{"role":"assistant","content":` + encode(s.text()) + `,"tool_calls":[` + strings.Join(calls, ",") + `]`
		switch s.pick("anthropic", "route", "none") {
		case "anthropic":
			message += `,"reasoning_content":"t","reasoning_details":[{"type":"reasoning.encrypted","data":"` + envelopeOf(`[{"type":"thinking","thinking":"t","signature":"EqA`+fmt.Sprint(s.turn)+`"}]`, "", "") + `"}]`
		case "route":
			message += `,"reasoning_details":[{"type":"reasoning.encrypted","data":"` + envelopeOf(`[]`, "chatgpt", "BLOB"+fmt.Sprint(s.turn)) + `"}]`
		}
		s.items = append(s.items, message+"}")
		for _, id := range ids {
			s.items = append(s.items, `{"role":"tool","tool_call_id":`+encode(id)+`,"content":`+encode(s.text())+`}`)
		}
		if s.rnd.Intn(3) == 0 {
			s.items = append(s.items, `{"role":"user","content":[{"type":"text","text":`+encode(s.text())+`},{"type":"image_url","image_url":{"url":"data:image/png;base64,`+pngData+`"}}]}`)
		}
	}
}

// body is the caller's whole request at this point of the session.
func (s *sessionGen) body() string {
	history := strings.Join(s.items, ",")
	switch s.grammar {
	case Messages:
		return `{"model":"claude-opus-5-5","max_tokens":4096,"stream":true,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},` +
			`"system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Be terse."}],"tools":` + benchTools(Messages) +
			`,"messages":[{"role":"user","content":"start"},` + history + `]}`
	case Responses:
		return `{"model":"gpt-6-sol","instructions":"You are Codex.","stream":true,"store":false,"reasoning":{"effort":"high"},"tools":` + benchTools(Responses) +
			`,"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},` + history + `]}`
	}
	return `{"model":"gpt-x","stream":true,"reasoning_effort":"high","tools":` + benchTools(Chat) +
		`,"messages":[{"role":"system","content":"You are OpenCode."},{"role":"user","content":"start"},` + history + `]}`
}

var propertyTargets = map[string][]Options{
	Messages:  {{Model: "claude-opus-5-5", Route: "anthropic"}, {Model: "kimi-k3", Route: "moonshot"}},
	Chat:      {{Model: "deepseek-v4-pro", Route: "deepseek", Dialect: "deepseek", Replay: true}},
	Responses: {{Model: "gpt-6-sol", Route: "chatgpt"}, {Model: "gpt-6-sol", Route: "openai"}},
}

// historyKey is the member holding the conversation on wire.
func historyKey(wire string) string {
	if wire == Responses {
		return "input"
	}
	return "messages"
}

// TestPrefixStability: across a growing session, every direction renders the
// earlier turns byte for byte the same (the runtime's own cache breakpoints
// aside, which move with the conversation by design), and every other field
// does not change at all. Anthropic's and the implicit caches read a prefix:
// one changed byte in turn 3 re-bills everything after it.
func TestPrefixStability(t *testing.T) {
	marker := []byte(`,"cache_control":{"type":"ephemeral"}`)
	for _, from := range []string{Messages, Chat, Responses} {
		for _, to := range []string{Messages, Chat, Responses} {
			for _, opts := range propertyTargets[to] {
				for seed := int64(1); seed <= 12; seed++ {
					gen := &sessionGen{rnd: rand.New(rand.NewSource(seed)), grammar: from}
					var previous map[string]json.RawMessage
					var previousItems [][]byte
					for turn := 0; turn < 7; turn++ {
						gen.step()
						out, _, err := Request(from, to, []byte(gen.body()), opts)
						if err != nil {
							t.Fatalf("%s>%s %s seed %d turn %d: %v", from, to, opts.Route, seed, turn, err)
						}
						if !json.Valid(out) {
							t.Fatalf("%s>%s seed %d turn %d: invalid JSON\n%s", from, to, seed, turn, out)
						}
						fields, _ := topFields(out)
						history := items(bytes.ReplaceAll(fields[historyKey(to)], marker, nil))
						if previous != nil {
							for key, value := range fields {
								if key != historyKey(to) && string(value) != string(previous[key]) {
									t.Fatalf("%s>%s %s seed %d turn %d: %s changed\nwas %s\nnow %s", from, to, opts.Route, seed, turn, key, previous[key], value)
								}
							}
							if len(history) < len(previousItems) {
								t.Fatalf("%s>%s seed %d turn %d: history shrank", from, to, seed, turn)
							}
							for at, item := range previousItems {
								if !bytes.Equal(item, history[at]) {
									t.Fatalf("%s>%s %s seed %d turn %d: element %d changed\nwas %s\nnow %s", from, to, opts.Route, seed, turn, at, item, history[at])
								}
							}
						}
						previous, previousItems = fields, history
					}
				}
			}
		}
	}
}

// --- fuzzing, one target per direction --------------------------------------------

// fuzzSeeds are bodies of every grammar (the fuzzer mutates them into the
// others) and answers of every wire.
func fuzzSeeds(f *testing.F) {
	for _, grammar := range []string{Messages, Chat, Responses} {
		gen := &sessionGen{rnd: rand.New(rand.NewSource(7)), grammar: grammar}
		gen.step()
		gen.step()
		for _, to := range []string{Messages, Chat, Responses} {
			f.Add([]byte(gen.body()), []byte(benchAnswer(to)))
		}
		f.Add(benchBody(grammar, 300), []byte(benchAnswer(grammar)))
	}
	f.Add([]byte(`{"messages":"x","input":7}`), []byte("data: {\n\n"))
	f.Add([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"QQ=="}}]}]}`), []byte(`{"choices":[]}`))
	f.Add([]byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,QQ=="}]}],"text":{"format":{"type":"json_schema","name":"o","schema":{"type":"object"}}}}`), []byte("event: response.failed\ndata: {}\n\n"))
}

// fuzzDirection checks one direction: a valid body translates to a valid body
// or an error, never a panic; Anthropic never gets a thinking signature it did
// not mint; any answer bytes are served or refused without a panic or hang.
func fuzzDirection(f *testing.F, from, to string) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, body, answer []byte) {
		for _, opts := range propertyTargets[to] {
			out, reply, err := Request(from, to, body, opts)
			if err != nil {
				continue
			}
			if json.Valid(body) && !json.Valid(out) {
				t.Fatalf("%s>%s: valid body became invalid JSON\nin  %s\nout %s", from, to, body, out)
			}
			if to == Messages && opts.Route == anthropicRoute {
				if forged := forgedThinking(out); forged != "" {
					t.Fatalf("%s>%s: Anthropic would get %s\nin %s", from, to, forged, body)
				}
			}
			recorder := httptest.NewRecorder()
			_, _ = reply.Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(answer))})
		}
	})
}

// forgedThinking names a thinking block bound for Anthropic that Anthropic
// did not sign ("" when there is none).
func forgedThinking(body []byte) string {
	var parsed struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	for _, message := range parsed.Messages {
		var blocks []map[string]any
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			field := map[any]string{"thinking": "signature", "redacted_thinking": "data"}[block["type"]]
			if field == "" {
				continue
			}
			if value, _ := block[field].(string); value == "" || strings.HasPrefix(value, signaturePrefix) {
				return fmt.Sprintf("%v", block)
			}
		}
	}
	return ""
}

func FuzzMessagesToMessages(f *testing.F)   { fuzzDirection(f, Messages, Messages) }
func FuzzMessagesToChat(f *testing.F)       { fuzzDirection(f, Messages, Chat) }
func FuzzMessagesToResponses(f *testing.F)  { fuzzDirection(f, Messages, Responses) }
func FuzzChatToMessages(f *testing.F)       { fuzzDirection(f, Chat, Messages) }
func FuzzChatToChat(f *testing.F)           { fuzzDirection(f, Chat, Chat) }
func FuzzChatToResponses(f *testing.F)      { fuzzDirection(f, Chat, Responses) }
func FuzzResponsesToMessages(f *testing.F)  { fuzzDirection(f, Responses, Messages) }
func FuzzResponsesToChat(f *testing.F)      { fuzzDirection(f, Responses, Chat) }
func FuzzResponsesToResponses(f *testing.F) { fuzzDirection(f, Responses, Responses) }
