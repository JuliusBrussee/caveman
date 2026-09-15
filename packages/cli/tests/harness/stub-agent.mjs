import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const SOURCE = `#!/usr/bin/env node
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
`;

export function stubAgent({ dir, name = "claude", platform = process.platform }) {
  if (!dir) throw new Error("stubAgent requires dir");
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  // Windows cannot execute a shebang script: CreateProcess resolves through
  // PATHEXT, so an extensionless file is not spawnable at all (#834). Write the
  // body as .mjs and a .cmd shim under the bare name — which is exactly how a
  // real agent binary presents itself on PATH there, so the product still has
  // to resolve `name` through PATHEXT to find it.
  if (platform === "win32") {
    writeFileSync(join(dir, `${name}.mjs`), SOURCE);
    const path = join(dir, `${name}.cmd`);
    // The backslash after %~dp0 is load-bearing, not cosmetic: parseWindowsNodeShim
    // only recognizes a Node shim in the `"%~dp0\target.mjs" %*` form, so without
    // it portableInvocation throws "non-Node Windows command shim" and every test
    // that spawns through stubAgent fails on Windows. (%~dp0 already ends in a
    // separator; Windows collapses the double.)
    writeFileSync(path, [
      "@echo off",
      `node "%~dp0\\${name}.mjs" %*`,
      "exit /b %errorlevel%",
      "",
    ].join("\r\n"));
    return { dir, name, path };
  }
  const path = join(dir, name);
  writeFileSync(path, SOURCE, { mode: 0o755 });
  return { dir, name, path };
}
