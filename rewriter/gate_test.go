package rewriter

import (
	"strings"
	"testing"
)

// pytestRun is the shape AgentDiet's own worked example condenses (§2.2,
// Fig. 2): a long list of passing tests around one real failure.
const pytestRun = `$ python -m pytest tests/ -q
tests/test_alpha.py::test_one PASSED
tests/test_alpha.py::test_two PASSED
tests/test_alpha.py::test_three PASSED
tests/test_alpha.py::test_four PASSED
tests/test_gamma.py::test_five PASSED
tests/test_gamma.py::test_six PASSED
tests/test_beta.py::test_edge FAILED
=================================== FAILURES ===================================
tests/test_beta.py:142: in test_edge
    assert normalise(value) == expected
E   AssertionError: assert 'a' == 'b'
=========================== short test summary info ============================
FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'
1 failed, 214 passed in 12.44s
exit code 1`

// goVetRun mixes a make wrapper (extensionless location) with a toolchain
// failure, so both reference shapes are exercised on one block.
const goVetRun = `$ make vet
go vet ./...
ok      github.com/example/app/internal/api
ok      github.com/example/app/internal/store
ok      github.com/example/app/internal/worker
internal/worker/loop.go:203:6: Errorf format %d has arg name of wrong type string
make: *** [Makefile:12: vet] Error 2
exit status 2`

