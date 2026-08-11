// Command caveman-engine is the standalone compression-engine binary that the
// CLI and other tools shell out to. Token figures are local estimates.
//
// Subcommands:
//
//	caveman-engine compress        compress stdin → stdout; JSON report on stderr
//	caveman-engine detect          print the detected content type of stdin
//	caveman-engine retrieve <h> [q] print the original bytes for a CCR handle; with
//	                               a query, only the most relevant sections (BM25)
//	caveman-engine stats           print aggregate compression stats as JSON
//	caveman-engine registry        print transform capability registry JSON
//	caveman-engine toon encode     JSON stdin → lossless TOON stdout (stateless)
//	caveman-engine toon decode     TOON stdin → JSON stdout (stateless)
//	caveman-engine pixel render    text file → <file>.pxN.png pages + JSONL metrics
//	caveman-engine pixel simulate  request JSON → pixel TransformInfo JSON
//	caveman-engine evals run [--fixtures DIR]
//	                               run embedded or caller-supplied eval fixtures
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/evals"
	"github.com/JuliusBrussee/caveman/engine/pixel"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const maxStdinBytes int64 = 64 << 20

func main() {
	args := os.Args[1:]
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "compress":
		runCompress(args[1:])
	case "detect":
		runDetect()
	case "retrieve":
		runRetrieve(args[1:])
	case "stats":
		runStats()
	case "registry":
		runRegistry()
	case "toon":
		runTOON(args[1:])
	case "pixel":
		runPixel(args[1:])
	case "evals":
		runEvals(args[1:])
	case "help", "--help", "-h":
		fmt.Fprintln(os.Stderr, "caveman-engine compress | detect | retrieve <handle> | stats | registry | toon encode|decode | pixel render|simulate | evals run [--fixtures DIR]")
	default:
		fmt.Fprintf(os.Stderr, "unknown caveman-engine subcommand: %s\n", cmd)
		os.Exit(2)
	}
}

// openEngine opens the on-disk recovery store under ~/.caveman/ and builds the
// engine with the default offline counter.
func openEngine() (*engine.Engine, func()) {
	store, err := ccr.Open(ccrPath())
	if err != nil {
		fatal("cannot open recovery store: %v", err)
	}
	return engine.New(store, nil), func() { _ = store.Close() }
}

func runCompress(args []string) {
	forcedType := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--type":
			if i+1 >= len(args) {
				fatal("usage: caveman-engine compress [--type <content-type>]")
			}
			forcedType = args[i+1]
			if forcedType == "auto" {
				forcedType = ""
			}
			i++
		default:
			fatal("unknown compress flag: %s", args[i])
		}
	}
	input, err := readBoundedInput(os.Stdin, maxStdinBytes)
	if err != nil {
		fatal("read stdin: %v", err)
	}
	eng, closeFn := openEngine()
	defer closeFn()
	res, err := eng.Compress(input, engine.Options{Mode: engine.ModeCompress, Type: forcedType})
	if err := emitCompressResult(input, res, err, os.Stdout, os.Stderr); err != nil {
		fatal("compress: %v", err)
	}
}

type compressReport struct {
	engine.Result
	Error string `json:"error,omitempty"`
}

