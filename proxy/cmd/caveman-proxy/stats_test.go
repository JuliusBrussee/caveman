package main

import "testing"

func TestStatsReportOptions(t *testing.T) {
	opts, write, out, err := parseStatsReportOptions([]string{"--report", "--days", "0", "--provider", "openai", "--model", "gpt-5.6", "--auth", "subscription", "--out", "stats.html"})
	if err != nil || opts.Days != 0 || opts.Provider != "openai" || opts.Model != "gpt-5.6" || opts.AuthMode != "subscription" || !write || out != "stats.html" {
		t.Fatalf("options=%+v write=%v out=%q err=%v", opts, write, out, err)
	}
	for _, args := range [][]string{
		{"--report", "--days", "-1"}, {"--report", "--days", "3661"},
		{"--report", "--provider"}, {"--report", "--model", "--json"},
		{"--report", "--days", "7", "--days", "8"}, {"--report", "--reset"},
	} {
		if _, _, _, err := parseStatsReportOptions(args); err == nil {
			t.Fatalf("accepted invalid options %v", args)
		}
	}
}