func TestAcceptTable(t *testing.T) {
	cases := []struct {
		name      string
		original  string
		rewritten string
		theta     int
		want      string
	}{
		{
			name:     "elides passing bulk, keeps the failure",
			original: pytestRun,
			rewritten: `$ python -m pytest tests/ -q
[individual test lines omitted; 214 PASSED]
tests/test_beta.py::test_edge FAILED
tests/test_beta.py:142: E   AssertionError: assert 'a' == 'b'
FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'
1 failed, 214 passed in 12.44s
exit code 1`,
			want: "",
		},
		{
			name:     "drops the failure entirely",
			original: pytestRun,
			rewritten: `$ python -m pytest tests/ -q
[test lines omitted]
215 passed in 12.44s`,
			want: reasonFailureSignal,
		},
		{
			name:     "keeps the signal but loses the file path",
			original: pytestRun,
			rewritten: `$ python -m pytest tests/ -q
1 failed, 214 passed; an AssertionError in the beta suite
exit code 1`,
			want: reasonReference,
		},
		{
			name:     "keeps the failure but drops the non-zero exit code",
			original: pytestRun,
			rewritten: `$ python -m pytest tests/ -q
[individual test lines omitted; 214 PASSED]
FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'
tests/test_beta.py:142
1 failed, 214 passed in 12.44s`,
			want: reasonExitCode,
		},
		{
			name:     "keeps both reference shapes",
			original: goVetRun,
			rewritten: `$ make vet
[3 ok package lines omitted]
internal/worker/loop.go:203:6: Errorf format %d has arg name of wrong type string
make: *** [Makefile:12: vet] Error 2
exit status 2`,
			want: "",
		},
		{
			name:     "loses the extensionless make location",
			original: goVetRun,
			rewritten: `$ make vet
[ok lines omitted]
internal/worker/loop.go:203:6: Errorf format %d has arg name of wrong type string
make: vet target failed with Error 2
exit status 2`,
			want: reasonReference,
		},
		{
			name:      "empty rewrite",
			original:  pytestRun,
			rewritten: "   \n\t ",
			want:      reasonEmpty,
		},
		{
			name:      "rewrite is larger than the original",
			original:  pytestRun,
			rewritten: pytestRun + "\n" + pytestRun,
			want:      reasonTokens,
		},
		{
			name:      "saving does not clear theta",
			original:  pytestRun,
			rewritten: strings.TrimSuffix(pytestRun, "\nexit code 1"),
			theta:     DefaultTheta,
			want:      reasonTokens,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := accept([]byte(tc.original), []byte(tc.rewritten), tc.theta)
			if got != tc.want {
				t.Fatalf("accept() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Source locations are protected everywhere in the block, not only on lines
// that happen to carry a failure word. Most toolchains put the location on its
// own line with no signal on it, which is exactly how a hostile rewrite slips
// past a failure-line-scoped gate.
func TestAcceptRejectsElidedCompilerLocations(t *testing.T) {
	original := `$ go build ./...
# github.com/example/app/internal/store
internal/store/query.go:88:14: undefined: mustQuery
internal/store/query.go:91:2: declared and not used: rows
internal/store/query.go:97:9: cannot use n (variable of type int) as string
exit status 2`
	rewritten := `$ go build ./...
[3 diagnostics omitted: undefined mustQuery; unused rows; cannot use n as string]
exit status 2`

	if got := accept([]byte(original), []byte(rewritten), 0); got != reasonReference {
		t.Fatalf("accept() = %q, want %q", got, reasonReference)
	}
}

// Every row is a rewrite that keeps the block looking plausible while dropping
// something the agent needs. These were produced by an adversarial probe
// against an earlier gate that accepted all fifteen.
func TestAcceptRejectsKnownHostileRewrites(t *testing.T) {
	cases := []struct {
		name      string
		original  string
		rewritten string
		want      string
	}{
		{
			name: "go build compile diagnostics",
			original: `$ go build ./...
go: downloading github.com/example/dep v1.4.0
go: downloading github.com/example/other v0.9.2
# github.com/example/app/internal/store
internal/store/query.go:88:14: undefined: mustQuery
internal/store/query.go:91:2: declared and not used: rows
exit status 2`,
			rewritten: `$ go build ./...
[compile diagnostics omitted]
exit status 2`,
			want: reasonReference,
		},
		{
			name: "rustc error with arrow location",
			original: `$ cargo build
   Compiling parser v0.3.1
   Compiling app v0.3.1
error[E0308]: mismatched types
  --> src/parser.rs:88:21
   |
88 |     let n: u32 = value;
   |                  ^^^^^ expected u32, found String
error: could not compile app
exit code 101`,
			rewritten: `$ cargo build
error[E0308]: mismatched types; could not compile app
exit code 101`,
			want: reasonReference,
		},
		{
			name: "rust panicked at location",
			original: `$ cargo test --release
   Compiling app v0.3.1
    Finished release profile
     Running unittests src/lib.rs
thread 'main' panicked at src/lib.rs:42:9:
index out of bounds: the len is 3 but the index is 7
note: run with RUST_BACKTRACE=1 environment variable to display a backtrace
exit code 101`,
			rewritten: `$ cargo test --release
thread 'main' panicked; rerun with a backtrace for detail
exit code 101`,
			want: reasonReference,
		},
		{
			name: "python traceback frames",
			original: `$ python -m svc.run --once
loading configuration
connecting to queue
Traceback (most recent call last):
  File "/app/svc/handler.py", line 88, in handle
    return self.parse(payload)
  File "/app/svc/parser.py", line 12, in parse
    raise ValueError("bad payload")
ValueError: bad payload
exit code 1`,
			rewritten: `$ python -m svc.run --once
Traceback: ValueError: bad payload
exit code 1`,
			want: reasonReference,
		},
		{
			name: "python syntax error frame",
			original: `$ python -c 'import app.conf.settings'
checking configuration module
  File "/app/conf/settings.py", line 31
    def load(:
             ^
SyntaxError: invalid syntax
exit code 1`,
			rewritten: `$ python -c 'import app.conf.settings'
SyntaxError: invalid syntax in the settings module
exit code 1`,
			want: reasonReference,
		},
		{
			name: "eslint bare line:col coordinates",
			original: `$ npx eslint src --format stylish
/app/src/components/Table.tsx
   8:1   warning  Unexpected console statement  no-console
  12:5   error    'rows' is assigned a value but never used  no-unused-vars
  40:11  warning  Missing return type on function  explicit-function-return-type

3 problems (1 error, 2 warnings)
exit code 1`,
			rewritten: `$ npx eslint src --format stylish
/app/src/components/Table.tsx
[3 lint messages omitted: 1 error, 2 warnings]
3 problems (1 error, 2 warnings)
exit code 1`,
			want: reasonReference,
		},
		{
			name: "npm ERR! block",
			original: `$ npm run build
> app@1.0.0 build
> tsc -p .
npm ERR! code ELIFECYCLE
npm ERR! app@1.0.0 build script returned a non-zero status
npm ERR! Failed at the app@1.0.0 build script.
npm ERR! A complete log of this run can be found above.
exit code 1`,
			rewritten: `$ npm run build
build script completed
exit code 1`,
			want: reasonFailureSignal,
		},
		{
			name: "command not found",
			original: `$ ./scripts/deploy.sh staging
resolving cluster credentials
selecting namespace staging
./scripts/deploy.sh: line 14: kubectl: command not found
exit code 127`,
			rewritten: `$ ./scripts/deploy.sh staging
deploy script finished for staging
exit code 127`,
			want: reasonFailureSignal,
		},
		{
			name: "integration timeout",
			original: `$ pytest tests/integration -k slow
collecting integration tests
tests/integration/test_sync.py::test_replica started
tests/integration/test_sync.py::test_replica timed out after 300 seconds
1 failed, 0 passed
exit code 1`,
			rewritten: `$ pytest tests/integration -k slow
integration suite finished
1 failed, 0 passed
exit code 1`,
			want: reasonFailureSignal,
		},
		{
			name: "segmentation fault",
			original: `$ ./bin/indexer --rebuild
opening index at /var/lib/index
scanning 12000 documents
flushing segment 4
Segmentation fault (core dumped)
exit code 139`,
			rewritten: `$ ./bin/indexer --rebuild
opening index and scanning documents
exit code 139`,
			want: reasonFailureSignal,
		},
		{
			name: "git merge conflict",
			original: `$ git merge origin/main
Auto-merging internal/api/router.go
Auto-merging internal/store/query.go
CONFLICT (content): Merge conflict in internal/store/query.go
Automatic merge failed; fix conflicts and then commit the result.
exit code 1`,
			rewritten: `$ git merge origin/main
merge of origin/main completed
exit code 1`,
			want: reasonFailureSignal,
		},
		{
			name: "java stack frames",
			original: `$ java -jar build/app.jar --once
starting worker pool
Exception in thread "main" java.lang.NullPointerException: Cannot invoke String.length() because s is null
	at com.example.app.Parser.parse(Parser.java:88)
	at com.example.app.Main.main(Main.java:21)
exit code 1`,
			rewritten: `$ java -jar build/app.jar --once
Exception in thread "main" java.lang.NullPointerException: Cannot invoke String.length() because s is null
[stack frames omitted]
exit code 1`,
			want: reasonReference,
		},
		{
			name: "tsc parenthesised locations",
			original: `$ npx tsc -p .
loading tsconfig
type checking 214 files
src/components/Table.tsx(12,5): error TS2304: Cannot find name rows.
src/lib/api.ts(88,3): error TS2345: Argument of type string is not assignable.
Found 2 errors in 2 files.
exit code 2`,
			rewritten: `$ npx tsc -p .
[2 type errors omitted: Cannot find name rows; argument type mismatch]
Found 2 errors in 2 files.
exit code 2`,
			want: reasonReference,
		},
		{
			name: "failure count destroyed",
			original: `$ go test ./...
--- FAIL: TestAlpha (0.00s)
--- FAIL: TestBeta (0.00s)
--- FAIL: TestGamma (0.00s)
--- FAIL: TestDelta (0.00s)
--- FAIL: TestEpsilon (0.00s)
FAIL	github.com/example/app/internal/store	0.31s
5 failed, 12 passed
exit status 1`,
			rewritten: `$ go test ./...
--- FAIL: TestAlpha (0.00s)
FAIL	github.com/example/app/internal/store	0.31s
1 failed, 12 passed
exit status 1`,
			want: reasonCount,
		},
		{
			name: "warning count destroyed",
			original: `$ make build
gcc -c src/parser.c -o build/parser.o
gcc -c src/lexer.c -o build/lexer.o
gcc -c src/emitter.c -o build/emitter.o
warning: unused variable tmp in parser
warning: comparison of integer expressions of different signedness in lexer
warning: implicit declaration of function strdup in emitter
3 warnings generated.
exit code 0`,
			rewritten: `$ make build
[compiler output omitted]
1 warning generated.
exit code 0`,
			want: reasonCount,
		},
	}

	if len(cases) != 15 {
		t.Fatalf("hostile corpus has %d rows, want 15", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if saving := countTokens([]byte(tc.original)) - countTokens([]byte(tc.rewritten)); saving <= 0 {
				t.Fatalf("fixture saves %d tokens; a hostile row must be smaller or the token gate answers for it", saving)
			}
			got := accept([]byte(tc.original), []byte(tc.rewritten), 0)
			if got != tc.want {
				t.Fatalf("accept() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The gate is only worth shipping if it still lets real waste through. These
// are the three block classes the mechanism exists for.
func TestAcceptStillFlipsRealWaste(t *testing.T) {
	cases := []struct {
		name      string
		original  string
		rewritten string
	}{
		{
			name:     "pytest passing bulk",
			original: pytestRun,
			rewritten: `$ python -m pytest tests/ -q
[individual test lines omitted; 214 PASSED]
tests/test_beta.py::test_edge FAILED
tests/test_beta.py:142: E   AssertionError: assert 'a' == 'b'
FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'
1 failed, 214 passed in 12.44s
exit code 1`,
		},
		{
			name: "__pycache__ listing",
			original: `$ find . -name '*.py' -o -name '__pycache__' -o -name '*.pyc'
./app/__init__.py
./app/__pycache__
./app/__pycache__/__init__.cpython-311.pyc
./app/models/__pycache__
./app/models/__pycache__/user.cpython-311.pyc
./app/models/user.py
./app/views/__pycache__
./app/views/__pycache__/home.cpython-311.pyc
./app/views/home.py`,
			rewritten: `$ find . -name '*.py' -o -name '__pycache__' -o -name '*.pyc'
./app/__init__.py
./app/models/user.py
./app/views/home.py
[6 __pycache__ and .pyc entries omitted]`,
		},
		{
			name:     "go vet ok lines",
			original: goVetRun,
			rewritten: `$ make vet
[3 ok package lines omitted]
internal/worker/loop.go:203:6: Errorf format %d has arg name of wrong type string
make: *** [Makefile:12: vet] Error 2
exit status 2`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := Accept([]byte(tc.original), []byte(tc.rewritten), 0)
			if !ok {
				t.Fatalf("Accept() rejected real waste with %q", reason)
			}
		})
	}
}

func TestExportedAcceptMirrorsInternal(t *testing.T) {
	ok, reason := Accept([]byte(pytestRun), []byte("nothing survived here"), 0)
	if ok || reason != reasonFailureSignal {
		t.Fatalf("Accept() = %v, %q; want false, %q", ok, reason, reasonFailureSignal)
	}
	ok, reason = Accept([]byte(pytestRun), []byte(`$ python -m pytest tests/ -q
[individual test lines omitted; 214 PASSED]
tests/test_beta.py::test_edge FAILED
tests/test_beta.py:142: E   AssertionError: assert 'a' == 'b'
FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'
1 failed, 214 passed in 12.44s
exit code 1`), 0)
	if !ok || reason != "" {
		t.Fatalf("Accept() = %v, %q; want true, \"\"", ok, reason)
	}
}

func TestNonZeroExitStatuses(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{name: "zero is not a failure", text: "exit code 0", want: nil},
		{name: "padded zero is not a failure", text: "exit status 00", want: nil},
		{name: "code and status both counted", text: "exit code 1\nexit status 2", want: []string{"exit code 1", "exit status 2"}},
		{name: "deduped case-insensitively", text: "Exit Code 1\nexit code 1", want: []string{"Exit Code 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStrings(t, "nonZeroExitStatuses", nonZeroExitStatuses(tc.text), tc.want)
		})
	}
}

// An exit code must match on word boundaries: "exit code 1" is a different
// failure from "exit code 12" and a substring check conflates them.
func TestExitCodeSurvivalIsWordBounded(t *testing.T) {
	original := `$ ./run.sh
starting the batch job
processing records
the job stopped early
exit code 1`
	decoy := `$ ./run.sh
the job stopped early
exit code 12`
	if got := accept([]byte(original), []byte(decoy), 0); got != reasonExitCode {
		t.Fatalf("accept() = %q, want %q — 'exit code 12' must not satisfy 'exit code 1'", got, reasonExitCode)
	}

	honest := `$ ./run.sh
the job stopped early
exit code 1`
	if got := accept([]byte(original), []byte(honest), 0); got != "" {
		t.Fatalf("accept() = %q, want accepted", got)
	}
}

// Counts match on word boundaries too, in both directions.
func TestCountSurvivalIsWordBounded(t *testing.T) {
	original := `$ go test ./...
running the store package suite
running the api package suite
running the worker package suite
5 failed, 12 passed
exit status 1`
	decoy := `$ go test ./...
ran three package suites
15 failed, 12 passed
exit status 1`
	if got := accept([]byte(original), []byte(decoy), 0); got != reasonCount {
		t.Fatalf("accept() = %q, want %q — '15 failed' must not satisfy '5 failed'", got, reasonCount)
	}
}

func TestAcceptPreservesEveryDistinctFailureDetail(t *testing.T) {
	original := `$ make verify
loading package alpha
loading package beta
loading package gamma
loading package delta
loading package epsilon
loading package zeta
loading package eta
loading package theta
error: alpha migration checksum mismatch
error: beta signing key is unavailable`
	rewritten := `$ make verify
[package loading omitted]
error: alpha migration checksum mismatch
[second error omitted]`
	if got := accept([]byte(original), []byte(rewritten), 0); got != reasonFailureDetail {
		t.Fatalf("accept() = %q, want %q when one distinct error disappears", got, reasonFailureDetail)
	}

	honest := `$ make verify
[package loading omitted]
error: alpha migration checksum mismatch
error: beta signing key is unavailable`
	if got := accept([]byte(original), []byte(honest), 0); got != "" {
		t.Fatalf("accept() = %q, want accepted when every distinct error survives", got)
	}

	prefixOriginal := original + "\nerror: beta signing key is unavailable during rotation"
	if got := accept([]byte(prefixOriginal), []byte(honest+" during rotation"), 0); got != reasonFailureDetail {
		t.Fatalf("accept() = %q, want %q when longer error only impersonates shorter error", got, reasonFailureDetail)
	}
}

func TestFailureCounts(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{name: "failed", text: "5 failed, 12 passed", want: []string{"5 failed"}},
		{name: "plural nouns", text: "3 problems (1 error, 2 warnings)", want: []string{"3 problems", "1 error", "2 warnings"}},
		{name: "word before number is not a tally", text: "Error 2", want: nil},
		{name: "passed is not a tally", text: "214 passed", want: nil},
		{name: "eslint column is not a tally", text: "   8:1   warning  Unexpected console", want: nil},
		{name: "a version line above an error is not a tally", text: "Compiling app v0.3.1\nerror[E0308]", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStrings(t, "failureCounts", failureCounts(tc.text), tc.want)
		})
	}
}

func TestSourceLocations(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "path with line and column",
			text: "src/main.rs:12:7: mismatched types",
			want: []string{"src/main.rs:12:7"},
		},
		{
			name: "extensionless location",
			text: "make: *** [Makefile:12: vet] Error 2",
			want: []string{"Makefile:12"},
		},
		{
			name: "tsc parenthesised location",
			text: "src/lib/api.ts(88,3): error TS2345: bad argument",
			want: []string{"src/lib/api.ts(88,3)"},
		},
		{
			name: "python frame",
			text: `  File "/app/svc/handler.py", line 88, in handle`,
			want: []string{`File "/app/svc/handler.py", line 88`},
		},
		{
			name: "clock time is not a source location",
			text: "2026-08-07 14:03:22 connection reset",
			want: nil,
		},
		{
			name: "bare path with no location is not unconditional",
			text: "ok  tests/test_alpha.py 0.01s",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStrings(t, "sourceLocations", sourceLocations(tc.text), tc.want)
		})
	}
}

func TestFailureReferences(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "bare path on a failure line",
			text: "FAILED ./tests/test_x.py::test_y",
			want: []string{"./tests/test_x.py"},
		},
		{
			name: "bare line:col on a failure line",
			text: "  12:5   error    'rows' is assigned a value but never used",
			want: []string{"12:5"},
		},
		{
			name: "references on non-failure lines are not protected",
			text: "ok  tests/test_alpha.py 0.01s",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertStrings(t, "failureReferences", failureReferences(tc.text), tc.want)
		})
	}
}

func TestFailureNeedlesCoverToolchainVocabulary(t *testing.T) {
	for _, line := range []string{
		"npm ERR! code ELIFECYCLE",
		"thread 'main' panicked at src/lib.rs:1:1",
		"kubectl: command not found",
		"open /etc/app.conf: no such file or directory",
		"request timed out after 30s",
		"Segmentation fault (core dumped)",
		"CONFLICT (content): Merge conflict",
		"permission denied",
		"connection refused",
		"Unable to locate package",
		"Cannot find name rows",
		"warning: unused variable",
		"Killed",
		"process aborted",
	} {
		if !isFailureLine(line) {
			t.Errorf("isFailureLine(%q) = false, want true", line)
		}
	}
	for _, line := range []string{
		"ok      github.com/example/app/internal/api",
		"tests/test_alpha.py::test_one PASSED",
		"./app/__pycache__/user.cpython-311.pyc",
	} {
		if isFailureLine(line) {
			t.Errorf("isFailureLine(%q) = true, want false", line)
		}
	}
}

func assertStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s() = %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s()[%d] = %q, want %q", name, i, got[i], want[i])
		}
	}
}
