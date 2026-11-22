package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/mcp"
)

const companionToolTimeout = 45 * time.Second

// CompanionOptions controls which bounded structured tools CompanionToolsWithOptions exposes.
type CompanionOptions struct {
	IncludeBrowse   bool
	IncludeRetrieve bool
}

// CompanionTools exposes Caveman's bounded T3-Code-style structured built-in
// tools (`repo_batch_read`, `repo_symbol_outline`, `repo_search_code`,
// `pr_read`, `pr_ci_logs`, optional lazy `browser_snapshot` / `browser_act` /
// `browser_eval`, plus `caveman_retrieve`) directly as flat, MCP-annotated
// tools WITHOUT wrapping or hiding any of the user's existing MCP servers.
//
// This companion surface is designed specifically for harnesses that already
// ship their own native codemode (Claude Code, Codex CLI, OpenCode): their
// native codemode calls these bounded structured tools instead of shelling out
// raw `gh`, `cat`, or DOM dumps while keeping CCR byte-exact recovery intact.
func CompanionTools(
	workspaceRoot string,
	store *ccr.Store,
	eng *engine.Engine,
	log *slog.Logger,
	includeBrowse bool,
) ([]mcp.Tool, func() error) {
	return CompanionToolsWithOptions(workspaceRoot, store, eng, log, CompanionOptions{
		IncludeBrowse:   includeBrowse,
		IncludeRetrieve: true,
	})
}

// CompanionToolsWithOptions builds the companion flat MCP toolset with explicit
// control over optional browser tools and `caveman_retrieve` (set
// IncludeRetrieve=false when the session already registers `caveman` MCP via
// `caveman wrap` so the model does not see duplicate retrieve tools).
func CompanionToolsWithOptions(
	workspaceRoot string,
	store *ccr.Store,
	eng *engine.Engine,
	log *slog.Logger,
	opts CompanionOptions,
) ([]mcp.Tool, func() error) {
	if eng == nil && store != nil {
		eng = engine.New(store, nil)
	}
	upstreams := []Upstream{
		NewRepoUpstream(workspaceRoot, store, eng),
		NewPRUpstream(workspaceRoot, store, eng, nil),
	}
	var browseUp *BrowseUpstream
	if opts.IncludeBrowse {
		browseUp = NewBrowseUpstream(eng, log, nil)
		upstreams = append(upstreams, browseUp)
	}

	var tools []mcp.Tool
	ctx := context.Background()
	for _, up := range upstreams {
		specs, err := up.ListTools(ctx)
		if err != nil {
			continue
		}
		for _, spec := range specs {
			u := up
			toolName := spec.Name
			exposedName := companionToolName(spec.Server, spec.Name)
			tools = append(tools, mcp.Tool{
				Name:            exposedName,
				Description:     spec.Description,
				InputSchema:     spec.InputSchema,
				Annotations:     annotationsToMap(spec),
				ExemptResultCap: toolName == "browser_recover",
				Handler: func(raw json.RawMessage) mcp.ToolResult {
					var args map[string]any
					if len(raw) > 0 {
						if err := json.Unmarshal(raw, &args); err != nil {
							return mcp.ToolError("cave_invalid_params", fmt.Sprintf("invalid %s arguments", exposedName))
						}
					}
					if args == nil {
						args = map[string]any{}
					}
					callCtx, cancel := context.WithTimeout(context.Background(), companionToolTimeout)
					defer cancel()
					res, err := u.CallTool(callCtx, toolName, args)
					if err != nil {
						return mcp.ToolError("cave_tool_failed", err.Error())
					}
					return mcp.ToolText(res)
				},
			})
		}
	}

	if opts.IncludeRetrieve && eng != nil {
		gw := NewGateway(store, eng, log)
		roAnnot := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}
		tools = append(tools, mcp.Tool{
			Name:            "caveman_retrieve",
			Description:     "Retrieve exact original bytes for a <<ccr:...>> handle archived by Caveman tools or proxy compression.",
			InputSchema:     mcp.ObjectSchema(map[string]any{"recovery_handle": mcp.StringProp("The CCR recovery handle."), "query": mcp.StringProp("Optional query to narrow records.")}, "recovery_handle"),
			Annotations:     roAnnot,
			ExemptResultCap: true,
			Handler:         gw.handleRetrieve,
		})
	}

	cleanup := func() error {
		if browseUp != nil {
			return browseUp.Close()
		}
		return nil
	}
	return tools, cleanup
}

func companionToolName(server, name string) string {
	switch server {
	case "repo":
		return "repo_" + name
	case "pr":
		if name == "pr_read" {
			return "pr_read"
		}
		return "pr_" + name
	case "browse":
		return name
	default:
		return server + "_" + name
	}
}

func annotationsToMap(spec ToolSpec) map[string]any {
	ro := IsReadOnlyTool(spec)
	m := map[string]any{
		"readOnlyHint":    ro,
		"destructiveHint": !ro,
		"idempotentHint":  ro,
	}
	if spec.Annotations != nil {
		if spec.Annotations.Title != "" {
			m["title"] = spec.Annotations.Title
		}
		if spec.Annotations.ReadOnlyHint != nil {
			m["readOnlyHint"] = *spec.Annotations.ReadOnlyHint
		}
		if spec.Annotations.DestructiveHint != nil {
			m["destructiveHint"] = *spec.Annotations.DestructiveHint
		}
		if spec.Annotations.IdempotentHint != nil {
			m["idempotentHint"] = *spec.Annotations.IdempotentHint
		}
		if spec.Annotations.OpenWorldHint != nil {
			m["openWorldHint"] = *spec.Annotations.OpenWorldHint
		}
	}
	return m
}