// emitCompressResult preserves the engine's byte-safety contract at the CLI
// boundary. CCR failures are expected operational conditions: the engine has
// already returned an accounted pass-through result, so stdout must still get
// the original bytes. Any contradictory result fails rather than guessing.
func emitCompressResult(input []byte, res engine.Result, compressErr error, stdout, stderr io.Writer) error {
	code := ""
	if compressErr != nil {
		if !bytes.Equal(res.Output, input) || !res.PassedThrough() || res.TokensAfter != res.TokensBefore {
			return fmt.Errorf("unsafe recovery failure result: %w", compressErr)
		}
		code = "cave_ccr_unavailable"
		if errors.Is(compressErr, ccr.ErrBudgetExceeded) {
			code = "cave_ccr_budget_exceeded"
		}
	}
	if _, err := stdout.Write(res.Output); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	// Report stays on stderr so stdout remains clean payload bytes.
	report, err := json.Marshal(compressReport{Result: res, Error: code})
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if _, err := fmt.Fprintln(stderr, string(report)); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func runDetect() {
	input, err := readBoundedInput(os.Stdin, maxStdinBytes)
	if err != nil {
		fatal("read stdin: %v", err)
	}
	// Detection needs no store.
	eng := engine.New(nil, nil)
	fmt.Println(eng.Detect(input))
}

func runRetrieve(args []string) {
	if len(args) < 1 {
		fatal("usage: caveman-engine retrieve <handle> [query]")
	}
	eng, closeFn := openEngine()
	defer closeFn()
	// A second arg narrows the recovery to the sections most relevant to the query
	// (the same BM25 path the MCP caveman_retrieve tool uses); no query returns the
	// byte-exact original.
	query := ""
	if len(args) >= 2 {
		query = args[1]
	}
	original, err := eng.RetrieveQuery(args[0], query)
	if err != nil {
		fatal("retrieve: %v", err)
	}
	if _, err := os.Stdout.Write(original); err != nil {
		fatal("write stdout: %v", err)
	}
}

func runStats() {
	eng, closeFn := openEngine()
	defer closeFn()
	stats, err := eng.Stats()
	if err != nil {
		fatal("stats: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(stats); err != nil {
		fatal("stats output: %v", err)
	}
}

func runRegistry() {
	manifest, err := compressors.Default().CapabilityManifest()
	if err != nil {
		fatal("registry: %v", err)
	}
	if _, err := os.Stdout.Write(append(manifest, '\n')); err != nil {
		fatal("write stdout: %v", err)
	}
}

// runTOON handles `caveman-engine toon encode|decode` — a stateless, CCR-free
// JSON⇄TOON converter. It exists so an agent can emit the compact TOON form (and
// have downstream recover JSON) or read a TOON rendering of large JSON, WITHOUT
// ever writing or reading the verbose JSON in its own context. The conversion
// output must not be fed back into the agent that produced the other form — that
// would double the tokens the agent sees and defeat the point.
func runTOON(args []string) {
	if len(args) < 1 {
		fatal("usage: caveman-engine toon encode|decode")
	}
	input, err := readBoundedInput(os.Stdin, maxStdinBytes)
	if err != nil {
		fatal("read stdin: %v", err)
	}
	var out []byte
	switch args[0] {
	case "encode":
		out, err = toonEncodeBytes(input)
	case "decode":
		out, err = toonDecodeBytes(input)
	default:
		fatal("usage: caveman-engine toon encode|decode")
	}
	if err != nil {
		fatal("toon %s: %v", args[0], err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fatal("write stdout: %v", err)
	}
}

func readBoundedInput(r io.Reader, maxBytes int64) ([]byte, error) {
	input, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(input)) > maxBytes {
		return nil, fmt.Errorf("cave_input_too_large: stdin exceeds %d bytes", maxBytes)
	}
	return input, nil
}

func runPixel(args []string) {
	if len(args) < 1 {
		pixelUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "render":
		runPixelRender(args[1:])
	case "simulate":
		runPixelSimulate(args[1:])
	case "help", "--help", "-h":
		pixelUsage()
	default:
		fatal("usage: caveman-engine pixel render|simulate")
	}
}

func pixelUsage() {
	fmt.Fprintln(os.Stderr, "usage: caveman-engine pixel render [--cols N] [--multicol N] [--density conservative|balanced|max] [--dense] [--no-reflow] <file>")
	fmt.Fprintln(os.Stderr, "  render reflows by default to match pxpipe export; --no-reflow preserves hard line layout.")
	fmt.Fprintln(os.Stderr, "  --density picks the pack geometry (default balanced; falsey CAVE_PIXEL_DENSITY → conservative): conservative uses the standard mono grid; balanced uses a denser zebra grid; max adds a 2-layer red/blue overlay. An invalid --density is a usage error; the flag wins over CAVE_PIXEL_DENSITY.")
	fmt.Fprintln(os.Stderr, "  --density and --dense compose: --dense is the pxpipe content-shrink + multi-column packing, --density is the per-cell geometry it draws with (the 2-layer max overlay applies to single-column output only).")
	fmt.Fprintln(os.Stderr, "usage: caveman-engine pixel simulate [--model MODEL] [--density conservative|balanced|max] <request.json>")
	fmt.Fprintln(os.Stderr, "  simulate sniffs request JSON without network: input -> OpenAI Responses, contents -> Gemini, messages -> Anthropic Messages. --model overrides or fills the body model. --density overrides CAVE_PIXEL_DENSITY for the simulated transform (still reader-model gated).")
}

// resolvePixelDensity turns a CLI --density flag into a level. The flag is the
// dev-tool surface, so it is LOUD: an invalid value is a usage error, not a silent
// fall to conservative (the engine's env path stays fail-closed instead). An empty
// flag defers to CAVE_PIXEL_DENSITY (default balanced, falsey → conservative).
func resolvePixelDensity(flag string) pixel.DensityLevel {
	if flag == "" {
		return pixel.DensityFromEnv()
	}
	switch pixel.DensityLevel(strings.ToLower(strings.TrimSpace(flag))) {
	case pixel.DensityConservative:
		return pixel.DensityConservative
	case pixel.DensityBalanced:
		return pixel.DensityBalanced
	case pixel.DensityMax:
		return pixel.DensityMax
	default:
		fatal("--density must be one of conservative|balanced|max (got %q)", flag)
		return pixel.DensityConservative // unreachable; fatal exits
	}
}

func runPixelRender(args []string) {
	cols := pixel.DefaultCols
	colsSet := false
	multicol := 1
	dense := false
	reflow := true
	densityFlag := ""
	file := ""
	usage := "usage: caveman-engine pixel render [--cols N] [--multicol N] [--density conservative|balanced|max] [--dense] [--no-reflow] <file>"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cols":
			i++
			if i >= len(args) {
				fatal(usage)
			}
			cols = parsePositiveInt("--cols", args[i])
			colsSet = true
		case "--multicol":
			i++
			if i >= len(args) {
				fatal(usage)
			}
			multicol = parsePositiveInt("--multicol", args[i])
		case "--density":
			i++
			if i >= len(args) {
				fatal(usage)
			}
			densityFlag = args[i]
		case "--dense":
			dense = true
		case "--no-reflow":
			reflow = false
		case "--help", "-h":
			pixelUsage()
			return
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fatal("unknown pixel render flag: %s", args[i])
			}
			if file != "" {
				fatal(usage)
			}
			file = args[i]
		}
	}
	if file == "" {
		fatal(usage)
	}
	input, err := os.ReadFile(file)
	if err != nil {
		fatal("pixel render read: %v", err)
	}
	// Density resolves flag > CAVE_PIXEL_DENSITY > default(balanced). The dev-tool
	// render is model-agnostic (standard tier), so it draws exactly the requested
	// level. --density and --dense compose: the level supplies the pack geometry
	// (style + per-image char budget + layers), --dense supplies pxpipe's content
	// shrink + multi-column fitting.
	level := resolvePixelDensity(densityFlag)
	// A forced multi-column layout draws the fixed multi-column grid, which ignores
	// the density RenderStyle/char budget/overlay (RenderTextToPNGsMultiCol, and
	// RenderDensePages' own multicol sub-path, draw conservative geometry). Resolve
	// AND report conservative so the labelled density matches the pixels actually
	// drawn — mirroring the gate, which already prices multicol at conservativeStdParams.
	if multicol > 1 {
		level = pixel.DensityConservative
	}
	draw := pixel.StdDensityDraw(level)
	if !colsSet {
		cols = draw.Cols
	}
	source := pixelRenderSource(string(input), reflow)
	twoLayer := draw.Layers == 2
	var images []pixel.RenderedImage
	switch {
	case twoLayer:
		// The max 2-layer red/blue overlay is single-column ONLY, and it must win over
		// --dense: RenderDensePages has no layer handling, so routing max through it would
		// silently drop the overlay while still reporting density:"max". This branch is
		// only reachable at multicol==1 (the clamp above forced conservative otherwise),
		// so `--dense --density max` renders the true overlay here. --dense/reflow still
		// applies via source; MeasureContentCols shrinks cols to the content width first.
		c := pixel.MeasureContentCols(source, cols, 1)
		images, err = pixel.RenderTextToTwoLayerPNGs(source, c, draw.CharBudget, draw.Style, draw.CanvasH)
	case dense:
		// --dense composes with the single-layer densities; RenderDensePages fits multi-
		// column internally (so --dense --multicol N stays on this path). Conservative
		// keeps the dense AA atlas (DenseRenderStyle), not the empty mono style, matching
		// pre-density dense. (max was handled by the twoLayer branch above.)
		// pxpipe src/core/render.ts:889-901 documents export passing reflow=true by default.
		style := draw.Style
		if style == (pixel.RenderStyle{}) {
			style = pixel.DenseRenderStyle
		}
		images, err = pixel.RenderDensePages(string(input), pixel.DensePagesOptions{Cols: cols, MultiCol: multicol, Reflow: reflow, Style: &style, MaxCharsPerImage: draw.CharBudget, MaxHeightPx: draw.CanvasH})
	case multicol > 1:
		// Non-dense multi-column: the fixed conservative grid (level forced above).
		images, err = pixel.RenderTextToPNGsMultiCol(source, cols, multicol)
	default:
		c := pixel.MeasureContentCols(source, cols, 1)
		images, err = pixel.RenderTextToPNGsWithCharLimit(source, c, draw.CharBudget, draw.Style, draw.CanvasH, "")
	}
	if err != nil {
		fatal("pixel render: %v", err)
	}
	reports, summary := pixelRenderReports(images, input, level)
	enc := json.NewEncoder(os.Stdout)
	for i, img := range images {
		outPath := fmt.Sprintf("%s.px%d.png", file, i+1)
		if err := os.WriteFile(outPath, img.PNG, 0o644); err != nil {
			fatal("pixel render write %s: %v", outPath, err)
		}
		if err := enc.Encode(reports[i]); err != nil {
			fatal("pixel render report: %v", err)
		}
	}
	if err := enc.Encode(summary); err != nil {
		fatal("pixel render summary: %v", err)
	}
}

