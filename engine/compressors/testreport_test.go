package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// JUnit XML fixtures

func junitSingleSuite() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="suite1" tests="4" failures="1" errors="1" skipped="1" time="2.5">
  <testcase name="test_passed" classname="TestExample" time="0.5"/>
  <testcase name="test_failed" classname="TestExample" time="0.7">
    <failure message="expected 200, got 404" type="AssertionError">AssertionError: expected 200, got 404
  at TestExample.test_failed(TestExample.java:10)
  at sun.reflect.NativeMethodAccessorImpl.invoke0(Native Method)
  at sun.reflect.NativeMethodAccessorImpl.invoke(NativeMethodAccessorImpl.java:62)
  at org.junit.runners.model.FrameworkMethod$1.runReflectiveCall(FrameworkMethod.java:50)
  at org.junit.internal.runners.model.ReflectiveCallable.run(ReflectiveCallable.java:12)
  at org.junit.runners.model.FrameworkMethod.invokeExplosively(FrameworkMethod.java:47)
  at org.junit.internal.runners.statements.InvokeMethod.evaluate(InvokeMethod.java:17)
  at org.junit.runners.ParentRunner.runLeaf(ParentRunner.java:325)</failure>
  </testcase>
  <testcase name="test_error" classname="TestExample" time="0.8">
    <error message="NullPointerException" type="java.lang.NullPointerException">java.lang.NullPointerException
  at TestExample.test_error(TestExample.java:20)
  at sun.reflect.NativeMethodAccessorImpl.invoke0(Native Method)
  at sun.reflect.NativeMethodAccessorImpl.invoke(NativeMethodAccessorImpl.java:62)
  at sun.reflect.DelegatingMethodAccessorImpl.invoke(DelegatingMethodAccessorImpl.java:43)
  at java.lang.reflect.Method.invoke(Method.java:498)</error>
  </testcase>
  <testcase name="test_skipped" classname="TestExample" time="0.0">
    <skipped/>
  </testcase>
</testsuite>`)
}

func junitMultiSuite() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="suite1" tests="2" failures="1" time="1.0">
    <testcase name="test1" classname="Suite1" time="0.5"/>
    <testcase name="test2" classname="Suite1" time="0.5">
      <failure message="failed">failure message</failure>
    </testcase>
  </testsuite>
  <testsuite name="suite2" tests="1" failures="0" time="0.5">
    <testcase name="test3" classname="Suite2" time="0.5"/>
  </testsuite>
</testsuites>`)
}

// pytest JSON fixtures

func pytestMixed() []byte {
	return []byte(`{
  "exitcode": 1,
  "duration": 3.2,
  "tests": [
    {
      "nodeid": "tests/test_example.py::test_passed",
      "outcome": "passed",
      "call": {"longrepr": ""}
    },
    {
      "nodeid": "tests/test_example.py::test_failed",
      "outcome": "failed",
      "call": {"longrepr": "E AssertionError: expected 401, got 200\n  at test_example.py:45 in test_failed\n  at auth.py:112 in validate_token\n  at test_example.py:50 in teardown\n  at core.py:20 in cleanup\n  at test_example.py:60 in final"}
    },
    {
      "nodeid": "tests/test_example.py::test_skipped",
      "outcome": "skipped",
      "call": {"longrepr": ""}
    },
    {
      "nodeid": "tests/test_example.py::test_error",
      "outcome": "error",
      "call": {"longrepr": "ImportError: module not found\n  at test_example.py:10\n  at importer.py:20"}
    }
  ]
}`)
}

// Jest JSON fixtures

func jestMixed() []byte {
	return []byte(`{
  "numFailedTests": 1,
  "numPassedTests": 2,
  "numPendingTests": 0,
  "testResults": [
    {
      "name": "LoginForm.test.tsx",
      "assertionResults": [
        {
          "fullName": "LoginForm validates email format",
          "status": "passed"
        },
        {
          "fullName": "LoginForm shows error on invalid password",
          "status": "failed",
          "failureMessages": ["expect(received).toBe(expected)\nExpected: \"valid@email.com\"\nReceived: \"\"\n  at LoginForm.test.tsx:45 in validates email\n  at LoginForm.test.tsx:50 in teardown\n  at test-utils.ts:20 in cleanup"]
        }
      ]
    },
    {
      "name": "UserAPI.test.ts",
      "assertionResults": [
        {
          "ancestorTitles": ["UserAPI", "authentication"],
          "title": "rejects invalid tokens",
          "status": "passed"
        }
      ]
    }
  ]
}`)
}

