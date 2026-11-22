package compressors

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// testReportCompressor normalizes test report formats (JUnit XML, pytest JSON,
// Jest JSON) to a concise text summary showing only failures + summary stats.
// It is S4 (lossy); the original is recoverable via CCR.
type testReportCompressor struct{}

// NewTestReport returns the default test-report compressor.
func NewTestReport() Compressor { return &testReportCompressor{} }

func (c *testReportCompressor) ContentType() string       { return "test-report" }
func (c *testReportCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *testReportCompressor) Compress(input []byte) ([]byte, bool) {
	report, ok := parseTestReport(input)
	if !ok {
		return nil, false
	}
	return renderTestReport(report), true
}

// LooksTestReport reports whether input parses as a JUnit, pytest or Jest
// report with at least one test: exactly the payloads Compress accepts. Detect
// gates on it so a report the compressor would decline keeps its old routing.
func LooksTestReport(input []byte) bool {
	_, ok := parseTestReport(input)
	return ok
}

func parseTestReport(input []byte) (*testReport, bool) {
	if !utf8.Valid(input) {
		return nil, false // binary content → pass-through
	}
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return nil, false
	}

	// Try each format parser in turn
	var report *testReport
	var err error

	if trimmed[0] == '<' {
		report, err = parseJUnitXML(input)
	} else if trimmed[0] == '{' || trimmed[0] == '[' {
		// Try pytest first, then Jest
		report, err = parsePytestJSON(input)
		if err != nil {
			report, err = parseJestJSON(input)
		}
	} else {
		// Plain-text verbose test runners: `go test -v` and `cargo test`.
		report, err = parseGoTestVerbose(input)
		if err != nil {
			report, err = parseCargoTest(input)
		}
	}

	if err != nil || report == nil {
		return nil, false // parse failure → pass-through
	}

	// If 0 tests total, pass through (nothing to compress)
	total := report.Passed + report.Failed + report.Skipped + report.Errors
	for _, n := range report.Other {
		total += n
	}
	if total == 0 {
		return nil, false
	}
	return report, true
}

// testReport is the normalized internal representation of test results.
type testReport struct {
	Passed   int
	Failed   int
	Skipped  int
	Errors   int
	Duration float64 // seconds
	Failures []testFailure
	// Other counts any further outcome by name (pytest's xfailed, xpassed,
	// rerun) so the summary never drops a test.
	Other map[string]int
}

// testFailure describes one failing test.
type testFailure struct {
	Name    string   // test name/nodeid/fullName
	Message string   // failure message
	Type    string   // "failure" or "error"
	Stack   []string // trimmed stack frames
}

