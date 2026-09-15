package contextwindow_test

import (
	"slices"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/contextwindow"
)

func TestPackSelectsBM25RelevantItemsWithinBudget(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	items := []contextwindow.Item{
		{ID: "intro", Text: "general project overview and team notes", Tokens: 20, Timestamp: now.Add(-4 * time.Hour)},
		{ID: "deploy", Text: "deploy failed because the migration lock timed out with ERROR in postgres", Tokens: 30, Timestamp: now.Add(-3 * time.Hour)},
		{ID: "billing", Text: "billing dashboard color adjustments and copy polish", Tokens: 25, Timestamp: now.Add(-2 * time.Hour)},
		{ID: "logs", Text: "postgres migration retry succeeded after lock release", Tokens: 30, Timestamp: now.Add(-time.Hour)},
	}

	got := contextwindow.Pack("postgres migration deploy failure", items, contextwindow.Options{MaxTokens: 60, Now: now})
	ids := selectedIDs(got.Items)
	if !slices.Contains(ids, "deploy") || !slices.Contains(ids, "logs") {
		t.Fatalf("selected ids = %v, want deploy and logs", ids)
	}
	if slices.Contains(ids, "billing") {
		t.Fatalf("selected ids = %v, unrelated billing item should be deferred", ids)
	}
	if got.TokensUsed > 60 {
		t.Fatalf("tokens used = %d, exceeds budget", got.TokensUsed)
	}
	if got.DeferredCount != 2 || got.TokensSaved <= 0 {
		t.Fatalf("bad accounting: %+v", got)
	}
}

func TestPackKeepsPinnedItemsWhenTheyFit(t *testing.T) {
	items := []contextwindow.Item{
		{ID: "system", Text: "system instruction keep this exact rule", Tokens: 25, Pin: true},
		{ID: "match", Text: "rare alpha beta gamma query terms", Tokens: 30},
		{ID: "extra", Text: "rare alpha beta gamma more query terms", Tokens: 30},
	}

	got := contextwindow.Pack("rare alpha beta gamma", items, contextwindow.Options{MaxTokens: 55})
	ids := selectedIDs(got.Items)
	if ids[0] != "system" {
		t.Fatalf("selected ids = %v, pinned system item must remain in original order", ids)
	}
	if len(ids) != 2 || got.TokensUsed != 55 {
		t.Fatalf("selected ids/tokens = %v/%d, want exactly pinned plus one match", ids, got.TokensUsed)
	}
}

func TestPackUsesRecencyWhenQueryIsEmpty(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	items := []contextwindow.Item{
		{ID: "old", Text: "old context", Tokens: 20, Timestamp: now.Add(-24 * time.Hour)},
		{ID: "recent", Text: "recent context", Tokens: 20, Timestamp: now.Add(-10 * time.Minute)},
	}

	got := contextwindow.Pack("", items, contextwindow.Options{MaxTokens: 20, Now: now})
	ids := selectedIDs(got.Items)
	if len(ids) != 1 || ids[0] != "recent" {
		t.Fatalf("selected ids = %v, want recent", ids)
	}
}

func TestPackReservesTokens(t *testing.T) {
	items := []contextwindow.Item{
		{ID: "a", Text: "alpha one", Tokens: 20},
		{ID: "b", Text: "alpha two", Tokens: 20},
	}

	got := contextwindow.Pack("alpha", items, contextwindow.Options{MaxTokens: 40, ReserveTokens: 25})
	if got.TokensUsed != 0 || got.DeferredCount != 2 {
		t.Fatalf("reserved budget should defer all items, got %+v", got)
	}
}

func selectedIDs(items []contextwindow.Selected) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}