// Test format parsing

func TestTestReportJUnitXML(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	t.Run("single_suite", func(t *testing.T) {
		out, ok := c.Compress(junitSingleSuite())
		if !ok {
			t.Fatal("expected JUnit XML compression")
		}
		// Verify summary line
		if !bytes.Contains(out, []byte("TEST SUMMARY:")) {
			t.Error("expected TEST SUMMARY line")
		}
		if !bytes.Contains(out, []byte("1 passed")) {
			t.Error("expected 1 passed")
		}
		if !bytes.Contains(out, []byte("1 failed")) {
			t.Error("expected 1 failed")
		}
		if !bytes.Contains(out, []byte("1 errors")) {
			t.Error("expected 1 errors")
		}
		if !bytes.Contains(out, []byte("1 skipped")) {
			t.Error("expected 1 skipped")
		}

		// Verify failure details
		if !bytes.Contains(out, []byte("FAILED: TestExample::test_failed")) {
			t.Error("expected FAILED line for test_failed")
		}
		if !bytes.Contains(out, []byte("expected 200, got 404")) {
			t.Error("expected failure message")
		}

		// Verify error details
		if !bytes.Contains(out, []byte("ERROR: TestExample::test_error")) {
			t.Error("expected ERROR line for test_error")
		}
		if !bytes.Contains(out, []byte("NullPointerException")) {
			t.Error("expected error message")
		}

		// Verify stack trimming (should have "... (N frames omitted)" marker)
		if !bytes.Contains(out, []byte("frames omitted")) {
			t.Errorf("expected stack trimming, got:\n%s", out)
		}
	})

	t.Run("multi_suite", func(t *testing.T) {
		out, ok := c.Compress(junitMultiSuite())
		if !ok {
			t.Fatal("expected JUnit XML compression")
		}
		if !bytes.Contains(out, []byte("2 passed")) {
			t.Error("expected 2 passed")
		}
		if !bytes.Contains(out, []byte("1 failed")) {
			t.Error("expected 1 failed")
		}
		if !bytes.Contains(out, []byte("FAILED: Suite1::test2")) {
			t.Error("expected FAILED line for test2")
		}
	})

	t.Run("outcome_categorization", func(t *testing.T) {
		// Create minimal XML with specific outcomes
		xml := []byte(`<testsuite tests="4" failures="1" errors="1" skipped="1">
  <testcase name="t1"/>
  <testcase name="t2"><failure message="f"/></testcase>
  <testcase name="t3"><error message="e"/></testcase>
  <testcase name="t4"><skipped/></testcase>
</testsuite>`)
		out, ok := c.Compress(xml)
		if !ok {
			t.Fatal("expected compression")
		}
		if !bytes.Contains(out, []byte("1 passed")) {
			t.Error("t1 should be passed")
		}
		if !bytes.Contains(out, []byte("1 failed")) {
			t.Error("t2 should be failed")
		}
		if !bytes.Contains(out, []byte("1 errors")) {
			t.Error("t3 should be error")
		}
		if !bytes.Contains(out, []byte("1 skipped")) {
			t.Error("t4 should be skipped")
		}
	})
}

func TestTestReportPytestJSON(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	out, ok := c.Compress(pytestMixed())
	if !ok {
		t.Fatal("expected pytest JSON compression")
	}

	// Verify summary
	if !bytes.Contains(out, []byte("1 passed")) {
		t.Error("expected 1 passed")
	}
	if !bytes.Contains(out, []byte("1 failed")) {
		t.Error("expected 1 failed")
	}
	if !bytes.Contains(out, []byte("1 skipped")) {
		t.Error("expected 1 skipped")
	}
	if !bytes.Contains(out, []byte("1 errors")) {
		t.Error("expected 1 errors")
	}

	// Verify failure details
	if !bytes.Contains(out, []byte("FAILED: tests/test_example.py::test_failed")) {
		t.Error("expected FAILED line for test_failed")
	}
	if !bytes.Contains(out, []byte("AssertionError: expected 401, got 200")) {
		t.Error("expected failure message")
	}

	// Verify error details
	if !bytes.Contains(out, []byte("ERROR: tests/test_example.py::test_error")) {
		t.Error("expected ERROR line for test_error")
	}
	if !bytes.Contains(out, []byte("ImportError: module not found")) {
		t.Error("expected error message")
	}

	// This pytest fixture only has 5 frames, which is less than minToTrim (8),
	// so stack trimming should NOT occur
	if bytes.Contains(out, []byte("frames omitted")) {
		t.Errorf("should not trim stack with only 5 frames, got:\n%s", out)
	}
}

