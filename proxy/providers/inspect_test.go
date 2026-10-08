package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// inspectCorpus holds request bodies around every rule inspectObject relies
// on: grammar edges json.Valid draws, numbers a decode into any refuses,
// duplicate and escaped keys, and the spellings the pricing walk matches.
func inspectCorpus(t testing.TB) []string {
	corpus := []string{
		``, ` `, `null`, ` null `, `{}`, `[]`, `"x"`, `1`, `true`, `{"model":"m"} x`, `{"model":"m"}{}`,
		`{"model":"claude-sonnet-4-5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-5.5","input":"just text","tools":[{"type":"function","name":"f"}]}`,
		`{"model":"gpt-5.5","input":[{"role":"user","content":"a"},{"role":"user","content":"b"}],"service_tier":"flex"}`,
		`{"model":"m","messages":[],"input":[1,2,3],"tools":[]}`,
		`{"model":"m","messages":{"not":"an array"},"input":null}`,
		`{"model":"m","model":"second wins","stream":false,"stream":true}`,
		`{"mo\u0064el":"escaped key","str\u0065am":true}`,
		`{"model":"m","messages":[{"content":"plain"}],"MODEL":"case differs"}`,
		`{"model":"m","service_tier":{"type":"priority"},"inference_geo":" us "}`,
		`{"model":"m","serviceTier":7}`,
		`{"model":"m","background":true,"messages":[]}`,
		`{"model":"m","fallbacks":[],"speed":"fast"}`,
		`{"model":"m","fallbacks":["other"]}`,
		`{"model":"m","performanceConfig":{"latency":"optimized"}}`,
		`{"model":"m","guardrailConfig":{}}`,
		`{"model":"m","modalities":["text","AUDIO"]}`,
		`{"model":"m","tools":[{"type":"web_search_20250305"},{"googleSearch":{}}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","data":"x"}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"talk about Audio files"}]}]}`,
		`{"model":"m","contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"x"}}]}]}`,
		`{"model":"m","cachedContent":"cachedContents/abc"}`,
		`{"model":"m","messages":[{"cached_content":""}]}`,
		`{"model":"m","messages":[{"role":"user","cachedContent":"cachedContents/abc"}]}`,
		`{"model":"m","messages":[{"role":"user","CACHED_CONTENT":"x"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"AUDIO"}]}]}`,
		`{"model":"m","messages":[{"role":"user","Input_Audio":{}}]}`,
		`{"model":"m","messages":[{"role":"user","MimeType":" Audio/ogg"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"aud\u0069o hidden by an escape"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"AUDİO with a dotted capital I"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"AUDİO"}]}]}`,
		`{"model":"m","messages":[{"AUDİO":true}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"\u0130nput_aud\u0130o"}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"aud\u0069o"}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"\u212Aelvin audio"}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":"\u0041UDIO"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"\u003cb\u003e no letters escaped \u2028 \ud83d\ude00"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"\\u0061udio is a backslash, not an escape"}]}`,
		`{"model":"m","messages":[{"n":1e400}]}`,
		`{"model":"m","messages":[{"n":-1E+309}]}`,
		`{"model":"m","messages":[{"n":1e-400,"m":-0,"o":0.5e10,"p":123456789012345678901234567890}]}`,
		`{"model":"m","messages":[` + strings.Repeat("9", 320) + `]}`,
		`{"model":"m","messages":[01]}`, `{"model":"m","messages":[1.]}`, `{"model":"m","messages":[.5]}`,
		`{"model":"m","messages":[-]}`, `{"model":"m","messages":[1e]}`, `{"model":"m","messages":[+1]}`,
		`{"model":"m","messages":[1,]}`, `{"model":"m",}`, `{"model" "m"}`, `{model:"m"}`, `{"model":'m'}`,
		`{"model":"m","messages":[tru]}`, `{"model":"m","messages":[nul]}`, `{"model":"m","messages":[falsey]}`,
		"{\"model\":\"m\",\"messages\":[\"tab\tinside\"]}", "{\"model\":\"m\",\"messages\":[\"bad \\x escape\"]}",
		"{\"model\":\"m\",\"messages\":[\"\\u12\"]}", "{\"model\":\"m\",\"messages\":[\"\\uZZZZ\"]}",
		"{\"model\":\"m\",\"messages\":[\"invalid \xff utf8\"],\"mod\xffel\":1}",
		"{\"model\":\"m\",\"messages\":[\"unterminated}",
		"\ufeff{\"model\":\"m\"}", "{\"model\":\"m\"}\n\t\r ",
		"{\"model\":\"m\",\"messages\":[\"\x7f del is fine\"]}",
		`{"model":"m","messages":[{"a":{"b":[{"c":[true,false,null]}]}}]}`,
		`{"model":"m","tools":[{"type":"custom","input_schema":{"properties":{"image":{"type":"string"}}}}]}`,
		strings.Repeat("[", 10000) + strings.Repeat("]", 10000),
		strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
		`{"a":` + strings.Repeat("[", 9999) + strings.Repeat("]", 9999) + `}`,
		`{"a":` + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + `}`,
	}
	files, _ := filepath.Glob(filepath.Join("*", "testdata", "*.json"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		corpus = append(corpus, string(data))
	}
	return corpus
}

func fullDecodeMetadata(provider string, data []byte) RequestMetadata {
	meta := RequestMetadata{}
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) == nil {
		Base{Provider: provider}.inspectDecoded(&meta, decoded)
	}
	return meta
}

