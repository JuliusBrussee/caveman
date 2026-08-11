// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import "testing"

func TestDefaultTransformOptions(t *testing.T) {
	opts := DefaultTransformOptions("claude-fable-5")
	if opts.Model != "claude-fable-5" ||
		!opts.Compress ||
		!opts.CompressTools ||
		!opts.CompressReminders ||
		!opts.CompressToolResults ||
		opts.MinCompressChars != 2000 ||
		opts.MinReminderChars != 6000 ||
		opts.MinToolResultChars != 6000 ||
		opts.Cols != 312 ||
		opts.MaxImagesPerToolResult != 10 ||
		opts.MultiCol != 1 ||
		opts.CharsPerToken != 4 ||
		opts.HistoryAmortizationHorizon != 1 ||
		!opts.CollapseHistory ||
		!opts.Reflow ||
		opts.EmitRecoverable {
		t.Fatalf("defaults mismatch: %+v", opts)
	}
	if opts.KeepSharp == nil || opts.KeepSharp(KeepSharpBlock{Kind: "tool_result", Text: "x"}) {
		t.Fatalf("KeepSharp default mismatch")
	}
}
