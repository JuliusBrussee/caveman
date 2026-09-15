// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"fmt"
	"strings"
	"testing"
)

func TestFactSheetExtraction(t *testing.T) {
	text := strings.Join([]string{
		"Edited src/lib/__tests__/livekit-egress.test.ts and agents/transcription/agent.ts",
		"opened https://github.com/Keplogic/atlas/pull/93 at commit 6d80bd6",
		"set LIVEKIT_API_SECRET and ran with --max-tokens 64000, coverage 97.82",
	}, "\n")
	toks := ExtractFactSheetTokens(text)
	for _, want := range []string{"src/lib/__tests__/livekit-egress.test.ts", "https://github.com/Keplogic/atlas/pull/93", "6d80bd6", "LIVEKIT_API_SECRET", "--max-tokens", "97.82"} {
		if !containsString(toks, want) {
			t.Fatalf("tokens missing %q: %#v", want, toks)
		}
	}
	if containsString(ExtractFactSheetTokens("this decade the facade was added"), "decade") {
		t.Fatal("pure-letter hex word extracted")
	}
	sub := ExtractFactSheetTokens("see https://github.com/o/r/pull/9 in repo")
	if !containsString(sub, "https://github.com/o/r/pull/9") || containsString(sub, "/github.com") {
		t.Fatalf("substring collapse failed: %#v", sub)
	}
	if FactSheetText("the quick brown fox jumps over", 0) != "" {
		t.Fatal("plain prose emitted factsheet")
	}
}

func TestFactSheetBudgetAndPriority(t *testing.T) {
	many := make([]string, 200)
	for i := range many {
		many[i] = fmt.Sprintf("/dir%d/file%d.ts", i, i)
	}
	if got := len(ExtractFactSheetTokens(strings.Join(many, " "))); got > 64 {
		t.Fatalf("token budget = %d, want <=64", got)
	}
	urls := make([]string, 80)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://platform.claude.com/docs/en/build-with-claude/page-%02d-guide.md", i)
	}
	text := strings.Join(append(append(urls[:40], "fix in commit 9d121ac on port 47821"), urls[40:]...), "\n")
	toks := ExtractFactSheetTokens(text)
	if !containsString(toks, "9d121ac") || !containsString(toks, "47821") {
		t.Fatalf("priority tokens missing: %#v", toks)
	}
	urlCount := 0
	for _, tok := range toks {
		if strings.HasPrefix(tok, "http") {
			urlCount++
		}
	}
	if urlCount > 8 {
		t.Fatalf("URL count = %d, want <=8", urlCount)
	}
}

func TestFactSheetTicketsAndCounts(t *testing.T) {
	toks := ExtractFactSheetTokens("audit marker AUDIT-ZX9 tracked as PROJ-1482, see CVE-2024-30078 for details")
	for _, want := range []string{"AUDIT-ZX9", "PROJ-1482", "CVE-2024-30078"} {
		if !containsString(toks, want) {
			t.Fatalf("ticket token %q missing from %#v", want, toks)
		}
	}
	prose := ExtractFactSheetTokens("column is READ-ONLY and NON-NULL by default")
	if containsString(prose, "READ-ONLY") || containsString(prose, "NON-NULL") {
		t.Fatalf("digit-free prose flagged: %#v", prose)
	}
	sheet := FactSheetText("retry DEPLOY-77 failed\nretry DEPLOY-77 ok\nfinal DEPLOY-77 done\nsha 9d121ac", 0)
	if !strings.Contains(sheet, "DEPLOY-77 ×3") || !strings.Contains(sheet, "×N marks a token that occurs N times") || strings.Contains(sheet, "9d121ac ×") {
		t.Fatalf("repeat annotation mismatch: %s", sheet)
	}
	if strings.Contains(FactSheetText("release v1.2.3 shipped", 0), "×") {
		t.Fatal("double-counted one span")
	}
}

func TestFactSheetAllPagesCounts(t *testing.T) {
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026-07-26T09:40:41Z WARN svc=ingest req=%x shard=12 msg=processed batch %d ok", 0x10000000+i*7919, 10000+i)
	}
	lines[137] += " AUDIT-ZX9"
	lines[201] += " AUDIT-ZX9"
	entries := ExtractFactSheetEntries(strings.Join(lines, "\n"))
	if hit := findEntry(entries, "AUDIT-ZX9"); hit == nil || hit.Count != 2 {
		t.Fatalf("AUDIT-ZX9 count mismatch: %#v", hit)
	}
	page := strings.Repeat("x", 90) + " TICK-42 "
	kept, _ := ExtractFactSheetEntriesAllPages(strings.Repeat(page, 5), 100)
	if hit := findEntry(kept, "TICK-42"); hit == nil || hit.Count != 5 {
		t.Fatalf("TICK-42 all-pages count mismatch: %#v", hit)
	}
}

func containsString(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

func findEntry(entries []FactSheetEntry, token string) *FactSheetEntry {
	for i := range entries {
		if entries[i].Token == token {
			return &entries[i]
		}
	}
	return nil
}