func pixelRenderSource(text string, reflow bool) string {
	if !reflow {
		return text
	}
	if packed, ok := pixel.Reflow(text); ok {
		return packed
	}
	return text
}

type pixelRenderSummary struct {
	Summary        bool   `json:"summary"`
	Pages          int    `json:"pages"`
	TextEstTokens  int    `json:"textEstTokens"`
	ImageEstTokens int    `json:"imageEstTokens"`
	Density        string `json:"density"`
	Layers         int    `json:"layers,omitempty"`
}

// pixelRenderReports builds the per-page JSONL reports plus the final summary
// line callers gate on. Both sides of summary use local estimates: the
// offline o200k text count and the margin-padded image token estimate.
// density is the RESOLVED level actually rendered; layers is set only when a page
// stacks the 2-layer (max) overlay, so existing single-layer fields stay unchanged.
func pixelRenderReports(images []pixel.RenderedImage, input []byte, level pixel.DensityLevel) ([]pixelRenderReport, pixelRenderSummary) {
	reports := make([]pixelRenderReport, 0, len(images))
	imageEstTokens := 0
	maxLayers := 0
	for _, img := range images {
		est := pixelEstTokens(img)
		imageEstTokens += est
		rep := pixelRenderReport{
			Width:         img.Width,
			Height:        img.Height,
			CharsRendered: img.CharsRendered,
			DroppedChars:  img.DroppedChars,
			EstTokens:     est,
			Density:       string(level),
		}
		if img.Layers == 2 {
			rep.Layers = img.Layers
		}
		if img.Layers > maxLayers {
			maxLayers = img.Layers
		}
		reports = append(reports, rep)
	}
	summary := pixelRenderSummary{
		Summary:        true,
		Pages:          len(images),
		TextEstTokens:  tokens.Default().Count(input),
		ImageEstTokens: imageEstTokens,
		Density:        string(level),
	}
	if maxLayers == 2 {
		summary.Layers = maxLayers
	}
	return reports, summary
}