func TestTestReportJestJSON(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	out, ok := c.Compress(jestMixed())
	if !ok {
		t.Fatal("expected Jest JSON compression")
	}

	// Verify summary
	if !bytes.Contains(out, []byte("2 passed")) {
		t.Error("expected 2 passed")
	}
	if !bytes.Contains(out, []byte("1 failed")) {
		t.Error("expected 1 failed")
	}

	// Verify failure details with fullName
	if !bytes.Contains(out, []byte("FAILED: LoginForm shows error on invalid password")) {
		t.Errorf("expected FAILED line with fullName, got:\n%s", out)
	}
	if !bytes.Contains(out, []byte("expect(received).toBe(expected)")) {
		t.Error("expected failure message")
	}

	// Verify ancestorTitles+title fallback
	// The third test uses ancestorTitles + title instead of fullName
	// It should be passed and not appear in failures
	if bytes.Contains(out, []byte("FAILED: UserAPI authentication rejects invalid tokens")) {
		t.Error("passed test should not appear in failures")
	}
}

// TestTestReportJestSuiteFailures pins that a suite Jest could not run — a
// module that fails to load, a throwing afterAll — reaches the summary as an
// error. Jest records those only on the suite (status "failed" plus a message,
// counted in numFailedTestSuites), never in numFailedTests, so a summary built
// from the assertion counts alone reported a red run as all-passing. Messages
// trimmed from real `jest --json` output (Jest 29.7.0; the first one was
// captured with FORCE_COLOR=1, so it carries ANSI escapes).
func TestTestReportJestSuiteFailures(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()
	out, ok := c.Compress([]byte(`{"numFailedTests":0,"numPassedTests":3,"numPendingTests":0,"numFailedTestSuites":2,"success":false,"testResults":[
		{"name":"/repo/src/user.test.js","status":"failed","assertionResults":[],
		 "message":"  \u001b[1m● \u001b[22mTest suite failed to run\n\n    Cannot find module './db' from 'src/user.test.js'\n\n    \u001b[0m\u001b[31m\u001b[1m>\u001b[22m\u001b[39m\u001b[90m 1 |\u001b[39m \u001b[36mconst\u001b[39m db \u001b[33m=\u001b[39m require(\u001b[32m'./db'\u001b[39m)\u001b[33m;\u001b[39m"},
		{"name":"/repo/src/hook.test.js","status":"failed","assertionResults":[{"fullName":"works","status":"passed"}],
		 "message":"\n\n  ● Test suite failed to run\n\n    afterAll boom: pool not closed\n\n    > 1 | afterAll(() => { throw new Error('afterAll boom: pool not closed'); });\n\n      at Object.<anonymous> (src/hook.test.js:1:24)"},
		{"name":"/repo/src/ok.test.js","status":"passed","message":"","assertionResults":[{"fullName":"adds","status":"passed"},{"fullName":"subs","status":"passed"}]}]}`))
	if !ok {
		t.Fatal("expected Jest JSON compression")
	}
	for _, want := range []string{
		"TEST SUMMARY: 3 passed, 2 errors",
		"ERROR: /repo/src/user.test.js\n  Cannot find module './db' from 'src/user.test.js'",
		"ERROR: /repo/src/hook.test.js\n  afterAll boom: pool not closed",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("\x1b")) {
		t.Errorf("ANSI escape leaked into the summary:\n%q", out)
	}

	// A suite that failed because one of its own tests failed is already
	// reported through that test; it must not be counted a second time.
	out, ok = c.Compress([]byte(`{"numFailedTests":1,"numPassedTests":1,"numPendingTests":0,"testResults":[
		{"name":"/repo/src/math.test.js","status":"failed","message":"  ● math › divides\n\n    expect(received).toBe(expected)",
		 "assertionResults":[{"fullName":"math adds","status":"passed"},{"fullName":"math divides","status":"failed","failureMessages":["expect(received).toBe(expected)"]}]}]}`))
	if !ok {
		t.Fatal("expected Jest JSON compression")
	}
	if !bytes.Contains(out, []byte("TEST SUMMARY: 1 passed, 1 failed\n")) || bytes.Contains(out, []byte("ERROR:")) {
		t.Errorf("assertion failure double-counted as a suite error:\n%s", out)
	}
}

