// All endpoints stay on loopback even if routing fails. Native/custom identities
// match the listener maps in integration.runtime.mjs; no fixture needs a public
// API request, key, or model response.
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
    baseUrl: "http://127.0.0.1:1/native-openai/v1",
    apiKey: "dummy-key-for-stub",
    api: "openai-completions",
    models: [model("stub-model", "Stub Model")],
  });
  pi.registerProvider("opencode-go", {
    name: "Stub OpenCode Go",
    baseUrl: "http://127.0.0.1:1/tenant-go/v1",
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
