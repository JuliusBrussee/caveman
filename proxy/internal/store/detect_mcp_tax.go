package store

import (
	"fmt"
	"sort"
)

const mcpSurfaceMinServers = 3

func mcpSurfaceSink(cfg configScan, existing []Sink) []Sink {
	unique := map[string]bool{}
	perScope := map[string]int{}
	for _, scope := range cfg.MCPScopes {
		if !scope.Present {
			continue
		}
		perScope[scope.Scope] = len(scope.Servers)
		for _, server := range scope.Servers {
			unique[server] = true
		}
	}
	if len(unique) < mcpSurfaceMinServers {
		return nil
	}
	servers := make([]string, 0, len(unique))
	for server := range unique {
		servers = append(servers, server)
	}
	sort.Strings(servers)
	if len(servers) > 10 {
		servers = servers[:10]
	}
	evidence := map[string]any{
		"server_count": len(unique),
		"servers":      servers,
		"per_scope":    perScope,
	}
	for _, sink := range existing {
		if sink.SinkID != "config_tax:baseline" {
			continue
		}
		if unexplained, ok := sink.Evidence["unexplained_prefix_tokens"]; ok {
			evidence["unexplained_prefix_tokens"] = unexplained
			evidence["unexplained_prefix_tokens_attribution"] = "shared_not_attributed"
		}
		break
	}
	return []Sink{{
		SinkID: "mcp_surface",
		Title:  fmt.Sprintf("%d MCP servers are set up; their tool descriptions load with every message", len(unique)),
		Class:  classBehavioral, Basis: learnBasis, Framing: framingHistorical,
		Evidence:   evidence,
		Suggestion: "Tool descriptions of servers you don't use still ride along with every message. Turning them off, or enabling them only in the projects that need them, makes every message smaller. The count alone doesn't prove any one server is unneeded.",
	}}
}