// TestTestReportPytestCollectionErrors pins the pytest sibling of the Jest gap
// above. A test module pytest could not import is recorded only under
// `collectors[]` (outcome "failed", reason in longrepr); it never reaches
// `tests[]` and the json-report `summary` has no error key either, so a report
// built from `tests[]` alone summarized a red run as all-passing.
//
// Reachable whenever pytest is told to keep going after a collection error:
// `--continue-on-collection-errors` leaves the surviving tests in `tests[]`, so
// the payload has tests and is compressed rather than passed through. Shapes and
// longreprs below are trimmed from real runs (pytest 9.2.0, pytest-json-report
// 1.5.0); that run printed "1 passed, 1 skipped, 2 errors" and exited 1.
func TestTestReportPytestCollectionErrors(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()
	out, ok := c.Compress([]byte(`{"exitcode":1,"duration":0.03,
		"summary":{"passed":1,"total":1,"collected":1},
		"tests":[{"nodeid":"test_ok.py::test_a","outcome":"passed"}],
		"collectors":[
			{"nodeid":"","outcome":"passed","result":[]},
			{"nodeid":"test_bad1.py","outcome":"failed","longrepr":"ImportError while importing test module '/repo/test_bad1.py'.\nHint: make sure your test modules/packages have valid Python names.\nTraceback:\n/usr/lib/python3.11/importlib/__init__.py:126: in import_module\n    return _bootstrap._gcd_import(name[level:], package, level)\ntest_bad1.py:1: in <module>\n    import nope_one\nE   ModuleNotFoundError: No module named 'nope_one'"},
			{"nodeid":"test_bad2.py","outcome":"failed","longrepr":"test_bad2.py:1: in <module>\n    raise RuntimeError(\"module body exploded\")\nE   RuntimeError: module body exploded"},
			{"nodeid":"test_modskip.py","outcome":"skipped","longrepr":"Skipped: needs hardware"},
			{"nodeid":"test_ok.py","outcome":"passed","result":[]},
			{"nodeid":".","outcome":"passed","result":[]}]}`))
	if !ok {
		t.Fatal("expected pytest JSON compression")
	}
	for _, want := range []string{
		"TEST SUMMARY: 1 passed, 2 errors",
		"ERROR: test_bad1.py\n  ModuleNotFoundError: No module named 'nope_one'",
		"ERROR: test_bad2.py\n  RuntimeError: module body exploded",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Only the two failed collectors count. A passed collector is not an
	// error, and a module skipped at collection is not one either — pytest
	// reported that one as a skip, not as the third error.
	if bytes.Contains(out, []byte("3 errors")) || bytes.Contains(out, []byte("ERROR: test_modskip.py")) ||
		bytes.Contains(out, []byte("ERROR: test_ok.py")) || bytes.Contains(out, []byte("ERROR: .")) {
		t.Errorf("counted a collector that did not fail:\n%s", out)
	}

	// pytest's default: collection aborts, tests[] is empty. Previously the
	// whole payload passed through on the 0-tests check; the error is now
	// reported, matching what the Jest sibling does for a load failure.
	out, ok = c.Compress([]byte(`{"exitcode":2,"summary":{"total":0,"collected":2},"tests":[],
		"collectors":[{"nodeid":"test_bad1.py","outcome":"failed","longrepr":"test_bad1.py:1: in <module>\n    import nope_one\nE   ModuleNotFoundError: No module named 'nope_one'"}]}`))
	if !ok {
		t.Fatal("expected an interrupted collection to compress, not pass through")
	}
	if !bytes.Contains(out, []byte("TEST SUMMARY: 1 errors")) ||
		!bytes.Contains(out, []byte("ModuleNotFoundError: No module named 'nope_one'")) {
		t.Errorf("interrupted collection lost its reason:\n%s", out)
	}

	// pytest on Windows writes its longrepr lines CRLF-separated, and the
	// reason is the line AFTER the frames, so a stray \r would ride into the
	// summary with it.
	out, ok = c.Compress([]byte("{\"tests\":[{\"nodeid\":\"t.py::a\",\"outcome\":\"passed\"}]," +
		"\"collectors\":[{\"nodeid\":\"test_bad.py\",\"outcome\":\"failed\"," +
		"\"longrepr\":\"test_bad.py:1: in <module>\\r\\n    import nope\\r\\nE   ModuleNotFoundError: No module named 'nope'\"}]}"))
	if !ok {
		t.Fatal("expected a CRLF longrepr to compress")
	}
	if !bytes.Contains(out, []byte("ModuleNotFoundError: No module named 'nope'")) {
		t.Errorf("CRLF longrepr lost its reason:\n%s", out)
	}
	if bytes.Contains(out, []byte("\r")) {
		t.Errorf("a bare CR rode into the summary:\n%q", out)
	}

	// A clean run carries only passed collectors; none of them may become an
	// error, and the summary must be byte-identical to the no-collectors form.
	withCollectors, ok1 := c.Compress([]byte(`{"exitcode":0,"tests":[{"nodeid":"t.py::a","outcome":"passed"}],
		"collectors":[{"nodeid":"","outcome":"passed"},{"nodeid":"t.py","outcome":"passed"}]}`))
	without, ok2 := c.Compress([]byte(`{"exitcode":0,"tests":[{"nodeid":"t.py::a","outcome":"passed"}]}`))
	if !ok1 || !ok2 {
		t.Fatal("expected both passing reports to compress")
	}
	if !bytes.Equal(withCollectors, without) {
		t.Errorf("passed collectors changed a green summary:\n%s\nvs\n%s", withCollectors, without)
	}
}

// Test detection

func TestTestReportDetection(t *testing.T) {
	t.Parallel()
	e := engine.New(nil, nil)

	cases := []struct {
		name      string
		input     string
		wantType  string
		wantMatch bool
	}{
		// True positives
		{"junit_xml", `<testsuite name="suite"><testcase name="t1"/></testsuite>`, engine.TypeTestReport, true},
		{"junit_xml_multi", `<testsuites><testsuite><testcase/></testsuite></testsuites>`, engine.TypeTestReport, true},
		{"pytest_json", `{"exitcode": 0, "tests": [{"nodeid": "t.py::a", "outcome": "passed"}]}`, engine.TypeTestReport, true},
		{"jest_json", `{"numFailedTests": 0, "numPassedTests": 1, "testResults": []}`, engine.TypeTestReport, true},

		// False positives (should NOT match test-report)
		{"generic_json_with_tests_only", `{"tests": []}`, engine.TypeJSON, false},
		{"generic_json_with_testresults_only", `{"testResults": []}`, engine.TypeJSON, false},
		{"generic_json_with_exitcode_only", `{"exitcode": 0}`, engine.TypeJSON, false},
		{"generic_json_with_numfailedtests_only", `{"numFailedTests": 0}`, engine.TypeJSON, false},
		// Jest emits camelCase keys; lowercase spellings are some other JSON.
		{"lowercase_jest_keys", `{"numfailedtests": 0, "testresults": []}`, engine.TypeJSON, false},

		// Non-JUnit XML
		{"other_xml", `<test>not junit</test>`, engine.TypeText, false},

		// Valid JSON should still route to JSON when not test-report
		{"valid_json_object", `{"a": 1, "b": 2}`, engine.TypeJSON, false},
		{"valid_json_array", `[1, 2, 3]`, engine.TypeJSON, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := e.Detect([]byte(tc.input))
			if tc.wantMatch {
				if got != tc.wantType {
					t.Errorf("Detect(%q) = %q, want %q", tc.name, got, tc.wantType)
				}
			} else {
				// For false positives, just verify it doesn't route to test-report
				if got == engine.TypeTestReport {
					t.Errorf("Detect(%q) = %q, should NOT be test-report", tc.name, got)
				}
			}
		})
	}
}

