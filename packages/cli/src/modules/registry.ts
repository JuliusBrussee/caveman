// Caveman modules: the things a developer switches on and off with
// `caveman on|off <module>`. One entry per module; onboarding, status,
// doctor and the signed modules.json all read this table.
//
// Every module is on by default in onboarding. A module that cannot run yet
// (routing before Cloud answers) still shows, with its reason.

export type ModuleId = "output" | "input" | "waste-fixes" | "routing" | "scripts" | "browse";

// Auto: the model the routing module adds to an agent's model picker.
// caveman-proxy asks Caveman Cloud only about requests naming it
// (proxy/internal/gateway/route.go AutoModel); every other model goes as sent.
export const AUTO_MODEL = "caveman-auto";
export const AUTO_NAME = "Auto";
export const AUTO_DESCRIPTION = "Caveman pick model + effort each turn. Hard ask, big brain. Easy ask, save rocks.";

// Capability config keys this module owns, with the value written when the
// module is switched on and when it is switched off. Keys are the existing
// CAPABILITY_KEYS in index.ts, or a top-level key of the same config file
// (`learnAutopilot`).
export type CapabilityEffect = { key: string; on: string | boolean; off: string | boolean };

export type ModuleDef = {
  id: ModuleId;
  // Shown in onboarding and status. Plain words, no internals.
  title: string;
  summary: string;
  defaultOn: boolean;
  // Signing in to Caveman Cloud is required before the module does anything.
  needsSignIn: boolean;
  // Works through the native agent wiring (`caveman enable <agent>`: route,
  // hooks, recovery MCP). The wiring stays while any such module is on, and
  // needs caveman-proxy and caveman-mcp, so such modules list both.
  wiresAgents: boolean;
  capabilities: CapabilityEffect[];
  // Names from GO_BINARIES the module needs on disk.
  binaries: string[];
  // A binary shipped from another repository and installed by its own
  // installer (Blocks). The hub downloads it and calls `install`; it never
  // writes that binary's harness files itself.
  external?: { binary: string; install: string[]; uninstall: string[]; status: string[] };
};

export const MODULES: readonly ModuleDef[] = [
  {
    id: "output",
    title: "output",
    summary: "the agent says less",
    defaultOn: true,
    needsSignIn: false,
    wiresAgents: true,
    capabilities: [{ key: "think.core", on: true, off: false }],
    binaries: ["caveman-proxy", "caveman-mcp"],
  },
  {
    id: "input",
    title: "input",
    summary: "logs, JSON, code and diffs shrink before the model reads them",
    defaultOn: true,
    needsSignIn: false,
    wiresAgents: true,
    capabilities: [
      { key: "think.mode", on: "compress", off: "record" },
      { key: "think.toon", on: true, off: false },
      { key: "think.shrink", on: true, off: false },
    ],
    binaries: ["caveman-proxy", "caveman-engine", "caveman-mcp", "cavemem", "caveman-shrink"],
  },
  {
    id: "waste-fixes",
    title: "waste fixes",
    summary: "finds your agent's worst waste and fixes it",
    defaultOn: true,
    needsSignIn: false,
    wiresAgents: true,
    // The learn autopilot the native SessionEnd hook starts.
    capabilities: [{ key: "learnAutopilot", on: true, off: false }],
    binaries: ["caveman-proxy", "caveman-mcp"],
  },
  {
    id: "routing",
    title: "routing",
    // The onboarding picker cuts hints at the terminal width (62 characters at
    // 80 columns): what leaves the machine comes first.
    summary: "asks to Auto go to Cloud to pick model + effort",
    defaultOn: true,
    // Adds Auto (AUTO_MODEL) to each wired agent's model picker; only
    // requests naming it are routed. Decisions come from Cloud (POST
    // /v1/route), so it acts only once signed in; caveman-proxy's route stage
    // reads `modules.routing` itself, so no capability key carries it.
    needsSignIn: true,
    wiresAgents: true,
    capabilities: [],
    binaries: ["caveman-proxy", "caveman-mcp"],
  },
  {
    id: "scripts",
    title: "scripts",
    summary: "reusable scripts your agent keeps",
    defaultOn: true,
    needsSignIn: false,
    wiresAgents: false,
    capabilities: [],
    binaries: [],
    external: {
      binary: "caveman-blocks",
      install: ["hooks", "install"],
      uninstall: ["hooks", "uninstall"],
      status: ["hooks", "status", "--json"],
    },
  },
  {
    id: "browse",
    title: "browse",
    summary: "compressed pages for browser tools",
    defaultOn: true,
    needsSignIn: false,
    wiresAgents: false,
    capabilities: [{ key: "execute.browse_tool", on: true, off: false }],
    binaries: ["caveman-browse"],
  },
];

export function findModule(id: string): ModuleDef | undefined {
  return MODULES.find((m) => m.id === id);
}