func checkInspectObject(t *testing.T, data []byte) {
	t.Helper()
	scan := requestScan{data: data}
	valid := scan.value() && scan.space() == len(data)
	if valid != json.Valid(data) {
		t.Fatalf("scan valid=%v, json.Valid=%v for %q", valid, !valid, truncateForLog(data))
	}
	var full map[string]any
	decoded, ok := inspectObject(data)
	if want := json.Unmarshal(data, &full) == nil; ok != want {
		t.Fatalf("inspectObject ok=%v, json.Unmarshal ok=%v for %q", ok, want, truncateForLog(data))
	}
	if !ok || scan.spelled || spellsPricingKey(data) {
		// Not decoded, or decoded whole exactly as before (where a body with
		// several pricing reasons reports one picked in random map order).
		return
	}
	for _, provider := range []string{"anthropic", "openai", "gemini", "bedrock", "vertex"} {
		got := RequestMetadata{}
		Base{Provider: provider}.inspectDecoded(&got, decoded)
		if want := fullDecodeMetadata(provider, data); got != want {
			t.Fatalf("%s metadata differs for %q:\n got %+v\nwant %+v", provider, truncateForLog(data), got, want)
		}
	}
}

// TestInspectObjectMatchesFullDecode: the targeted decode accepts exactly what
// json.Unmarshal into map[string]any accepts and yields the same metadata.
func TestInspectObjectMatchesFullDecode(t *testing.T) {
	for _, body := range inspectCorpus(t) {
		checkInspectObject(t, []byte(body))
	}
}

func FuzzInspectObject(f *testing.F) {
	for _, body := range inspectCorpus(f) {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkInspectObject(t, data) })
}

// TestOnlyKnownRunesLowercaseIntoASCII pins the premise of requestScan's
// spelled flag: U+0130 and U+212A are the only non-ASCII runes whose
// lowercase is ASCII.
func TestOnlyKnownRunesLowercaseIntoASCII(t *testing.T) {
	var found []rune
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		if unicode.ToLower(r) < 0x80 {
			found = append(found, r)
		}
	}
	if !reflect.DeepEqual(found, []rune{0x130, 0x212A}) {
		t.Fatalf("runes lowercasing into ASCII: %U", found)
	}
}