// TestTestReportDetectionIsAnchored pins that detection reads only top-level
// shape. Payloads that merely mention JUnit tags or pytest keys somewhere inside
// must keep their own type: the test-report parser rejects them, and a rejected
// payload passes through uncompressed instead of getting diff/code/JSON
// compression.
func TestTestReportDetectionIsAnchored(t *testing.T) {
	t.Parallel()
	e := engine.New(nil, nil)
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"diff editing junit testcases", "diff --git a/report.xml b/report.xml\n--- a/report.xml\n+++ b/report.xml\n@@ -1,4 +1,4 @@\n <testsuite name=\"s\">\n-  <testcase name=\"old\"/>\n+  <testcase name=\"new\"/>\n </testsuite>\n", engine.TypeDiff},
		{"go source printing junit tags", "package report\n\nimport \"fmt\"\n\nfunc emit() {\n\tfmt.Println(\"<testsuite name=\\\"x\\\">\")\n\tfmt.Println(\"<testcase name=\\\"y\\\"/>\")\n\tfmt.Println(\"</testsuite>\")\n}\n", engine.TypeCode},
		{"json with nested exitcode", `{"job":{"steps":[{"exitcode":0}]},"tests":["a"]}`, engine.TypeJSON},
		{"json with nested jest keys", `{"run":{"numFailedTests":0},"testResults":[]}`, engine.TypeJSON},
		{"junit with xml prolog and comment", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- generated -->\n<testsuites><testsuite name=\"s\"><testcase name=\"t\"/></testsuite></testsuites>", engine.TypeTestReport},
		{"junit with ansi-colored failure", "<testsuite name=\"s\"><testcase name=\"t\"><failure message=\"boom\">\x1b[31mexpected 1, got 2\x1b[0m</failure></testcase></testsuite>", engine.TypeTestReport},
		{"pytest report", `{"exitcode":1,"duration":0.2,"tests":[{"nodeid":"t.py::a","outcome":"failed"}]}`, engine.TypeTestReport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.Detect([]byte(tc.input)); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

// junitCases renders n <testcase> rows; every tenth fails with a message
// colored the way test runners print it to a terminal when ansi is set.
func junitCases(n int, ansi bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i%10 != 0 {
			fmt.Fprintf(&b, "<testcase classname=\"InventoryTest\" name=\"test_sku_%03d\" time=\"0.01\"/>\n", i)
			continue
		}
		body := fmt.Sprintf("expected %d, got %d\n  at InventoryTest.java:%d", i, i+1, 40+i)
		if ansi {
			body = "\x1b[31m" + body + "\x1b[0m"
		}
		fmt.Fprintf(&b, "<testcase classname=\"InventoryTest\" name=\"test_sku_%03d\" time=\"0.01\"><failure message=\"mismatch\">%s</failure></testcase>\n", i, body)
	}
	return b.String()
}

