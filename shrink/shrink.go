// Package shrink compresses MCP/OpenAI tool-definition catalogs. It is a thin
// wrapper over the engine's tool-schema compressor: it drops annotation metadata
// and truncates descriptions while preserving the structural selection surface
// (tool and parameter names, types, enums, required) verbatim. Description
// reduction is model-visible and lossy, so this structural invariant does not
// guarantee same-tool behavior. Everything it reports is `inferred`.
package shrink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

// schemaType forces the engine to route to the tool-schema compressor.
const schemaType = "toolschema"

// Option configures where Shrink and Recover read/write the CCR recovery store.
// With no option they use the DURABLE shared store (CAVEMAN_CCR_DB, else
// ~/.caveman/ccr.db) — the same store the engine CLI, MCP server, and gateway
// use — so a handle minted by Shrink resolves later, from a different process,
// via Recover. That durability is the whole point: an in-memory store closed on
// return (the previous behaviour) minted handles that were never resolvable, so
// the reversibility guarantee was false on every call.
type Option func(*config)

type config struct {
	store *ccr.Store // caller-supplied store; its lifecycle is the caller's
	path  string     // else open this path (":memory:" for an ephemeral store)
}

// WithStore uses a caller-owned recovery store. The caller keeps ownership and
// must Close it; Shrink/Recover will not.
func WithStore(s *ccr.Store) Option { return func(c *config) { c.store = s } }

// WithStorePath opens (and closes) a recovery store at path. Use a real file path
// for durable recovery; ":memory:" is ephemeral and only sensible within one call.
func WithStorePath(path string) Option { return func(c *config) { c.path = path } }

// resolveStore returns the store to use plus a cleanup func. A caller-supplied
// store is returned with a no-op cleanup (the caller owns it); otherwise a store
// is opened at the configured or default path and cleanup closes it. The opened
// store is durable: engine.Compress commits the recovery row before it returns, so
// the handle resolves after Close from a fresh store instance.
func resolveStore(opts []Option) (*ccr.Store, func() error, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.store != nil {
		return cfg.store, func() error { return nil }, nil
	}
	path := cfg.path
	if path == "" {
		p, err := defaultCCRPath()
		if err != nil {
			return nil, nil, err
		}
		path = p
	}
	s, err := ccr.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open recovery store %q: %w", path, err)
	}
	return s, s.Close, nil
}

// defaultCCRPath resolves the durable shared recovery store path, mirroring the
// engine CLI, MCP server, and proxy: CAVEMAN_CCR_DB, else CAVEMAN_HOME/ccr.db,
// else ~/.caveman/ccr.db. It creates the parent directory so the first shrink on a
// machine succeeds.
func defaultCCRPath() (string, error) {
	if p := os.Getenv("CAVEMAN_CCR_DB"); p != "" {
		return p, nil
	}
	home := os.Getenv("CAVEMAN_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		home = filepath.Join(h, ".caveman")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", home, err)
	}
	return filepath.Join(home, "ccr.db"), nil
}

// Result is the outcome of Shrink.
type Result struct {
	Output         []byte  `json:"-"`
	TokensBefore   int     `json:"tokens_before"`
	TokensAfter    int     `json:"tokens_after"`
	Ratio          float64 `json:"ratio"`
	Basis          string  `json:"basis"`
	ContentType    string  `json:"content_type"`
	RecoveryHandle string  `json:"recovery_handle,omitempty"`
}

// Shrink compresses a tool catalog with an S4 lossy transform. It is fail-open:
// on any parse problem or when the result would not be smaller, it returns the
// input unchanged with ratio 0 and no handle.
//
// When it does compress (a lossy S4 transform), it writes the exact original to
// the DURABLE recovery store first and returns its handle; engine.Compress commits
// that write before it publishes the transformed bytes, so a non-empty
// RecoveryHandle always resolves via Recover — including from a later, separate
// process. With no option, the store is the shared CAVEMAN_CCR_DB / ~/.caveman/ccr.db.
func Shrink(input []byte, opts ...Option) (Result, error) {
	store, cleanup, err := resolveStore(opts)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	eng := engine.New(store, nil)
	res, err := eng.Compress(input, engine.Options{Mode: engine.ModeCompress, Type: schemaType})
	out := Result{
		Output:         res.Output,
		TokensBefore:   res.TokensBefore,
		TokensAfter:    res.TokensAfter,
		Ratio:          res.Ratio,
		Basis:          res.Basis,
		ContentType:    res.ContentType,
		RecoveryHandle: res.RecoveryHandle,
	}
	if err != nil {
		// Engine returns a fully accounted, byte-identical pass-through result
		// when CCR persistence fails. Preserve it so library callers can forward
		// safely while still receiving the operational error.
		return out, err
	}
	return out, nil
}

