package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestCodexTextExecEnvelopeUnwrapsAndRewraps(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(store, nil)

	var testLog strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&testLog, "=== RUN   TestCase%02d\n--- PASS: TestCase%02d (0.01s)\n", i, i)
	}
	testLog.WriteString("=== RUN   TestBrokenAuth\n    auth_test.go:42: expected 200 got 401\n--- FAIL: TestBrokenAuth (0.02s)\nFAIL\nFAIL\tgithub.com/example/pkg\t0.50s\n")

	header := "Chunk ID: 7ae69c\nWall time: 0.5123 seconds\nProcess exited with code 1\nOriginal token count: 1420\nOutput:\n"
	payload := []byte(header + testLog.String())

	if got := eng.Detect(payload); got != TypeTestReport {
		t.Fatalf("Detect(codex exec test log) = %q, want %q", got, TypeTestReport)
	}
	res, err := eng.Compress(payload, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	out := string(res.Output)
	if !strings.HasPrefix(out, header) {
		t.Fatalf("Codex header not preserved verbatim:\n%s", out)
	}
	if !strings.Contains(out, "TestBrokenAuth") || !strings.Contains(out, "expected 200 got 401") {
		t.Fatalf("failing test detail lost:\n%s", out)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("expected token reduction: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}
}

func TestClaudeSystemReminderUnwrapsListingAndJSONAndPreservesVerbatim(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(store, nil)

	var jsonArr strings.Builder
	jsonArr.WriteString("[")
	for i := 0; i < 30; i++ {
		if i > 0 {
			jsonArr.WriteByte(',')
		}
		fmt.Fprintf(&jsonArr, `{"id":%d,"name":"item-%d","status":"active","description":"repeated verbose metadata description entry %d"}`, i, i, i)
	}
	jsonArr.WriteString("]")
	reminder := "\n\n<system-reminder>\nWhenever you read a file, check for security issues.\n</system-reminder>"
	payload := []byte(jsonArr.String() + reminder)

	if got := eng.Detect(payload); got != TypeJSON {
		t.Fatalf("Detect(json + system-reminder) = %q, want %q", got, TypeJSON)
	}
	res, err := eng.Compress(payload, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	out := string(res.Output)
	if !strings.HasSuffix(out, reminder) {
		t.Fatalf("Claude <system-reminder> suffix not preserved verbatim:\n%s", out)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("expected token reduction: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}

	// Also verify stacked: line-numbered Go source + trailing <system-reminder>.
	var goLines strings.Builder
	goSrc := []string{
		"package demo",
		`import "fmt"`,
		"func ProcessOrder(id int, name string) string {",
		`    msg := fmt.Sprintf("order-%d-%s", id, name)`,
		"    for i := 0; i < 10; i++ {",
		"        msg = msg + \"!\"",
		"    }",
		"    return msg",
		"}",
		"func ValidateOrder(id int) bool {",
		"    if id <= 0 {",
		"        return false",
		"    }",
		"    return true",
		"}",
	}
	for i := 0; i < 5; i++ {
		for j, line := range goSrc {
			if i > 0 && j < 2 {
				continue
			}
			lineNum := i*15 + j + 1
			if i > 0 && strings.HasPrefix(line, "func ") {
				line = strings.Replace(line, "Order", fmt.Sprintf("Order%d", i), 1)
			}
			fmt.Fprintf(&goLines, "%4d→%s\n", lineNum, line)
		}
	}
	codePayload := []byte(goLines.String() + reminder)
	if got := eng.Detect(codePayload); got != TypeCode {
		t.Fatalf("Detect(line-numbered Go + system-reminder) = %q, want %q", got, TypeCode)
	}
	codeRes, err := eng.Compress(codePayload, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("Compress code: %v", err)
	}
	if !strings.HasSuffix(string(codeRes.Output), reminder) {
		t.Fatalf("stacked <system-reminder> not preserved:\n%s", codeRes.Output)
	}
	if !strings.Contains(string(codeRes.Output), "func ProcessOrder") {
		t.Fatalf("code signature lost:\n%s", codeRes.Output)
	}
}

func TestOpenCodeReadEnvelopeUnwrapsAndRewraps(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(store, nil)

	var body strings.Builder
	body.WriteString("[")
	for i := 0; i < 25; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"sku":"SKU-%03d","qty":%d,"warehouse":"us-west-2","notes":"standard inventory line item"}`, i, i+1)
	}
	body.WriteString("]")

	prefix := "<path>/workspace/inventory.json</path>\n<type>file</type>\n<content>\n"
	suffix := "\n</content>"
	payload := []byte(prefix + body.String() + suffix)

	if got := eng.Detect(payload); got != TypeJSON {
		t.Fatalf("Detect(opencode read json) = %q, want %q", got, TypeJSON)
	}
	res, err := eng.Compress(payload, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	out := string(res.Output)
	if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, suffix) {
		t.Fatalf("OpenCode envelope not preserved verbatim:\n%s", out)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("expected token reduction: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}
}

func TestHarnessEnvelopesFailClosedOnMalformedInputs(t *testing.T) {
	cases := [][]byte{
		[]byte("<system-reminder>standalone only</system-reminder>"),
		[]byte("Output:\nnot a codex header because no metadata line preceded it\n"),
		[]byte("Chunk ID: abc\nRandom line\nOutput:\nshould not match\n"),
		[]byte("<path>/a</path>\n<content>first</content>\n<content>second</content>"),
	}
	for i, c := range cases {
		inner, wrappers := unwrapInput(c)
		if len(wrappers) != 0 || string(inner) != string(c) {
			t.Fatalf("case %d unexpectedly unwrapped (%d wrappers): %q", i, len(wrappers), inner)
		}
	}
}

func TestCanonicalDedupKeyIgnoresVolatileCodexAndClaudeEnvelopes(t *testing.T) {
	body := `{"items":[{"id":1},{"id":2}]}`
	codexA := []byte("Chunk ID: aaa111\nWall time: 0.0412 seconds\nProcess exited with code 0\nTotal output lines: 12\nOutput:\n" + body)
	codexB := []byte("Chunk ID: zzz999\nWall time: 0.1893 seconds\nProcess exited with code 0\nTotal output lines: 12\nOutput:\n" + body)
	codexFail := []byte("Chunk ID: zzz999\nWall time: 0.1893 seconds\nProcess exited with code 1\nTotal output lines: 12\nOutput:\n" + body)
	if string(CanonicalDedupKey(codexA)) != string(CanonicalDedupKey(codexB)) {
		t.Fatalf("volatile Codex headers broke CanonicalDedupKey: %q vs %q", CanonicalDedupKey(codexA), CanonicalDedupKey(codexB))
	}
	if !strings.Contains(string(CanonicalDedupKey(codexA)), body) {
		t.Fatalf("CanonicalDedupKey did not retain inner body: %q", CanonicalDedupKey(codexA))
	}
	if string(CanonicalDedupKey(codexA)) == string(CanonicalDedupKey(codexFail)) {
		t.Fatalf("exit 0 and exit 1 Codex outputs must NOT share the same dedup key: %q", CanonicalDedupKey(codexA))
	}

	// OpenCode CRLF read wrapper strips cleanly and distinguishes different file paths.
	ocCRLF := []byte("<path>/repo/a.json</path>\r\n<type>file</type>\r\n<content>\r\n" + body + "\r\n</content>")
	ocOtherPath := []byte("<path>/repo/b.json</path>\r\n<type>file</type>\r\n<content>\r\n" + body + "\r\n</content>")
	if !strings.Contains(string(CanonicalDedupKey(ocCRLF)), body) || strings.Contains(string(CanonicalDedupKey(ocCRLF)), "\r") {
		t.Fatalf("OpenCode CRLF CanonicalDedupKey = %q", CanonicalDedupKey(ocCRLF))
	}
	if string(CanonicalDedupKey(ocCRLF)) == string(CanonicalDedupKey(ocOtherPath)) {
		t.Fatalf("OpenCode reads of two different paths must NOT share the same dedup key")
	}

	// OpenCode 00001| gutter + trailing pagination footer unwrap cleanly.
	var ocCode strings.Builder
	ocCode.WriteString("<path>/repo/handler.go</path>\n<type>file</type>\n<content>\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&ocCode, "%05d| func Handler%02d(x int) int { return x + %d }\n", i, i, i)
	}
	ocCode.WriteString("\n(End of file - total 40 lines)\n</content>")
	ocInfo := ExtractDedupComponents([]byte(ocCode.String()))
	if strings.Contains(string(ocInfo.Key), "(End of file") {
		t.Fatalf("OpenCode trailing footer should be stripped from canonical key: %q", ocInfo.Key)
	}
	if ocInfo.InnerLen == 0 {
		t.Fatalf("expected non-zero InnerLen for OpenCode read")
	}

	// Standalone <system-reminder> is rejected (InnerLen == 0) while trailing reminder is preserved for stubs.
	standaloneReminder := ExtractDedupComponents([]byte("<system-reminder>do not dedup me</system-reminder>"))
	if standaloneReminder.InnerLen != 0 {
		t.Fatalf("standalone <system-reminder> must return InnerLen=0, got %d", standaloneReminder.InnerLen)
	}
	withReminder := ExtractDedupComponents([]byte(body + "\n\n<system-reminder>preserve me verbatim</system-reminder>"))
	if !strings.Contains(string(withReminder.TrailingReminder), "preserve me verbatim") {
		t.Fatalf("expected TrailingReminder to be preserved, got %q", withReminder.TrailingReminder)
	}
}

func TestHermesToolJSONEnvelopeUnwrapsAndSeparatesExitCodes(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(store, nil)

	var inner strings.Builder
	inner.WriteString("[")
	for i := 0; i < 25; i++ {
		if i > 0 {
			inner.WriteByte(',')
		}
		fmt.Fprintf(&inner, `{"service":"worker-%02d","region":"us-east-1","status":"healthy","latency_ms":%d}`, i, 10+i)
	}
	inner.WriteString("]")

	okPayload, _ := json.Marshal(map[string]any{
		"output":    inner.String(),
		"exit_code": 0,
	})
	failPayload, _ := json.Marshal(map[string]any{
		"output":    inner.String(),
		"exit_code": 1,
	})

	if got := eng.Detect(okPayload); got != TypeJSON {
		t.Fatalf("Detect(hermes terminal json) = %q, want %q", got, TypeJSON)
	}
	res, err := eng.Compress(okPayload, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("Compress(hermes): %v", err)
	}
	var rewrapped map[string]any
	if err := json.Unmarshal(res.Output, &rewrapped); err != nil {
		t.Fatalf("Hermes rewrapped output is not valid JSON: %v\n%s", err, res.Output)
	}
	if rewrapped["exit_code"] != float64(0) {
		t.Fatalf("Hermes exit_code lost: %#v", rewrapped)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("expected token reduction inside Hermes JSON envelope: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}

	// Dedup key must distinguish exit_code 0 vs exit_code 1 and Hermes error fields.
	kOK := CanonicalDedupKey(okPayload)
	kFail := CanonicalDedupKey(failPayload)
	if string(kOK) == string(kFail) {
		t.Fatalf("Hermes exit_code 0 and 1 must NOT collide in dedup key: %q", kOK)
	}
}
