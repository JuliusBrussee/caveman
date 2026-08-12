import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("billing preserves invoice currency while savings remain USD", async () => {
  const account = {
    gainshare_bps: 725,
    mtd_fee_cents: 1234,
    next_invoice_estimate_cents: 2468,
    currency: "eur",
    connected: true,
    status: "active",
    billing_enabled: true,
  };
  const server = createServer((req, res) => {
    res.writeHead(200, { "content-type": "application/json" });
    if (req.url === "/api/v1/billing/account") return res.end(JSON.stringify(account));
    if (req.url?.startsWith("/api/v1/billing/charges")) {
      return res.end(JSON.stringify({ charges: [{ day: "2026-07-10", fee_cents: 123, gross_savings_usd: 10.5, status: "reconciled", receipt_hashes: ["sha256:x"] }] }));
    }
    res.statusCode = 404;
    res.end("{}");
  });
  const port = await new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));
  const env = {
    ...process.env,
    HOME: mkdtempSync(join(tmpdir(), "cave-billing-")),
    CAVE_TOKEN: "ci-token",
    CAVE_API_URL: `http://127.0.0.1:${port}`,
  };

  try {
    const status = await runCli(["billing", "status"], env);
    assert.equal(status.code, 0, status.stderr);
    assert.match(status.stdout, /7\.25% of verified savings/);
    assert.match(status.stdout, /€12\.34/);
    assert.doesNotMatch(status.stdout, /\$12\.34 EUR/);

    const charges = await runCli(["billing", "charges"], env);
    assert.equal(charges.code, 0, charges.stderr);
    assert.match(charges.stdout, /FEE DELTA \(EUR\).*SAVINGS \(USD\)/);
    assert.match(charges.stdout, /€1\.23/);
    assert.match(charges.stdout, /\$10\.50/);
    assert.match(charges.stdout, /1 linked/);
  } finally {
    server.close();
  }
});