// TestTestReportDetectedShapesCompress pins that a payload Detect routes to
// test-report really compresses through the engine. Engine.Compress has no
// fallback when the routed compressor declines, and the native runtime skips
// masking test-report output, so a shape detected here but rejected by the
// parser would be neither masked nor compressed.
func TestTestReportDetectedShapesCompress(t *testing.T) {
	t.Parallel()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e := engine.New(store, nil)
	cases := []struct {
		name, input, summary string
	}{
		// encoding/xml rejects a raw ESC (U+001B) as an illegal XML character.
		{"ansi-colored junit", "<testsuite name=\"inventory\">\n" + junitCases(80, true) + "</testsuite>", "TEST SUMMARY: 72 passed, 8 failed"},
		// PHPUnit nests a suite per class inside a suite per directory.
		{"phpunit nested suites", "<?xml version=\"1.0\"?>\n<testsuites><testsuite name=\"all\"><testsuite name=\"Unit\">\n" + junitCases(40, false) + "</testsuite><testsuite name=\"Feature\">\n" + junitCases(40, false) + "</testsuite></testsuite></testsuites>", "TEST SUMMARY: 72 passed, 8 failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.input)
			if got := e.Detect(in); got != engine.TypeTestReport {
				t.Fatalf("Detect = %q, want %q", got, engine.TypeTestReport)
			}
			res, err := e.Compress(in, engine.Options{Mode: engine.ModeCompress})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Output) >= len(in) || res.RecoveryHandle == "" {
				t.Fatalf("not compressed: in=%d out=%d handle=%q", len(in), len(res.Output), res.RecoveryHandle)
			}
			if !bytes.Contains(res.Output, []byte(tc.summary)) {
				t.Errorf("summary missing %q:\n%s", tc.summary, res.Output)
			}
		})
	}
}

