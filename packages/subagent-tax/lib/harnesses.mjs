// The harness registry: how to point each installed coding harness at the
// local sink for exactly one request. No recipe ever MODIFIES the user's
// config files — redirection rides env vars and ephemeral home dirs.
//
// `touchesRealConfig` marks the recipes that deliberately RUN AGAINST the real
// config (today: claude, because the user's own plugins/MCP are the tax being
// measured). Those launch the real binary in the real environment, which boots
// their MCP servers, runs their hooks, and leaves the harness's own session
// artifacts behind. It is disclosed before the run and `--isolate` opts out.
//
// `confirmed: true` means the recipe was checked end-to-end against the real
// installed harness. `variant` names what configuration the measurement
// reflects — "real config" rows include the user's plugins/MCP; isolated rows
// measure the harness floor. The table prints the label; mixing them silently
// would be a lie.
//
// `mcpPattern` is only set where the harness's MCP tool-naming convention has
// been CONFIRMED. Absent means the mcp column reports "-" (unknown), never 0.
//
// ("verified" is deliberately avoided throughout: in this repo it is a reserved
// savings-accounting word and must never appear about anything else.)

export const PROMPT = "Reply with exactly: DONE";

const base = (port) => `http://127.0.0.1:${port}`;

export const HARNESSES = {
  claude: {
    id: "claude",
    binNames: ["claude"],
    wire: "anthropic-messages",
    // Claude Code is the one harness whose MCP naming convention
    // (mcp__server__tool) is confirmed; other harnesses report null, not 0.
    mcpPattern: /^mcp__/,
    confirmed: true,
    variant: "real config",
    // Reads (never writes) the user's config, but launching the real binary
    // against it boots their MCP servers, runs their hooks, and leaves a
    // session transcript in ~/.claude/projects. Disclosed before the run.
    touchesRealConfig: true,
    realConfigEffects: "boots your MCP servers, runs your hooks, and leaves a session transcript in ~/.claude/projects",
    launch: ({ port, homeDir, isolate }) => ({
      argv: ["claude", "-p", PROMPT],
      env: {
        ANTHROPIC_BASE_URL: base(port),
        ANTHROPIC_API_KEY: "sink-placeholder-key",
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
        DISABLE_AUTOUPDATER: "1",
        ...(isolate ? { CLAUDE_CONFIG_DIR: homeDir } : {}),
      },
      files: [],
    }),
    isolateVariant: "isolated config (floor)",
    notes:
      "Default captures the user's REAL installed tax — their plugins and MCP servers are the point. Allow/disallow-tools flags would not strip schemas anyway (checked 2026-08-07). --isolate switches to an empty CLAUDE_CONFIG_DIR for the harness floor.",
  },

  codex: {
    id: "codex",
    binNames: ["codex"],
    wire: "openai-responses",
    confirmed: true,
    variant: "minimal home (floor)",
    launch: ({ port, homeDir }) => ({
      argv: ["codex", "exec", "--skip-git-repo-check", "-m", "gpt-5.5", PROMPT],
      env: { CODEX_HOME: homeDir, SINK_API_KEY: "dummy" },
      files: [
        {
          path: "config.toml",
          content: [
            `model_provider = "sink"`,
            ``,
            `[model_providers.sink]`,
            `name = "Sink"`,
            `base_url = "${base(port)}/v1"`,
            `wire_api = "responses"`,
            `env_key = "SINK_API_KEY"`,
            ``,
          ].join("\n"),
        },
      ],
    }),
    notes:
      "Ephemeral CODEX_HOME with a minimal config — the harness floor, not the user's real tax. Copying the real config works but fires background memory-agent calls and real side-effect network I/O (plugin git clones, MCP OAuth), which a zero-touch public tool must not trigger by default.",
  },

  gemini: {
    id: "gemini",
    binNames: ["gemini"],
    wire: "gemini-generatecontent",
    confirmed: true,
    variant: "isolated home (api-key mode)",
    launch: ({ port, homeDir }) => ({
      argv: ["gemini", "-p", PROMPT, "--skip-trust", "-m", "gemini-2.5-flash"],
      env: {
        HOME: homeDir,
        GEMINI_API_KEY: "sink-placeholder-key",
        GEMINI_BASE_URL: base(port),
        GOOGLE_GEMINI_BASE_URL: base(port),
      },
      files: [
        {
          path: ".gemini/settings.json",
          content: `${JSON.stringify({ security: { auth: { selectedType: "gemini-api-key" } } }, null, 2)}\n`,
        },
      ],
    }),
    notes:
      "Isolated HOME so the user's real ~/.gemini OAuth login is never consulted. The -m pin matters: gemini-3* default models fire a strict-JSON preflight classifier that retry-loops against a plain-text sink reply and never reaches the agent turn.",
  },

  opencode: {
    id: "opencode",
    binNames: ["opencode"],
    wire: "openai-responses",
    confirmed: true,
    variant: "isolated config",
    launch: ({ port, homeDir }) => ({
      argv: ["opencode", "run", PROMPT, "--model", "openai/gpt-4o"],
      env: {
        OPENCODE_CONFIG_CONTENT: JSON.stringify({
          $schema: "https://opencode.ai/config.json",
          provider: { openai: { options: { baseURL: `${base(port)}/v1`, apiKey: "sink-placeholder-key" } } },
          model: "openai/gpt-4o",
        }),
        XDG_CONFIG_HOME: `${homeDir}/xdg-config`,
        XDG_DATA_HOME: `${homeDir}/xdg-data`,
        XDG_CACHE_HOME: `${homeDir}/xdg-cache`,
      },
      dirs: ["xdg-config", "xdg-data", "xdg-cache"],
      files: [],
    }),
    notes:
      "OPENCODE_CONFIG_CONTENT sets the sink provider; XDG isolation keeps the user's real opencode config out (their config can narrow the model catalog and break the run — observed) and keeps their auth.json unconsulted. Despite the provider id, opencode speaks the OpenAI Responses API here. It sends a small title-generation call first; the most-tools primary pick skips it.",
  },

  "cursor-agent": {
    id: "cursor-agent",
    binNames: ["cursor-agent"],
    wire: "proprietary-backend",
    confirmed: false,
    // Probed 2026-08-07: the endpoint IS redirectable (hidden -e/--endpoint
    // flag), but nothing measurable arrives. The client streams
    // agent.v1.AgentRunRequest protobuf over a bidirectional Connect/HTTP-2
    // RPC carrying only conversation turns, a model id, and the user's own MCP
    // tools — no system-prompt field, no builtin tool schemas, and no LLM
    // provider host anywhere in the bundle. The prefix is assembled
    // server-side, so it is structurally unmeasurable by any local sink.
    launch: null,
    unmeasurableReason:
      "thin client to Cursor's backend: the agent loop runs server-side and the wire schema (agent.v1.AgentRunRequest) has no system-prompt or builtin-tool-schema field, so no prefix crosses the wire to measure",
  },

  pi: {
    id: "pi",
    binNames: ["pi"],
    wire: "anthropic-messages",
    confirmed: true,
    variant: "isolated home (4 default tools)",
    launch: ({ port, homeDir, sessionDir }) => ({
      argv: ["pi", "-p", PROMPT, "--provider", "sink", "--model", "sink-model"],
      env: { PI_CODING_AGENT_DIR: homeDir, PI_CODING_AGENT_SESSION_DIR: sessionDir },
      files: [
        {
          path: "models.json",
          content: `${JSON.stringify(
            {
              providers: {
                sink: {
                  baseUrl: base(port),
                  api: "anthropic-messages",
                  apiKey: "sink-placeholder-key",
                  models: [{ id: "sink-model" }],
                },
              },
            },
            null,
            2,
          )}\n`,
        },
      ],
    }),
    notes:
      "Minimal models.json is sufficient (id-only model entry). Without the env isolation pi fails closed with 'Unknown provider' — it never falls back to a real provider.",
  },
};

export const HARNESS_IDS = Object.keys(HARNESSES);