type pixelRenderReport struct {
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	CharsRendered int    `json:"charsRendered"`
	DroppedChars  int    `json:"droppedChars"`
	EstTokens     int    `json:"estTokens"`
	Density       string `json:"density"`
	Layers        int    `json:"layers,omitempty"`
}

func pixelEstTokens(img pixel.RenderedImage) int {
	base := pixel.AnthropicImageTokens(img.Width, img.Height, pixel.StandardPixelTier)
	return int(math.Ceil(float64(base) * pixel.ImageCostSafetyMargin))
}

func runPixelSimulate(args []string) {
	model := ""
	modelOverride := false
	densityFlag := ""
	file := ""
	simUsage := "usage: caveman-engine pixel simulate [--model MODEL] [--density conservative|balanced|max] <request.json>"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			i++
			if i >= len(args) {
				fatal(simUsage)
			}
			model = args[i]
			modelOverride = true
		case "--density":
			i++
			if i >= len(args) {
				fatal(simUsage)
			}
			densityFlag = args[i]
		case "--help", "-h":
			pixelUsage()
			return
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fatal("unknown pixel simulate flag: %s", args[i])
			}
			if file != "" {
				fatal(simUsage)
			}
			file = args[i]
		}
	}
	if file == "" {
		fatal(simUsage)
	}
	// --density is the flag-wins override: validate loudly, then set the env the
	// transform reads (DensityFromEnv) so the simulated transform resolves the same
	// density path a wrapped agent would — still reader-model gated inside.
	if densityFlag != "" {
		level := resolvePixelDensity(densityFlag)
		os.Setenv("CAVE_PIXEL_DENSITY", string(level))
	}
	body, err := os.ReadFile(file)
	if err != nil {
		fatal("pixel simulate read: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		fatal("pixel simulate parse: %v", err)
	}
	if model == "" {
		model = modelFromJSON(root)
	}
	if modelOverride {
		rawModel, _ := json.Marshal(model)
		root["model"] = rawModel
		body, err = json.Marshal(root)
		if err != nil {
			fatal("pixel simulate model override: %v", err)
		}
	}
	opts := pixel.DefaultTransformOptions(model)
	var info pixel.TransformInfo
	switch sniffPixelRequest(root) {
	case "openai":
		_, info, err = pixel.TransformOpenAI(body, opts)
	case "gemini":
		_, info, err = pixel.TransformGemini(body, opts)
	case "anthropic":
		_, info, err = pixel.TransformAnthropic(body, opts)
	default:
		fatal("pixel simulate: cannot sniff request format (expected input, contents, or messages)")
	}
	enc := json.NewEncoder(os.Stdout)
	if encErr := enc.Encode(info); encErr != nil {
		fatal("pixel simulate report: %v", encErr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pixel simulate transform: %v\n", err)
		os.Exit(1)
	}
}

func sniffPixelRequest(root map[string]json.RawMessage) string {
	if _, ok := root["input"]; ok {
		return "openai"
	}
	if _, ok := root["contents"]; ok {
		return "gemini"
	}
	if _, ok := root["messages"]; ok {
		return "anthropic"
	}
	return ""
}

func modelFromJSON(root map[string]json.RawMessage) string {
	raw, ok := root["model"]
	if !ok {
		return ""
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return ""
	}
	return model
}

func parsePositiveInt(name, raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		fatal("%s must be a positive integer", name)
	}
	return n
}