// Recover returns the exact original bytes for a handle minted by Shrink. It reads
// the same durable store Shrink wrote (CAVEMAN_CCR_DB / ~/.caveman/ccr.db by
// default), so recovery works across processes. An unknown handle returns
// ccr.ErrNotFound — recovery never guesses.
func Recover(handle string, opts ...Option) ([]byte, error) {
	store, cleanup, err := resolveStore(opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return store.Get(handle)
}

// ToolReport is the inferred token reduction for one tool.
type ToolReport struct {
	Name         string  `json:"name"`
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	Ratio        float64 `json:"ratio"`
}

// Report is the lint output: per-tool and overall inferred reductions.
type Report struct {
	Tools        []ToolReport `json:"tools"`
	TokensBefore int          `json:"tokens_before"`
	TokensAfter  int          `json:"tokens_after"`
	Ratio        float64      `json:"ratio"`
	Basis        string       `json:"basis"`
}

// Lint measures the inferred per-tool and overall token reduction of a catalog
// without committing to it. Token counts use the engine's default (offline)
// counter; every number is `inferred`.
func Lint(input []byte) (Report, error) {
	entries, err := extractTools(input)
	if err != nil {
		return Report{}, err
	}
	counter := tokens.Default()
	comp := compressors.NewToolSchema()
	rep := Report{Basis: engine.BasisInferred}
	for _, e := range entries {
		before := counter.Count(e.raw)
		after := before
		if out, ok := comp.Compress(e.raw); ok {
			if a := counter.Count(out); a < before {
				after = a
			}
		}
		rep.Tools = append(rep.Tools, ToolReport{
			Name:         e.name,
			TokensBefore: before,
			TokensAfter:  after,
			Ratio:        ratio(before, after),
		})
		rep.TokensBefore += before
		rep.TokensAfter += after
	}
	rep.Ratio = ratio(rep.TokensBefore, rep.TokensAfter)
	return rep, nil
}

// ToolProfile is the selection-relevant surface of one tool: the structure a
// host uses to decide whether to call it. Descriptions are deliberately absent —
// they are exactly what shrink compresses.
type ToolProfile struct {
	Params   []string            `json:"params"`
	Enums    map[string][]string `json:"enums"`
	Required []string            `json:"required"`
}

// SelectionProfile extracts the structural selection surface of every tool.
// Shrink preserves this surface byte-for-byte; a test asserts
// SelectionProfile(input) == SelectionProfile(Shrink(input).Output). This does
// not prove same-tool behavior because descriptions remain model-visible.
func SelectionProfile(input []byte) (map[string]ToolProfile, error) {
	entries, err := extractTools(input)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ToolProfile, len(entries))
	for _, e := range entries {
		out[e.name] = profileOf(e.schema)
	}
	return out, nil
}

// --- catalog parsing --------------------------------------------------------

type toolEntry struct {
	name   string
	raw    []byte         // the tool object's JSON
	schema map[string]any // its inputSchema/parameters, if any
}

// extractTools handles the MCP ({"tools":[…]}), OpenAI (array of {function:…} or
// flat) and {"functions":[…]} shapes.
func extractTools(input []byte) ([]toolEntry, error) {
	var top any
	if err := json.Unmarshal(input, &top); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	var rawList []any
	switch t := top.(type) {
	case map[string]any:
		if v, ok := t["tools"].([]any); ok {
			rawList = v
		} else if v, ok := t["functions"].([]any); ok {
			rawList = v
		} else {
			rawList = []any{t} // a single tool object
		}
	case []any:
		rawList = t
	default:
		return nil, fmt.Errorf("unsupported catalog shape")
	}

	var entries []toolEntry
	for _, item := range rawList {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// OpenAI nests under "function".
		toolObj := obj
		if fn, ok := obj["function"].(map[string]any); ok {
			toolObj = fn
		}
		name, _ := toolObj["name"].(string)
		if name == "" {
			continue
		}
		raw, _ := json.Marshal(obj)
		schema := schemaOf(toolObj)
		entries = append(entries, toolEntry{name: name, raw: raw, schema: schema})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no named tools found")
	}
	return entries, nil
}

// schemaOf returns a tool's parameter schema, under either "inputSchema" (MCP) or
// "parameters" (OpenAI).
func schemaOf(tool map[string]any) map[string]any {
	if s, ok := tool["inputSchema"].(map[string]any); ok {
		return s
	}
	if s, ok := tool["parameters"].(map[string]any); ok {
		return s
	}
	return nil
}

// profileOf extracts param names, per-param enums, and the required list.
func profileOf(schema map[string]any) ToolProfile {
	p := ToolProfile{Enums: map[string][]string{}}
	if schema == nil {
		return p
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, v := range props {
			p.Params = append(p.Params, name)
			if pv, ok := v.(map[string]any); ok {
				if enum, ok := pv["enum"].([]any); ok {
					for _, ev := range enum {
						p.Enums[name] = append(p.Enums[name], fmt.Sprint(ev))
					}
				}
			}
		}
	}
	sort.Strings(p.Params)
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			p.Required = append(p.Required, fmt.Sprint(r))
		}
		sort.Strings(p.Required)
	}
	return p
}

func ratio(before, after int) float64 {
	if before <= 0 || after >= before {
		return 0
	}
	return float64(before-after) / float64(before)
}