// renderTestReport produces the normalized text output.
func renderTestReport(r *testReport) []byte {
	var b strings.Builder

	// Summary line
	b.WriteString("TEST SUMMARY: ")
	parts := []string{}
	if r.Passed > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", r.Passed))
	}
	if r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", r.Failed))
	}
	if r.Errors > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", r.Errors))
	}
	if r.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", r.Skipped))
	}
	others := make([]string, 0, len(r.Other))
	for name := range r.Other {
		others = append(others, name)
	}
	sort.Strings(others)
	for _, name := range others {
		parts = append(parts, fmt.Sprintf("%d %s", r.Other[name], name))
	}
	b.WriteString(strings.Join(parts, ", "))
	if r.Duration > 0 {
		b.WriteString(fmt.Sprintf(" (%.1fs)", r.Duration))
	}
	b.WriteString("\n")

	// Failures
	for _, f := range r.Failures {
		b.WriteString("\n")
		label := "FAILED"
		if f.Type == "error" {
			label = "ERROR"
		}
		b.WriteString(fmt.Sprintf("%s: %s\n", label, f.Name))
		if f.Message != "" {
			// Indent message
			for _, line := range strings.Split(strings.TrimSpace(f.Message), "\n") {
				b.WriteString("  ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
		// Stack frames (already trimmed by parser)
		for _, frame := range f.Stack {
			b.WriteString("  ")
			b.WriteString(frame)
			b.WriteString("\n")
		}
	}

	return []byte(b.String())
}

// parseJUnitXML parses JUnit XML format (standard test runner output).
func parseJUnitXML(input []byte) (*testReport, error) {
	type testCase struct {
		Name      string `xml:"name,attr"`
		ClassName string `xml:"classname,attr"`
		Time      string `xml:"time,attr"`
		Failure   *struct {
			Message string `xml:"message,attr"`
			Type    string `xml:"type,attr"`
			Text    string `xml:",chardata"`
		} `xml:"failure"`
		Error *struct {
			Message string `xml:"message,attr"`
			Type    string `xml:"type,attr"`
			Text    string `xml:",chardata"`
		} `xml:"error"`
		Skipped *struct{} `xml:"skipped"`
	}

	type testSuite struct {
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Errors   int        `xml:"errors,attr"`
		Skipped  int        `xml:"skipped,attr"`
		Time     string     `xml:"time,attr"`
		TestCase []testCase `xml:"testcase"`
		// PHPUnit nests a suite per class inside a suite per directory.
		Suites []testSuite `xml:"testsuite"`
	}

	type testSuites struct {
		XMLName xml.Name    `xml:"testsuites"`
		Suite   []testSuite `xml:"testsuite"`
	}

	type singleSuite struct {
		XMLName xml.Name `xml:"testsuite"`
		testSuite
	}

	// Test runners color failure text for terminals; a raw ESC is illegal XML
	// and would make the whole report unparseable.
	input = ansiRe.ReplaceAll(input, nil)

	// Try multi-suite format first
	var suites testSuites
	err := xml.Unmarshal(input, &suites)
	if err != nil || len(suites.Suite) == 0 {
		// Try single suite format
		var single singleSuite
		err = xml.Unmarshal(input, &single)
		if err != nil {
			return nil, err
		}
		suites.Suite = []testSuite{single.testSuite}
	}

	// Cases of a suite and every suite nested in it. Duration stays the sum of
	// the top-level suites: a parent suite's time already includes its children.
	var casesOf func(testSuite) []testCase
	casesOf = func(s testSuite) []testCase {
		cases := s.TestCase
		for _, child := range s.Suites {
			cases = append(cases, casesOf(child)...)
		}
		return cases
	}

	report := &testReport{}
	for _, suite := range suites.Suite {
		// Parse duration
		if suite.Time != "" {
			var dur float64
			fmt.Sscanf(suite.Time, "%f", &dur)
			report.Duration += dur
		}

		for _, tc := range casesOf(suite) {
			switch {
			case tc.Failure != nil:
				report.Failed++
				name := tc.Name
				if tc.ClassName != "" && tc.ClassName != name {
					name = tc.ClassName + "::" + name
				}
				msg := tc.Failure.Message
				if msg == "" {
					msg = tc.Failure.Text
				}
				stack := trimStack(strings.Split(tc.Failure.Text, "\n"))
				report.Failures = append(report.Failures, testFailure{
					Name:    name,
					Message: msg,
					Type:    "failure",
					Stack:   stack,
				})
			case tc.Error != nil:
				report.Errors++
				name := tc.Name
				if tc.ClassName != "" && tc.ClassName != name {
					name = tc.ClassName + "::" + name
				}
				msg := tc.Error.Message
				if msg == "" {
					msg = tc.Error.Text
				}
				stack := trimStack(strings.Split(tc.Error.Text, "\n"))
				report.Failures = append(report.Failures, testFailure{
					Name:    name,
					Message: msg,
					Type:    "error",
					Stack:   stack,
				})
			case tc.Skipped != nil:
				report.Skipped++
			default:
				report.Passed++
			}
		}
	}

	return report, nil
}

// parsePytestJSON parses pytest --json-report output format.
func parsePytestJSON(input []byte) (*testReport, error) {
	var doc struct {
		ExitCode int     `json:"exitcode"`
		Duration float64 `json:"duration"`
		Tests    []struct {
			NodeID  string `json:"nodeid"`
			Outcome string `json:"outcome"`
			// Each stage carries its own longrepr: a failure lives under
			// call, a fixture error under setup or teardown.
			Setup    pytestStage `json:"setup"`
			Call     pytestStage `json:"call"`
			Teardown pytestStage `json:"teardown"`
		} `json:"tests"`
		// A module pytest could not collect is recorded only here: tests[]
		// never gets an entry for it and the json-report summary has no error
		// key, so this is the sole record that the run was red.
		Collectors []struct {
			NodeID   string `json:"nodeid"`
			Outcome  string `json:"outcome"`
			Longrepr string `json:"longrepr"`
		} `json:"collectors"`
	}

	if err := json.Unmarshal(input, &doc); err != nil {
		return nil, err
	}

	// Validate this is actually pytest format (has required keys)
	if doc.Tests == nil {
		return nil, fmt.Errorf("not pytest format: missing 'tests' key")
	}

	report := &testReport{Duration: doc.Duration}

	for _, test := range doc.Tests {
		longrepr := test.Call.Longrepr
		if longrepr == "" {
			longrepr = test.Setup.Longrepr
		}
		if longrepr == "" {
			longrepr = test.Teardown.Longrepr
		}
		switch test.Outcome {
		case "passed":
			report.Passed++
		case "failed":
			report.Failed++
			lines := strings.Split(longrepr, "\n")
			msg := ""
			stack := []string{}
			// pytest longrepr has message + traceback mixed; extract both
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if strings.HasPrefix(line, "E ") {
					// Error line from pytest
					if msg == "" {
						msg = strings.TrimPrefix(line, "E ")
					}
				} else if strings.Contains(line, ".py:") || strings.HasPrefix(line, "at ") {
					stack = append(stack, line)
				}
			}
			stack = trimStack(stack)
			report.Failures = append(report.Failures, testFailure{
				Name:    test.NodeID,
				Message: msg,
				Type:    "failure",
				Stack:   stack,
			})
		case "skipped":
			report.Skipped++
		case "error":
			report.Errors++
			lines := strings.Split(longrepr, "\n")
			msg := ""
			stack := []string{}
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if msg == "" && !strings.Contains(line, ".py:") {
					msg = line
				} else if strings.Contains(line, ".py:") {
					stack = append(stack, line)
				}
			}
			stack = trimStack(stack)
			report.Failures = append(report.Failures, testFailure{
				Name:    test.NodeID,
				Message: msg,
				Type:    "error",
				Stack:   stack,
			})
		case "":
		default:
			if report.Other == nil {
				report.Other = map[string]int{}
			}
			report.Other[test.Outcome]++
		}
	}

	// A collector pytest could not run — an unimportable test module, a module
	// body that raises, a broken conftest — is "failed" with no entry in
	// tests[], so without this a run pytest itself reported as red ("2 passed,
	// 1 error", exit 1) summarized as all-passing. Reachable under
	// --continue-on-collection-errors, which leaves the surviving tests in
	// tests[] so the payload is compressed rather than passed through on the
	// 0-tests check.
	//
	// Only "failed" counts. Parent collectors of a failed module stay "passed",
	// so a failure is reported once, by the node that produced it; a module
	// skipped at collection is "skipped", which pytest counts as a skip and not
	// as an error.
	for _, col := range doc.Collectors {
		if col.Outcome != "failed" {
			continue
		}
		report.Errors++
		report.Failures = append(report.Failures, pytestCollectionFailure(col.NodeID, col.Longrepr))
	}

	return report, nil
}

