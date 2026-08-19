// Registers a loopback test provider so integration runs never leave the
// machine: direct (ungated) traffic fast-fails on a dead local port, and gated
// traffic goes wherever the caveman extension routes it.
export default function (pi) {
  pi.registerProvider("stubprov", {
    name: "Stub Provider",
    baseUrl: "http://127.0.0.1:1",
    apiKey: "dummy-key-for-stub",
    api: "openai-completions",
    models: [{
      id: "stub-model",
      name: "Stub Model",
      reasoning: false,
      input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow: 32768,
      maxTokens: 1024,
    }],
  });
}
