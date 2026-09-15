// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestMinifyForRender(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"strip trailing spaces", "foo   \n", "foo\n"},
		{"strip trailing tab space mix", "foo\t \n", "foo\n"},
		{"collapse five newlines", "foo\n\n\n\n\nbar", "foo\n\n\nbar"},
		{"preserve one blank line", "foo\n\nbar", "foo\n\nbar"},
		{"preserve cap", "foo\n\n\nbar", "foo\n\n\nbar"},
		{"preserve midline spaces", "a   b   c", "a   b   c"},
		{"preserve leading whitespace", "    foo", "    foo"},
		{"real-world mix", "a  \n\n\n\n  b\t\nc", "a\n\n\n  b\nc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MinifyForRender(tc.in); got != tc.want {
				t.Fatalf("MinifyForRender() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReflowAndDereflow(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		"hello   \nworld\t ",
		"\n\n",
		"alpha\nbeta\n\ngamma",
		"中\t文\nemoji 😀 ok",
		"    indented line\n        double indented",
		"line1\r\nline2",
		"spaces only   ",
		"\t\t",
		"e\u0301 combining",
	}
	for _, text := range cases {
		t.Run(strings.ReplaceAll(text, "\n", "\\n"), func(t *testing.T) {
			packed, ok := Reflow(text)
			if !ok {
				t.Fatalf("Reflow(%q) ok=false", text)
			}
			if strings.Contains(packed, "\n") {
				t.Fatalf("packed contains newline: %q", packed)
			}
			expected := strings.Join(expandLinesForReference(MinifyForRender(text)), "\n")
			if got := Dereflow(packed); got != expected {
				t.Fatalf("Dereflow(Reflow()) = %q, want %q", got, expected)
			}
		})
	}

	for _, text := range []string{"a↵b", "↵start", "end↵", "↵"} {
		if _, ok := Reflow(text); ok {
			t.Fatalf("Reflow(%q) ok=true, want sentinel collision", text)
		}
	}
	if got := NeutralizeSentinel("a↵b"); got != "a⏎b" {
		t.Fatalf("NeutralizeSentinel = %q", got)
	}
}

func expandLinesForReference(text string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = ExpandTabsInLine(line)
	}
	return lines
}

func TestExpandTabsInLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a\tb", "a→  b"},
		{"\tx", "→   x"},
		{"ab\tc", "ab→ c"},
		{"abc\tx", "abc→x"},
		{"a\nb", "a\nb"},
		{"hello world", "hello world"},
		{"中\tx", "中→ x"},
	}
	for _, tc := range cases {
		if got := ExpandTabsInLine(tc.in); got != tc.want {
			t.Fatalf("ExpandTabsInLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMeasureAndWrap(t *testing.T) {
	if got := MeasureLineCols("abc中", 1); got != 5 {
		t.Fatalf("MeasureLineCols CJK = %d, want 5", got)
	}
	if got := MeasureContentCols("short\n"+strings.Repeat("x", 40), 20, 1); got != 20 {
		t.Fatalf("MeasureContentCols cap = %d, want 20", got)
	}
	lines := WrapLines(strings.Repeat("a", 5)+"中"+"b", 6, 1)
	if len(lines) != 2 || lines[0] != "aaaaa" || lines[1] != "中b" {
		t.Fatalf("WrapLines wide boundary = %#v", lines)
	}
	missing := WrapLines("ab😀cd", 3, 1)
	if len(missing) != 2 || missing[0] != "ab😀" || missing[1] != "cd" {
		t.Fatalf("WrapLines missing glyph stability = %#v", missing)
	}
}

func TestRenderChunkToPNG(t *testing.T) {
	img, err := RenderChunkToPNG("hello", 80, RenderStyle{}, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img.PNG[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Fatal("PNG signature mismatch")
	}
	if img.Width <= 0 || img.Height > MaxHeightPx {
		t.Fatalf("bad dimensions: %dx%d", img.Width, img.Height)
	}
	if img.CharsRendered != 5 {
		t.Fatalf("CharsRendered = %d, want 5", img.CharsRendered)
	}
	if img.DroppedChars != 0 {
		t.Fatalf("DroppedChars = %d, want 0", img.DroppedChars)
	}
}

func TestRenderSplitsLongInput(t *testing.T) {
	imgs, err := RenderTextToPNGs(strings.Repeat("a", ReadableCharsPerImage+100), 312, RenderStyle{})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) <= 1 {
		t.Fatalf("image count = %d, want > 1", len(imgs))
	}
	for _, img := range imgs {
		if img.Height > MaxHeightPx {
			t.Fatalf("height = %d, want <= %d", img.Height, MaxHeightPx)
		}
	}
}

func TestRenderUnicodeCoverageAndDrops(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantDrops int
	}{
		{"CJK", "中國", 0},
		{"Cyrillic", "Привет мир", 0},
		{"symbols", "✓ ✗ ⚠ ★ ℝ ℕ ℤ ℚ ℂ █ ░ ▒ ▲ ▼ ► ◄ ● ⌈ ⌉ ⌊ ⌋ ⓘ ① ② ⑩ 한글안녕", 0},
		{"emoji", "hi 😀 world", 1},
		{"repeat emoji", "😀😀😀", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := RenderChunkToPNG(tc.text, 40, RenderStyle{}, MaxHeightPx, "")
			if err != nil {
				t.Fatal(err)
			}
			if img.DroppedChars != tc.wantDrops {
				t.Fatalf("DroppedChars = %d, want %d", img.DroppedChars, tc.wantDrops)
			}
			if tc.wantDrops > 0 && img.DroppedCodepoints['😀'] != tc.wantDrops {
				t.Fatalf("emoji drop tally = %d, want %d", img.DroppedCodepoints['😀'], tc.wantDrops)
			}
		})
	}
}

