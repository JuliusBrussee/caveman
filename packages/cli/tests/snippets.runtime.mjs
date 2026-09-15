import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const { RECIPES } = await import(pathToFileURL(join(here, "..", "dist", "recipes.generated.js")).href);

function run(args, env = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...args], {
      env: {
        ...process.env,
        NO_COLOR: "1",
        HOME: mkdtempSync(join(tmpdir(), "cave-snippets-home-")),
        CAVEMAN_HOME: mkdtempSync(join(tmpdir(), "cave-snippets-cave-")),
        ...env,
      },
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("snippets list mode shows every recipe id", async () => {
  assert.equal(RECIPES.length, 12, "registry fixture should contain 12 recipes");
  const out = await run(["snippets"]);
  assert.equal(out.code, 0, out.stderr);
  for (const recipe of RECIPES) {
    assert.match(out.stdout, new RegExp(`(^|\\n)${recipe.id}\\s+${recipe.display_name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
  }
  assert.match(out.stdout, /usage: caveman snippets <id> \[--app <slug>\]/);
});

test("snippets renders selected recipe with gateway and app templates resolved", async () => {
  const out = await run(["snippets", "openai-py", "--app", "checkout"], {
    CAVE_GATEWAY_URL: "https://gateway.example.test",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /# One constructor argument/);
  assert.match(out.stdout, /\/w\/checkout\/openai\/v1/);
  assert.doesNotMatch(out.stdout, /\{\{/);
});

test("unknown snippet recipe fails closed and lists valid ids", async () => {
  const out = await run(["snippets", "not-a-recipe"]);
  assert.notEqual(out.code, 0, "unknown recipe must exit non-zero");
  assert.match(out.stderr, /unknown snippet recipe: not-a-recipe/);
  for (const recipe of RECIPES) assert.match(out.stderr, new RegExp(recipe.id));
});

test("every snippet recipe renders without unresolved template markers", async () => {
  for (const recipe of RECIPES) {
    const out = await run(["snippets", recipe.id, "--app", "checkout"], {
      CAVE_GATEWAY_URL: "http://127.0.0.1:8787",
    });
    assert.equal(out.code, 0, `${recipe.id}: ${out.stderr}`);
    assert.doesNotMatch(out.stdout, /\{\{/, `${recipe.id} left a template marker unresolved`);
  }
});

test("sdk snippet keeps legacy output and points to registry snippets", async () => {
  const out = await run(["sdk", "snippet"]);
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /OpenAI baseURL: \$CAVE_GATEWAY_URL\/openai\/v1/);
  assert.match(out.stdout, /TypeScript SDK: import \{ Cave \} from "@caveman-ai\/sdk"/);
  assert.match(out.stdout, /caveman snippets openai-ts --app my-service/);
});
