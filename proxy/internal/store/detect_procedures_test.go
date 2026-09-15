package store

import (
	"strings"
	"testing"
)

func procedureCalls(steps [][2]string, tokens int) []learnToolCall {
	calls := make([]learnToolCall, 0, len(steps))
	for _, step := range steps {
		calls = append(calls, learnToolCall{Name: step[0], Input: step[1], OutputTokens: tokens})
	}
	return calls
}

var loopSteps = [][2]string{
	{"Read", "/repo/conftest.py"},
	{"Bash", "pytest -k failing"},
	{"Edit", "/repo/src/thing.py"},
	{"Bash", "pytest -k failing"},
}

// TestProcedureMiningFindsRepeatedSequences proves the core: a sequence the
// user repeats across sessions surfaces as a distillation candidate carrying
// the measured cost of re-deriving it.
func TestProcedureMiningFindsRepeatedSequences(t *testing.T) {
	miner := newProcedureMiner()
	for _, session := range []string{"s1", "s2", "s3", "s4"} {
		miner.observeSession(session, procedureCalls(loopSteps, 6_000))
	}
	sinks := procedureSinks(miner, &LearnSpend{EffectiveInputUSDPerMTok: 3.0})
	if len(sinks) == 0 {
		t.Fatal("a sequence repeated in four sessions must surface")
	}
	sink := sinks[0]
	if !strings.HasPrefix(sink.SinkID, "procedure_repeat:") {
		t.Fatalf("unexpected sink id %q", sink.SinkID)
	}
	if sink.Evidence["fix_kind"] != "skill_distillation" {
		t.Fatalf("candidate must route to distillation: %+v", sink.Evidence)
	}
	if sessions, _ := sink.Evidence["sessions"].(int); sessions != 4 {
		t.Fatalf("sessions = %v, want 4", sink.Evidence["sessions"])
	}
	steps, _ := sink.Evidence["steps"].([]string)
	if len(steps) < procedureMinLength {
		t.Fatalf("steps must carry the sequence: %+v", steps)
	}
	// Signatures only: no path, no command line, no prompt text.
	joined := strings.Join(steps, " ")
	if strings.Contains(joined, "/repo/") || strings.Contains(joined, "-k failing") {
		t.Fatalf("mined steps leaked raw input: %q", joined)
	}
	if _, priced := sink.Evidence["spend_usd_observed"]; !priced {
		t.Fatalf("a priced window must price the candidate: %+v", sink.Evidence)
	}

	// The grading rule is the whole point: this must NOT be handed to the
	// net-token-negative gate.
	grading, _ := sink.Evidence["grading"].(string)
	if !strings.Contains(grading, "holdout only") {
		t.Fatalf("candidate must state holdout-only grading: %q", grading)
	}
	if !strings.Contains(sink.Suggestion, "experiment") {
		t.Fatalf("suggestion must route to the harness: %q", sink.Suggestion)
	}
	if sink.Class != classBehavioral {
		t.Fatalf("a fix that ADDS prefix tokens must not be classed reducible: %q", sink.Class)
	}
}

// TestProcedureMiningIgnoresOneOffs keeps a sequence that happened once out of
// the report, and refuses a "procedure" that is one call repeated.
func TestProcedureMiningIgnoresOneOffs(t *testing.T) {
	once := newProcedureMiner()
	once.observeSession("s1", procedureCalls(loopSteps, 50_000))
	if got := procedureSinks(once, nil); len(got) != 0 {
		t.Fatalf("a single session must not produce a candidate: %+v", got)
	}

	flat := newProcedureMiner()
	same := [][2]string{{"Read", "/a/x.go"}, {"Read", "/a/y.go"}, {"Read", "/a/z.go"}, {"Read", "/a/w.go"}}
	for _, session := range []string{"s1", "s2", "s3", "s4"} {
		flat.observeSession(session, procedureCalls(same, 20_000))
	}
	if got := procedureSinks(flat, nil); len(got) != 0 {
		t.Fatalf("one call shape repeated is not a procedure: %+v", got)
	}

	cheap := newProcedureMiner()
	for _, session := range []string{"s1", "s2", "s3"} {
		cheap.observeSession(session, procedureCalls(loopSteps, 10))
	}
	if got := procedureSinks(cheap, nil); len(got) != 0 {
		t.Fatalf("a trivially cheap procedure is not worth distilling: %+v", got)
	}
}

// TestProcedureCandidatesDoNotTeachTheSameThingTwice pins the overlap filter:
// a short candidate contained in a longer kept one is dropped.
func TestProcedureCandidatesDoNotTeachTheSameThingTwice(t *testing.T) {
	miner := newProcedureMiner()
	for _, session := range []string{"s1", "s2", "s3", "s4", "s5"} {
		miner.observeSession(session, procedureCalls(loopSteps, 8_000))
	}
	sinks := procedureSinks(miner, nil)
	if len(sinks) == 0 {
		t.Fatal("expected at least one candidate")
	}
	var bodies []string
	for _, sink := range sinks {
		steps, _ := sink.Evidence["steps"].([]string)
		bodies = append(bodies, strings.Join(steps, "\x00"))
	}
	for i := range bodies {
		for j := range bodies {
			if i == j {
				continue
			}
			if strings.Contains(bodies[i], bodies[j]) {
				t.Fatalf("candidate %d contains candidate %d; overlap filter failed:\n%q\n%q", i, j, bodies[i], bodies[j])
			}
		}
	}
}

// TestProcedureMiningDoesNotInflateOnTightLoops proves one session's repeated
// span is booked once per procedure, so a loop cannot manufacture value.
func TestProcedureMiningDoesNotInflateOnTightLoops(t *testing.T) {
	repeated := append(append([]([2]string){}, loopSteps...), loopSteps...)
	looped := newProcedureMiner()
	single := newProcedureMiner()
	for _, session := range []string{"s1", "s2", "s3"} {
		looped.observeSession(session, procedureCalls(repeated, 6_000))
		single.observeSession(session, procedureCalls(loopSteps, 6_000))
	}
	loopedSinks := procedureSinks(looped, nil)
	singleSinks := procedureSinks(single, nil)
	if len(loopedSinks) == 0 || len(singleSinks) == 0 {
		t.Fatal("both must produce candidates")
	}
	if loopedSinks[0].TokensObserved > singleSinks[0].TokensObserved*2 {
		t.Fatalf("a tight loop inflated the candidate: looped=%d single=%d",
			loopedSinks[0].TokensObserved, singleSinks[0].TokensObserved)
	}
}
