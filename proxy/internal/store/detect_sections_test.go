package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSectionCorpusCapsSessionsAndBytes(t *testing.T) {
	beh := behaviorScan{}
	for i := 0; i < maxSectionSessions+8; i++ {
		local := behaviorScan{}
		consumer := newSessionEventConsumer("claude", sessionRef{relPath: "session"}, nil, &local, newRecurringMiner())
		consumer.consume(turnEvent{sessionStart: true})
		consumer.consume(turnEvent{TextPayloads: []string{strings.Repeat("x", maxSectionTextBytes+1024)}})
		consumer.finish()
		mergeBehaviorScan(&beh, &local)
	}
	if len(beh.SessionTexts) != maxSectionSessions {
		t.Fatalf("session corpus = %d, want cap %d", len(beh.SessionTexts), maxSectionSessions)
	}
	for _, session := range beh.SessionTexts {
		if len(session.Text) > maxSectionTextBytes {
			t.Fatalf("session text bytes = %d, want <= %d", len(session.Text), maxSectionTextBytes)
		}
	}
}

func TestClaudeMDSectionEchoPositiveExcludesEchoedSection(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "CLAUDE.md")
	line := "Preserve byte exact request ordering whenever provider cache state changes between turns."
	if err := os.WriteFile(path, []byte("## Cache safety\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := make([]sessionTextObservation, 5)
	for i := range sessions {
		sessions[i] = sessionTextObservation{Repo: repo, Text: normalizeEchoText("unrelated session text")}
	}
	sessions[2].Text = normalizeEchoText("Agent followed this rule: " + line)
	if sink, ok := claudeMDSectionSink(&ConfigSnapshot{Path: path}, "project", sessions); ok {
		t.Fatalf("echoed-only file emitted section sink: %+v", sink)
	}
}

func TestClaudeMDSectionEchoZeroCapabilityEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	line := "Run the repository boundary verifier before changing any public provider adapter surface."
	raw := "Preamble short.\n## Verification\n" + line + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := make([]sessionTextObservation, 5)
	for i := range sessions {
		sessions[i] = sessionTextObservation{Text: normalizeEchoText("session without distinctive rule echo")}
	}
	if sink, ok := claudeMDSectionSink(&ConfigSnapshot{Path: path}, "user", sessions); ok {
		t.Fatalf("zero-echo corpus emitted unsupported sink: %+v", sink)
	}
}

func TestClaudeMDSectionEchoPositiveControlEmitsOnlyUnechoedSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	echoed := "Preserve byte exact request ordering whenever provider cache state changes between turns."
	unechoed := "Run the repository boundary verifier before changing any public provider adapter surface."
	if err := os.WriteFile(path, []byte("## Cache safety\n"+echoed+"\n## Verification\n"+unechoed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions := make([]sessionTextObservation, 5)
	for i := range sessions {
		sessions[i] = sessionTextObservation{Text: normalizeEchoText("unrelated visible transcript text")}
	}
	sessions[2].Text = normalizeEchoText("Agent echoed: " + echoed)
	sink, ok := claudeMDSectionSink(&ConfigSnapshot{Path: path}, "user", sessions)
	if !ok {
		t.Fatal("positive-control corpus did not emit unechoed candidate")
	}
	sections, typed := sink.Evidence["sections"].([]claudeMDSectionEvidence)
	if !typed || len(sections) != 1 || sections[0].Heading != "Verification" || sections[0].Tokens <= 0 {
		t.Fatalf("section evidence = %#v", sink.Evidence["sections"])
	}
	if sink.Evidence["sessions_checked"] != 5 || sink.Evidence["sections_echoed"] != 1 || sink.Evidence["method"] != "distinctive_line_echo" {
		t.Fatalf("section method evidence = %+v", sink.Evidence)
	}
	if sink.Evidence["session_text_cap"] != maxSectionSessions || sink.Evidence["session_byte_cap"] != maxSectionTextBytes {
		t.Fatalf("section corpus caps missing: %+v", sink.Evidence)
	}
	if sink.TokensPerTurn != int64(sections[0].Tokens) {
		t.Fatalf("tokens_per_turn = %d, section tokens = %d", sink.TokensPerTurn, sections[0].Tokens)
	}
}
