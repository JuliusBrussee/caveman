package compressors_test

import (
	"bytes"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
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

// Test detection

func TestTestReportDetection(t *testing.T) {
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
		{"pytest_json", `{"exitcode": 0, "tests": []}`, engine.TypeTestReport, true},
		{"jest_json", `{"numFailedTests": 0, "testResults": []}`, engine.TypeTestReport, true},
		{"jest_json_case_insensitive", `{"numfailedtests": 0, "testresults": []}`, engine.TypeTestReport, true},

		// False positives (should NOT match test-report)
		{"generic_json_with_tests_only", `{"tests": []}`, engine.TypeJSON, false},
		{"generic_json_with_testresults_only", `{"testResults": []}`, engine.TypeJSON, false},
		{"generic_json_with_exitcode_only", `{"exitcode": 0}`, engine.TypeJSON, false},
		{"generic_json_with_numfailedtests_only", `{"numFailedTests": 0}`, engine.TypeJSON, false},

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

// Test behavior

func TestTestReportFailClosed(t *testing.T) {
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
