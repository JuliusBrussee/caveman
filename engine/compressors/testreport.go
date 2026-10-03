package compressors

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
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
		return nil, false // not a recognized format
	}

	if err != nil || report == nil {
		return nil, false // parse failure → pass-through
	}

	// If 0 tests total, pass through (nothing to compress)
	if report.Passed+report.Failed+report.Skipped+report.Errors == 0 {
		return nil, false
	}

	// Render normalized output
	out := renderTestReport(report)
	return out, true
}

// testReport is the normalized internal representation of test results.
type testReport struct {
	Passed   int
	Failed   int
	Skipped  int
	Errors   int
	Duration float64 // seconds
	Failures []testFailure
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
	}

	type testSuites struct {
		XMLName xml.Name    `xml:"testsuites"`
		Suite   []testSuite `xml:"testsuite"`
	}

	type singleSuite struct {
		XMLName xml.Name `xml:"testsuite"`
		testSuite
	}

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

	report := &testReport{}
	for _, suite := range suites.Suite {
		// Parse duration
		if suite.Time != "" {
			var dur float64
			fmt.Sscanf(suite.Time, "%f", &dur)
			report.Duration += dur
		}

		for _, tc := range suite.TestCase {
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
			Call    struct {
				Longrepr string `json:"longrepr"`
			} `json:"call"`
		} `json:"tests"`
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
		switch test.Outcome {
		case "passed":
			report.Passed++
		case "failed":
			report.Failed++
			lines := strings.Split(test.Call.Longrepr, "\n")
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
			lines := strings.Split(test.Call.Longrepr, "\n")
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
		}
	}

	return report, nil
}

// parseJestJSON parses Jest JSON output format.
func parseJestJSON(input []byte) (*testReport, error) {
	var doc struct {
		NumFailedTests  int `json:"numFailedTests"`
		NumPassedTests  int `json:"numPassedTests"`
		NumPendingTests int `json:"numPendingTests"`
		TestResults     []struct {
			Name             string `json:"name"`
			AssertionResults []struct {
				AncestorTitles []string `json:"ancestorTitles"`
				Title          string   `json:"title"`
				FullName       string   `json:"fullName"`
				Status         string   `json:"status"`
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
		for _, test := range suite.AssertionResults {
			if test.Status == "failed" {
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
	}

	return report, nil
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