// pytestCollectionFailure turns a failed collector's longrepr into an error
// entry. pytest's own short summary names the exception, so the "E " line is
// preferred as the reason; a longrepr without one (a plain collection message)
// falls back to its first non-frame line. Frames are the ".py:" lines, matching
// the two branches above.
func pytestCollectionFailure(nodeID, longrepr string) testFailure {
	msg, fallback := "", ""
	stack := []string{}
	for _, line := range strings.Split(longrepr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "E "):
			if msg == "" {
				msg = strings.TrimSpace(strings.TrimPrefix(line, "E "))
			}
		case strings.Contains(line, ".py:"):
			stack = append(stack, line)
		case fallback == "":
			fallback = line
		}
	}
	if msg == "" {
		msg = fallback
	}
	return testFailure{Name: nodeID, Message: msg, Type: "error", Stack: trimStack(stack)}
}

type pytestStage struct {
	Longrepr string `json:"longrepr"`
}

// parseJestJSON parses Jest JSON output format.
func parseJestJSON(input []byte) (*testReport, error) {
	var doc struct {
		NumFailedTests  int `json:"numFailedTests"`
		NumPassedTests  int `json:"numPassedTests"`
		NumPendingTests int `json:"numPendingTests"`
		TestResults     []struct {
			Name             string `json:"name"`
			Status           string `json:"status"`
			Message          string `json:"message"`
			AssertionResults []struct {
				AncestorTitles  []string `json:"ancestorTitles"`
				Title           string   `json:"title"`
				FullName        string   `json:"fullName"`
				Status          string   `json:"status"`
				FailureMessages []string `json:"failureMessages"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}

	if err := json.Unmarshal(input, &doc); err != nil {
		return nil, err
	}

	// Validate Jest format (both required keys present)
	if doc.TestResults == nil {
		return nil, fmt.Errorf("not Jest format: missing required keys")
	}

	report := &testReport{
		Passed:  doc.NumPassedTests,
		Failed:  doc.NumFailedTests,
		Skipped: doc.NumPendingTests,
	}

	for _, suite := range doc.TestResults {
		failedTests := 0
		for _, test := range suite.AssertionResults {
			if test.Status == "failed" {
				failedTests++
				name := test.FullName
				if name == "" {
					name = strings.Join(append(test.AncestorTitles, test.Title), " › ")
				}
				msg := ""
				stack := []string{}
				if len(test.FailureMessages) > 0 {
					// Jest failure messages include stack traces
					lines := strings.Split(test.FailureMessages[0], "\n")
					for _, line := range lines {
						line = strings.TrimSpace(line)
						if line == "" {
							continue
						}
						if strings.HasPrefix(line, "at ") || strings.Contains(line, ".test.") || strings.Contains(line, ".spec.") {
							stack = append(stack, line)
						} else if msg == "" {
							msg = line
						}
					}
				}
				stack = trimStack(stack)
				report.Failures = append(report.Failures, testFailure{
					Name:    name,
					Message: msg,
					Type:    "failure",
					Stack:   stack,
				})
			}
		}
		// A suite Jest could not run (a module that fails to load, a throwing
		// beforeAll/afterAll) is "failed" with no failed test of its own. Its
		// reason lives only in the suite message and it never reaches
		// numFailedTests, so without this a red run summarizes as all-passing.
		if suite.Status == "failed" && failedTests == 0 {
			report.Errors++
			report.Failures = append(report.Failures, jestSuiteFailure(suite.Name, suite.Message))
		}
	}

	return report, nil
}

// jestSuiteFailure turns a suite-level Jest message into an error entry. The
// first line after Jest's "● Test suite failed to run" header is the reason;
// "at …" lines are the stack. Jest colors the message when stdout is a TTY or
// FORCE_COLOR is set.
func jestSuiteFailure(name, message string) testFailure {
	msg := ""
	stack := []string{}
	for _, line := range strings.Split(ansiRe.ReplaceAllString(message, ""), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasSuffix(line, "Test suite failed to run"):
		case strings.HasPrefix(line, "at "):
			stack = append(stack, line)
		case msg == "":
			msg = line
		}
	}
	return testFailure{Name: name, Message: msg, Type: "error", Stack: trimStack(stack)}
}

// trimStack keeps first + last few frames, eliding the middle if too long.
// It filters out empty lines and non-stack-frame lines (message lines, blank lines).
// Stack frames typically start with "at" or contain file:line patterns.
func trimStack(frames []string) []string {
	const keepHead = 3
	const keepTail = 2
	const minToTrim = keepHead + keepTail + 3 // only trim if it saves at least 3 frames

	// Filter out empty frames and non-stack-frame lines
	var clean []string
	for _, f := range frames {
		trimmed := strings.TrimSpace(f)
		if trimmed == "" {
			continue
		}
		// Keep lines that look like stack frames: start with "at" or contain file:line patterns
		if strings.HasPrefix(trimmed, "at ") ||
			strings.Contains(trimmed, ".py:") ||
			strings.Contains(trimmed, ".java:") ||
			strings.Contains(trimmed, ".js:") ||
			strings.Contains(trimmed, ".ts:") ||
			strings.Contains(trimmed, ".go:") ||
			strings.Contains(trimmed, ".rb:") {
			clean = append(clean, trimmed)
		}
	}

	if len(clean) < minToTrim {
		return clean
	}

	out := make([]string, 0, keepHead+1+keepTail)
	out = append(out, clean[:keepHead]...)
	elided := len(clean) - keepHead - keepTail
	out = append(out, fmt.Sprintf("... (%d frames omitted)", elided))
	out = append(out, clean[len(clean)-keepTail:]...)
	return out
}

// parseGoTestVerbose parses plain-text `go test -v` output, collapsing all
// passing `=== RUN` / `--- PASS:` lines while keeping 100% of failure messages,
// assertion details, and file:line diagnostics for every failing test.
func parseGoTestVerbose(input []byte) (*testReport, error) {
	clean := ansiRe.ReplaceAll(input, nil)
	if !bytes.Contains(clean, []byte("=== RUN")) ||
		(!bytes.Contains(clean, []byte("--- PASS:")) && !bytes.Contains(clean, []byte("--- FAIL:"))) {
		return nil, fmt.Errorf("not go test -v output")
	}
	lines := strings.Split(string(clean), "\n")
	report := &testReport{}
	var currentTest string
	var currentBuf []string
	var buildPkg string
	var buildLines []string
	pkgFailedTests := 0
	flushFailed := func(name string) {
		msgLines := make([]string, 0, len(currentBuf))
		for _, l := range currentBuf {
			if t := strings.TrimSpace(l); t != "" {
				msgLines = append(msgLines, t)
			}
		}
		report.Failures = append(report.Failures, testFailure{
			Name:    name,
			Message: strings.Join(msgLines, "\n"),
			Type:    "failure",
		})
	}
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(trimmed, "# "):
			if buildPkg != "" && len(buildLines) > 0 {
				// Multiple packages failed to build before any FAIL summary line;
				// emit the previous package's compiler diagnostics verbatim.
				report.Failed++
				report.Failures = append(report.Failures, testFailure{
					Name:    buildPkg + " [build failed]",
					Message: strings.Join(buildLines, "\n"),
					Type:    "error",
				})
			}
			buildPkg = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			buildLines = []string{raw}
			currentTest = ""
			currentBuf = currentBuf[:0]
		case strings.HasPrefix(trimmed, "=== RUN "):
			currentTest = strings.TrimSpace(strings.TrimPrefix(trimmed, "=== RUN "))
			currentBuf = currentBuf[:0]
		case strings.HasPrefix(trimmed, "=== PAUSE "), strings.HasPrefix(trimmed, "=== CONT "):
			continue
		case strings.HasPrefix(trimmed, "--- PASS: "):
			report.Passed++
			currentBuf = currentBuf[:0]
		case strings.HasPrefix(trimmed, "--- SKIP: "):
			report.Skipped++
			currentBuf = currentBuf[:0]
		case strings.HasPrefix(trimmed, "--- FAIL: "):
			report.Failed++
			pkgFailedTests++
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "--- FAIL: "))
			name := rest
			if sp := strings.IndexByte(rest, ' '); sp > 0 {
				name = rest[:sp]
			}
			if name == "" {
				name = currentTest
			}
			flushFailed(name)
			currentBuf = currentBuf[:0]
		case strings.HasPrefix(trimmed, "ok  \t") || strings.HasPrefix(trimmed, "?\t") || strings.HasPrefix(trimmed, "?   \t"):
			if fields := strings.Fields(trimmed); len(fields) >= 3 && strings.HasSuffix(fields[2], "s") {
				var d float64
				if _, err := fmt.Sscanf(strings.TrimSuffix(fields[2], "s"), "%f", &d); err == nil {
					report.Duration += d
				}
			}
			pkgFailedTests = 0
		case strings.HasPrefix(trimmed, "FAIL\t"):
			if fields := strings.Fields(trimmed); len(fields) >= 3 && strings.HasSuffix(fields[2], "s") {
				var d float64
				if _, err := fmt.Sscanf(strings.TrimSuffix(fields[2], "s"), "%f", &d); err == nil {
					report.Duration += d
				}
			}
			if strings.Contains(trimmed, "[build failed]") || strings.Contains(trimmed, "[setup failed]") {
				report.Failed++
				report.Failures = append(report.Failures, testFailure{
					Name:    strings.TrimSpace(strings.TrimPrefix(trimmed, "FAIL\t")),
					Message: strings.Join(buildLines, "\n"),
					Type:    "error",
				})
				buildPkg = ""
				buildLines = nil
			} else if pkgFailedTests == 0 {
				// Package failed without any individual `--- FAIL:` test (e.g.
				// panic in TestMain or init). Fail closed to the original output
				// so stack traces and diagnostics are never dropped.
				return nil, fmt.Errorf("go test package failure without test-level failure: %s", trimmed)
			}
			pkgFailedTests = 0
		case trimmed == "PASS" || trimmed == "FAIL" || trimmed == "":
			continue
		default:
			if buildPkg != "" {
				buildLines = append(buildLines, raw)
			} else if currentTest != "" {
				currentBuf = append(currentBuf, raw)
			}
		}
	}
	if buildPkg != "" && len(buildLines) > 0 {
		report.Failed++
		report.Failures = append(report.Failures, testFailure{
			Name:    buildPkg + " [build failed]",
			Message: strings.Join(buildLines, "\n"),
			Type:    "error",
		})
	}
	total := report.Passed + report.Failed + report.Skipped
	if total < 4 {
		return nil, fmt.Errorf("too few go test results (%d)", total)
	}
	return report, nil
}

// parseCargoTest parses plain-text `cargo test` output, collapsing passing
// `test ... ok` lines while keeping failure details from the `failures:` section.
func parseCargoTest(input []byte) (*testReport, error) {
	clean := ansiRe.ReplaceAll(input, nil)
	if !bytes.Contains(clean, []byte("running ")) || !bytes.Contains(clean, []byte("test result:")) {
		return nil, fmt.Errorf("not cargo test output")
	}
	lines := strings.Split(string(clean), "\n")
	report := &testReport{}
	inFailures := false
	var curFailName string
	var curFailLines []string
	flushFail := func() {
		if curFailName == "" {
			return
		}
		report.Failures = append(report.Failures, testFailure{
			Name:    curFailName,
			Message: strings.TrimSpace(strings.Join(curFailLines, "\n")),
			Type:    "failure",
		})
		curFailName = ""
		curFailLines = curFailLines[:0]
	}
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "failures:" {
			inFailures = true
			continue
		}
		if strings.HasPrefix(trimmed, "test result:") {
			flushFail()
			inFailures = false
			continue
		}
		if inFailures {
			if strings.HasPrefix(trimmed, "---- ") && strings.HasSuffix(trimmed, " stdout ----") {
				flushFail()
				curFailName = strings.TrimSuffix(strings.TrimPrefix(trimmed, "---- "), " stdout ----")
			} else if curFailName != "" {
				curFailLines = append(curFailLines, raw)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "test ") {
			switch {
			case strings.HasSuffix(trimmed, " ... ok"):
				report.Passed++
			case strings.HasSuffix(trimmed, " ... FAILED"):
				report.Failed++
			case strings.HasSuffix(trimmed, " ... ignored"):
				report.Skipped++
			}
		}
	}
	flushFail()
	total := report.Passed + report.Failed + report.Skipped
	if total < 4 {
		return nil, fmt.Errorf("too few cargo test results (%d)", total)
	}
	return report, nil
}