// TestTestReportDetectionRequiresParse pins that a test-report root the
// compressor cannot read keeps its pre-test-report routing instead of being
// claimed by a compressor that will decline it.
func TestTestReportDetectionRequiresParse(t *testing.T) {
	t.Parallel()
	e := engine.New(nil, nil)
	full := string(junitSingleSuite())
	for name, input := range map[string]string{
		"truncated junit":    full[:len(full)/2],
		"zero-test junit":    `<testsuite tests="0" failures="0"/>`,
		"zero-test pytest":   `{"exitcode": 5, "tests": []}`,
		"pytest wrong types": `{"exitcode": "1", "tests": [{"outcome": "failed"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := e.Detect([]byte(input)); got == engine.TypeTestReport {
				t.Errorf("Detect = %q for a report the compressor rejects", got)
			}
		})
	}
}

// TestTestReportPytestKeepsEveryOutcome pins that outcomes beyond
// passed/failed/skipped/error still reach the summary, and that a setup or
// teardown error keeps its reason (pytest-json-report stores it under that
// stage, not under call).
func TestTestReportPytestKeepsEveryOutcome(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()
	out, ok := c.Compress([]byte(`{"exitcode":1,"tests":[
		{"nodeid":"t.py::a","outcome":"passed"},
		{"nodeid":"t.py::b","outcome":"xfailed"},
		{"nodeid":"t.py::c","outcome":"xpassed"},
		{"nodeid":"t.py::d","outcome":"error","setup":{"longrepr":"E fixture 'db' not found"}},
		{"nodeid":"t.py::e","outcome":"error","teardown":{"longrepr":"E RuntimeError: leaked connection"}}]}`))
	if !ok {
		t.Fatal("expected compression")
	}
	for _, want := range []string{"1 passed", "2 errors", "1 xfailed", "1 xpassed", "ERROR: t.py::d\n  E fixture 'db' not found", "ERROR: t.py::e\n  E RuntimeError: leaked connection"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Test behavior

func TestTestReportFailClosed(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	cases := []struct {
		name  string
		input []byte
	}{
		{"malformed_xml", []byte(`<testsuite><testcase></testsuite>`)},
		{"malformed_json", []byte(`{"exitcode": 0, "tests": [}`)},
		{"non_utf8", []byte{0xff, 0xfe, 0x00, 0x01}},
		{"empty", []byte{}},
		{"empty_suite_zero_tests", []byte(`<testsuite tests="0" failures="0"/>`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := c.Compress(tc.input)
			if ok {
				t.Errorf("%s: expected !ok (pass-through), got ok=true", tc.name)
			}
		})
	}
}

func TestTestReportIdempotent(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	// Test with each format
	formats := []struct {
		name  string
		input []byte
	}{
		{"junit", junitSingleSuite()},
		{"pytest", pytestMixed()},
		{"jest", jestMixed()},
	}

	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			first, ok := c.Compress(tc.input)
			if !ok {
				t.Fatal("expected first compression to succeed")
			}

			// The test-report compressor outputs normalized text.
			// Re-compressing that text should fail (!ok) because it's no longer
			// in a recognized test-report format (JUnit/pytest/Jest).
			// This is the correct behavior - the compressor is idempotent
			// because it produces output that cannot be re-compressed.
			_, secondOk := c.Compress(first)

			// If secondOk is true, verify the output is unchanged
			// (this shouldn't happen, but we check for safety)
			if secondOk {
				t.Errorf("re-compressing normalized output should not succeed, but got ok=true")
			}
		})
	}
}

func TestTestReportAllPassed(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	// All-passed report with no failures
	xml := []byte(`<testsuite tests="5" failures="0" errors="0" skipped="0" time="1.5">
  <testcase name="t1"/>
  <testcase name="t2"/>
  <testcase name="t3"/>
  <testcase name="t4"/>
  <testcase name="t5"/>
</testsuite>`)

	out, ok := c.Compress(xml)
	if !ok {
		t.Fatal("expected compression even when all passed")
	}

	// Should have summary line
	if !bytes.Contains(out, []byte("TEST SUMMARY:")) {
		t.Error("expected TEST SUMMARY line")
	}
	if !bytes.Contains(out, []byte("5 passed")) {
		t.Error("expected 5 passed")
	}

	// Should NOT have any FAILED or ERROR blocks
	if bytes.Contains(out, []byte("FAILED:")) {
		t.Error("should not have FAILED block when all passed")
	}
	if bytes.Contains(out, []byte("ERROR:")) {
		t.Error("should not have ERROR block when all passed")
	}

	// Verify output is short (just summary)
	if len(out) > 100 {
		t.Errorf("expected short output (just summary), got %d bytes: %s", len(out), out)
	}
}

func TestTestReportStackTrimming(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()

	// Create a failure with a long stack (more than keepHead+keepTail+3 = 8 frames)
	// Note: "failed" appears twice in the text but only lines starting with "at" are kept as stack frames
	xml := []byte(`<testsuite tests="1" failures="1">
  <testcase name="test_with_long_stack">
    <failure message="failed">
  at frame1.go:1
  at frame2.go:2
  at frame3.go:3
  at frame4.go:4
  at frame5.go:5
  at frame6.go:6
  at frame7.go:7
  at frame8.go:8
  at frame9.go:9
  at frame10.go:10</failure>
  </testcase>
</testsuite>`)

	out, ok := c.Compress(xml)
	if !ok {
		t.Fatal("expected compression")
	}

	// Should have trimming marker
	if !bytes.Contains(out, []byte("frames omitted")) {
		t.Errorf("expected stack trimming for long stack, got:\n%s", out)
	}

	// Should have first 3 frames (keepHead=3)
	if !bytes.Contains(out, []byte("frame1.go:1")) {
		t.Error("expected first frame to be kept")
	}
	if !bytes.Contains(out, []byte("frame2.go:2")) {
		t.Error("expected second frame to be kept")
	}
	if !bytes.Contains(out, []byte("frame3.go:3")) {
		t.Errorf("expected third frame to be kept, got:\n%s", out)
	}

	// Should have last 2 frames (keepTail=2)
	if !bytes.Contains(out, []byte("frame9.go:9")) {
		t.Error("expected second-to-last frame to be kept")
	}
	if !bytes.Contains(out, []byte("frame10.go:10")) {
		t.Error("expected last frame to be kept")
	}

	// Should NOT have middle frames
	if bytes.Contains(out, []byte("frame5.go:5")) {
		t.Error("middle frames should be trimmed")
	}
	if bytes.Contains(out, []byte("frame6.go:6")) {
		t.Error("middle frames should be trimmed")
	}

	// Test short stack (less than minToTrim = 8 frames, no trimming)
	shortXML := []byte(`<testsuite tests="1" failures="1">
  <testcase name="test_short">
    <failure message="failed">
  at frame1.go:1
  at frame2.go:2
  at frame3.go:3
  at frame4.go:4
  at frame5.go:5</failure>
  </testcase>
</testsuite>`)

	out2, ok := c.Compress(shortXML)
	if !ok {
		t.Fatal("expected compression")
	}

	// Should NOT have trimming marker for short stack (5 frames < minToTrim=8)
	if bytes.Contains(out2, []byte("frames omitted")) {
		t.Errorf("should not trim short stack, got:\n%s", out2)
	}

	// Should have all frames
	if !bytes.Contains(out2, []byte("frame1.go:1")) {
		t.Error("expected first frame")
	}
	if !bytes.Contains(out2, []byte("frame5.go:5")) {
		t.Error("expected last frame")
	}
}

func TestTestReportGoBuildFailurePreservesDiagnostics(t *testing.T) {
	t.Parallel()
	c := compressors.NewTestReport()
	raw := []byte(`# github.com/example/app/broken
broken/main.go:12:5: undefined: doesNotExist
broken/main.go:19:2: cannot use x (variable of type string) as int value
FAIL	github.com/example/app/broken [build failed]
=== RUN   TestOne
--- PASS: TestOne (0.00s)
=== RUN   TestTwo
--- PASS: TestTwo (0.00s)
=== RUN   TestThree
--- PASS: TestThree (0.00s)
=== RUN   TestFour
--- PASS: TestFour (0.00s)
PASS
ok  	github.com/example/app/okpkg	0.012s
`)
	out, ok := c.Compress(raw)
	if !ok {
		// Fail-closed passthrough is also acceptable; either way diagnostics cannot be dropped.
		return
	}
	for _, want := range []string{
		"1 failed",
		"github.com/example/app/broken [build failed]",
		"undefined: doesNotExist",
		"cannot use x",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("compressed go test output dropped %q:\n%s", want, out)
		}
	}
}