// toonEncodeBytes converts JSON to the lossless TOON subset. It fails closed:
// any input that is not valid JSON, or whose shape is outside the proven
// round-trip subset (deeply nested / non-uniform), returns an error rather than a
// lossy approximation — so the caller keeps JSON instead of trusting bad TOON.
func toonEncodeBytes(input []byte) ([]byte, error) {
	out, ok := compressors.NewTOON().Compress(input)
	if !ok {
		return nil, fmt.Errorf("not losslessly TOON-encodable (invalid JSON, or nested/non-uniform shape); keep JSON")
	}
	return out, nil
}

// toonDecodeBytes converts TOON back to compact JSON. Malformed TOON fails closed
// with an error; it never emits partial or guessed JSON.
func toonDecodeBytes(input []byte) ([]byte, error) {
	v, ok := compressors.DecodeTOON(input)
	if !ok {
		return nil, fmt.Errorf("not valid TOON")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func runEvals(args []string) {
	if len(args) < 1 || args[0] != "run" {
		fatal("usage: caveman-engine evals run [--fixtures DIR]")
	}
	fixtureDir := ""
	if len(args) == 3 && args[1] == "--fixtures" && args[2] != "" {
		fixtureDir = args[2]
	} else if len(args) != 1 {
		fatal("usage: caveman-engine evals run [--fixtures DIR]")
	}
	var report evals.Report
	var err error
	if fixtureDir == "" {
		report, err = evals.Run()
	} else {
		report, err = evals.RunDir(fixtureDir)
	}
	if err != nil {
		fatal("evals: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fatal("evals output: %v", err)
	}
	pixelChecks, pixelPassed := pixelDensityChecks()
	if err := enc.Encode(map[string]any{"pixel_density_gate": pixelChecks, "passed": pixelPassed}); err != nil {
		fatal("evals pixel output: %v", err)
	}
	if !report.Passed || !pixelPassed {
		os.Exit(1) // fail closed: any fixture/grader/pixel-gate failure is a non-zero exit
	}
}

type pixelCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// pixelDensityChecks regression-gates the pixel density geometry offline, with
// no model call: every level×tier full page must stay inside its no-resize
// envelope (long edge ≤ tier max px AND patch tokens ≤ tier cap — beyond either,
// Anthropic downscales server-side and destroys the glyphs), the hard floors
// must hold (never >2 layers), unknown models must fail closed to conservative
// std, and the capacity multipliers must match the renderer's configured
// geometry. It checks real renders through the live draw path rather than
// restating renderer internals, so atlas or geometry drift fails the gate.
func pixelDensityChecks() ([]pixelCheck, bool) {
	var checks []pixelCheck
	add := func(name string, passed bool, detail string) {
		checks = append(checks, pixelCheck{Name: name, Passed: passed, Detail: detail})
	}
	levels := []pixel.DensityLevel{pixel.DensityConservative, pixel.DensityBalanced, pixel.DensityMax}
	for _, model := range []string{"claude-3-5-sonnet", "claude-fable-5"} {
		for _, level := range levels {
			draw := pixel.ResolveDensityDraw(model, level)
			name := fmt.Sprintf("envelope %s/%s", model, level)
			text := strings.Repeat("M", draw.CharBudget)
			var (
				images []pixel.RenderedImage
				err    error
			)
			if draw.Layers == 2 {
				images, err = pixel.RenderTextToTwoLayerPNGs(text, draw.Cols, draw.CharBudget, draw.Style, draw.CanvasH)
			} else {
				images, err = pixel.RenderTextToPNGsWithCharLimit(text, draw.Cols, draw.CharBudget, draw.Style, draw.CanvasH, "")
			}
			if err != nil || len(images) == 0 {
				add(name, false, fmt.Sprintf("render failed: %v (%d images)", err, len(images)))
				continue
			}
			img := images[0]
			long := max(img.Width, img.Height)
			tokens := pixel.AnthropicImageTokens(img.Width, img.Height, draw.Tier)
			passed := long <= draw.Tier.MaxPx && tokens <= draw.Tier.MaxTokens
			add(name, passed, fmt.Sprintf("%dx%d px, %d patch tokens (tier max %d px / %d tokens)",
				img.Width, img.Height, tokens, draw.Tier.MaxPx, draw.Tier.MaxTokens))
			// Floor, not a mapping: 2 layers only ever at max, never more than 2.
			// Non-density-capable models fail closed to 1 layer at every level.
			floorOK := draw.Layers <= 2 && (draw.Layers != 2 || level == pixel.DensityMax)
			add(fmt.Sprintf("layer floor %s/%s", model, level), floorOK,
				fmt.Sprintf("layers=%d (2 only at max, never >2)", draw.Layers))
		}
	}
	// The density-capable hires exemplar must actually graduate: max = 2 layers.
	fable := pixel.ResolveDensityDraw("claude-fable-5", pixel.DensityMax)
	add("hires max renders 2 layers", fable.Layers == 2, fmt.Sprintf("layers=%d want 2", fable.Layers))
	// A non-density-capable model must fail closed at every level.
	sonnet := pixel.ResolveDensityDraw("claude-3-5-sonnet", pixel.DensityMax)
	add("non-capable model fails closed", sonnet.Layers == 1 && sonnet.CharBudget == 28080,
		fmt.Sprintf("layers=%d chars/page=%d want conservative std 1/28080", sonnet.Layers, sonnet.CharBudget))
	cons := pixel.StdDensityDraw(pixel.DensityConservative).CharBudget
	bal := pixel.StdDensityDraw(pixel.DensityBalanced).CharBudget
	maxB := pixel.StdDensityDraw(pixel.DensityMax).CharBudget
	add("capacity conservative", cons == 28080, fmt.Sprintf("chars/page=%d want 28080", cons))
	add("capacity balanced", bal == 46291, fmt.Sprintf("chars/page=%d want configured balanced capacity", bal))
	add("capacity max", maxB == 2*bal, fmt.Sprintf("chars/page=%d want %d (2 layers)", maxB, 2*bal))
	unknown := pixel.ResolveDensityDraw("some-unknown-model", pixel.DensityMax)
	add("unknown model fails closed", unknown.CharBudget == 28080 && unknown.Layers == 1,
		fmt.Sprintf("chars/page=%d layers=%d want conservative std 28080/1", unknown.CharBudget, unknown.Layers))
	for _, c := range checks {
		if !c.Passed {
			return checks, false
		}
	}
	return checks, true
}

func ccrPath() string {
	if p := os.Getenv("CAVEMAN_CCR_DB"); p != "" {
		return p
	}
	return filepath.Join(caveHome(), "ccr.db")
}

// caveHome resolves and creates ~/.caveman, honoring CAVEMAN_HOME.
func caveHome() string {
	home := os.Getenv("CAVEMAN_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			fatal("cannot resolve home directory: %v", err)
		}
		home = filepath.Join(h, ".caveman")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		fatal("cannot create ~/.caveman: %v", err)
	}
	return home
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
