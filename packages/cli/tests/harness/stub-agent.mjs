import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export function stubAgent({ dir, name = "claude" }) {
  if (!dir) throw new Error("stubAgent requires dir");
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const path = join(dir, name);
  writeFileSync(
    path,
    `#!/usr/bin/env node
const baseURL = process.env.ANTHROPIC_BASE_URL;
if (!baseURL) {
  process.stderr.write("stub-agent: ANTHROPIC_BASE_URL is required\\n");
  process.exit(2);
}
try {
  const response = await fetch(baseURL.replace(/\\/+$/, "") + "/v1/messages", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "anthropic-version": "2023-06-01",
      "x-api-key": process.env.ANTHROPIC_API_KEY || "stub-key",
    },
    body: JSON.stringify({
      model: "claude-sonnet-4-6",
      max_tokens: 16,
      messages: [{ role: "user", content: "Reply with ok." }],
    }),
  });
  const body = await response.json();
  if (!response.ok) throw new Error("upstream HTTP " + response.status);
  const text = body?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error("missing Anthropic text block");
  process.stdout.write("stub-agent: " + text + "\\n");
} catch (error) {
  process.stderr.write("stub-agent: " + error.message + "\\n");
  process.exit(1);
}
`,
    { mode: 0o755 },
  );
  return { dir, name, path };
}
