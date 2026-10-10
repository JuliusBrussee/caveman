// Runtime tests inherit a developer's shell, including alternate host profiles.
// HOME overrides in fixtures do not override these absolute config locations.
// Clear them before tests load; individual tests can still set explicit fixtures.
for (const key of [
  "CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "HERMES_HOME",
  "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT",
  "PI_CODING_AGENT_DIR", "CAVEMAN_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
  // An agent endpoint exported in the shell is the user's own: enable leaves
  // that agent as is.
  "ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "GOOGLE_GEMINI_BASE_URL", "GEMINI_BASE_URL",
]) delete process.env[key];