func TestMultiColumnRendering(t *testing.T) {
	text := strings.Repeat("row data data data\n", 400)
	single, err := RenderTextToPNGs(text, 100, RenderStyle{})
	if err != nil {
		t.Fatal(err)
	}
	passthrough, err := RenderTextToPNGsMultiCol(text, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != len(passthrough) {
		t.Fatalf("single path count = %d, numCols=1 count = %d", len(single), len(passthrough))
	}
	for i := range single {
		if !bytes.Equal(single[i].PNG, passthrough[i].PNG) {
			t.Fatalf("numCols=1 PNG %d differs", i)
		}
	}
	two, err := RenderTextToPNGsMultiCol(text, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if two[0].Width != MultiColWidth(100, 2) {
		t.Fatalf("width = %d, want %d", two[0].Width, MultiColWidth(100, 2))
	}
	if two[0].Width <= single[0].Width || two[0].Width > MaxWidthPx {
		t.Fatalf("bad multicol width: got %d single %d", two[0].Width, single[0].Width)
	}
	if MaxFittingCols(100) != 3 {
		t.Fatalf("MaxFittingCols(100) = %d, want 3", MaxFittingCols(100))
	}
	clamped, err := RenderTextToPNGsMultiCol(text, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range clamped {
		if img.Width > MaxWidthPx {
			t.Fatalf("clamped image width = %d, want <= %d", img.Width, MaxWidthPx)
		}
	}
	total := 0
	for _, img := range two {
		total += img.CharsRendered
	}
	if total != runeLen(text) {
		t.Fatalf("charsRendered total = %d, want %d", total, runeLen(text))
	}
}

func TestMultiColumnGutterDivider(t *testing.T) {
	imgs, err := RenderTextToPNGsMultiCol(strings.Repeat("abc\n", 200), 80, 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(imgs[0].PNG))
	if err != nil {
		t.Fatal(err)
	}
	dividerX := PadX + 80*CellW + (GutterCells*CellW)/2
	midGrayRows := 0
	liveRows := 0
	for y := 0; y < got.Bounds().Dy(); y++ {
		r, g, b, _ := got.At(dividerX, y).RGBA()
		v := uint8(r >> 8)
		if r == g && g == b && v == 191 {
			midGrayRows++
		}
		if v != 255 {
			liveRows++
		}
	}
	if liveRows == 0 || float64(midGrayRows) <= float64(liveRows)*0.8 {
		t.Fatalf("divider rows mid-gray=%d live=%d", midGrayRows, liveRows)
	}
}

func TestDensePages(t *testing.T) {
	imgs, err := RenderDensePages("a\nb\nc", DensePagesOptions{Reflow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 {
		t.Fatalf("dense page count = %d, want 1", len(imgs))
	}
	if imgs[0].DroppedChars != 0 {
		t.Fatalf("DroppedChars = %d, want 0", imgs[0].DroppedChars)
	}
	if imgs[0].Width > MaxWidthPx {
		t.Fatalf("dense width = %d, want <= %d", imgs[0].Width, MaxWidthPx)
	}
}

func TestPageGeometryPatchAligned(t *testing.T) {
	const patch = AnthropicPatchPx // 28

	// The framing constants must themselves be whole patches.
	if MaxHeightPx%patch != 0 {
		t.Fatalf("MaxHeightPx %d is not a multiple of %d", MaxHeightPx, patch)
	}
	if MaxWidthPx%patch != 0 {
		t.Fatalf("MaxWidthPx %d is not a multiple of %d", MaxWidthPx, patch)
	}

	// The standard page width (default render cols) must be exactly MaxWidthPx,
	// a whole number of patches, and fit inside the standard tier's long edge —
	// so no column of padding forces a 57th patch or a server-side downscale.
	stdWidth := 2*PadX + DefaultCols*CellW
	if stdWidth != MaxWidthPx || stdWidth%patch != 0 || stdWidth > StandardPixelTier.MaxPx {
		t.Fatalf("standard width = %d, want %d (patch-aligned, <= tier %d)", stdWidth, MaxWidthPx, StandardPixelTier.MaxPx)
	}

	// The transform-layer default must use that same patch-aligned width.
	if got := DefaultTransformOptions("").Cols; got != DefaultCols {
		t.Fatalf("default transform Cols = %d, want %d (patch-aligned)", got, DefaultCols)
	}

	// The per-page char budget at the default width must permit a full,
	// height-capped page of 90 patch rows. A non-divisor width (e.g. 313, where
	// 28080/313 = 89) would cap pages at 89 lines -> 720px, paying for a 26th
	// patch row it never fills.
	if ReadableCharsPerImage%DefaultCols != 0 {
		t.Fatalf("ReadableCharsPerImage %d not divisible by DefaultCols %d: page height would misalign", ReadableCharsPerImage, DefaultCols)
	}
	hardLines := (MaxHeightPx - 2*PadY) / CellH
	if budgetLines := ReadableCharsPerImage / DefaultCols; budgetLines < hardLines {
		t.Fatalf("char budget caps pages at %d lines < height cap %d: wasted patch row", budgetLines, hardLines)
	}

	// A full rendered page must land on whole patches on both axes.
	img, err := RenderChunkToPNG(strings.Repeat("x\n", hardLines+20), DefaultCols, DenseRenderStyle, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != MaxWidthPx || img.Height != MaxHeightPx {
		t.Fatalf("full page = %dx%d, want %dx%d", img.Width, img.Height, MaxWidthPx, MaxHeightPx)
	}
	if img.Width%patch != 0 || img.Height%patch != 0 {
		t.Fatalf("full page %dx%d not patch-aligned (%d)", img.Width, img.Height, patch)
	}
}

func TestRoleSlots(t *testing.T) {
	body := "hello body text"
	seg := RoleSlotSegment("user", body, SlotMarkUser, ` t="37"`)
	text := `<user t="37">` + "\n" + body + "\n" + `</user>`
	if runeLen(seg) != runeLen(text) {
		t.Fatalf("slot length = %d, want %d", runeLen(seg), runeLen(text))
	}
	segRunes := []rune(seg)
	textRunes := []rune(text)
	for i := range segRunes {
		if (segRunes[i] == '\n') != (textRunes[i] == '\n') {
			t.Fatalf("newline alignment drift at %d", i)
		}
	}
	open := runeLen(`<user t="37">`)
	closeLen := runeLen(`</user>`)
	for _, r := range segRunes[:open] {
		if slotForMark(r) != 1 {
			t.Fatal("open tag not user-slotted")
		}
	}
	for _, r := range segRunes[len(segRunes)-closeLen:] {
		if slotForMark(r) != 1 {
			t.Fatal("close tag not user-slotted")
		}
	}
	bodyCopy := SlotCopyBody("x" + SlotMarkUser + "y" + SlotMarkAssistant)
	if runeLen(bodyCopy) != 4 {
		t.Fatalf("neutralized length = %d, want 4", runeLen(bodyCopy))
	}
	for _, r := range bodyCopy {
		if slotForMark(r) != 0 {
			t.Fatalf("body forged role slot for %q", r)
		}
	}
}
