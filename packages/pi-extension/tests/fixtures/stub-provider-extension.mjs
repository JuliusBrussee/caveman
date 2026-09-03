// Registers four test providers.
//
// The gated arms ("openai" and "opencode-go") declare the real upstream hosts,
// because the extension routes a provider only when its base URL points at the
// host that the proxy sends to. With an open gate, the extension re-points them
// at the stub gateway, so a passing run never leaves the machine. If routing
// regresses, the dummy key reaches the real host and the request fails with a
// 401 error or a DNS error.
//
// The loopback arm ("anthropic") points at a dead local port. The closed-gate
// tests use it, so direct traffic fast-fails locally. The open-gate test for a
// custom endpoint uses it too: the extension must keep it direct.
export default function (pi) {
  const model = (id, name) => ({
    id,
    name,
    reasoning: false,
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 32768,
    maxTokens: 1024,
  });
  pi.registerProvider("openai", {
    name: "Stub OpenAI",
    baseUrl: "https://api.openai.com/v1",
    apiKey: "dummy-key-for-stub",
    api: "openai-completions",
    models: [model("stub-model", "Stub Model")],
  });
  pi.registerProvider("opencode-go", {
    name: "Stub OpenCode Go",
    baseUrl: "https://opencode.ai/zen/go/v1",
    apiKey: "dummy-key-for-stub",
    api: "openai-completions",
    models: [model("stub-go-model", "Stub Go Model")],
  });
  // Two custom-named providers on the same dead loopback endpoint. Only
  // "stub-relay" is published as a compat mount by the run-state fixture, so it
  // must route through /w/pi/compat/stub-relay; "unlisted-relay" must not.
  pi.registerProvider("stub-relay", {
    name: "Stub Relay",
    baseUrl: "http://127.0.0.1:1/v1",
    apiKey: "dummy-key-for-stub",
    api: "openai-completions",
    models: [model("stub-relay-model", "Stub Relay Model")],
  });
  pi.registerProvider("unlisted-relay", {
    name: "Stub Unlisted Relay",
    baseUrl: "http://127.0.0.1:1/v1",
    apiKey: "dummy-key-for-stub",
    api: "openai-completions",
    models: [model("stub-unlisted-model", "Stub Unlisted Model")],
  });
  pi.registerProvider("anthropic", {
    name: "Stub Local Anthropic",
    baseUrl: "http://127.0.0.1:1",
    apiKey: "dummy-key-for-stub",
    api: "anthropic-messages",
    models: [model("stub-local", "Stub Local Model")],
  });
}
