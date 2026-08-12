import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cliModule = await import(pathToFileURL(join(here, "..", "dist", "index.js")).href);
const { readJson5Lenient, deepMerge, buildWrapEnv, overlayBuilders } = cliModule;
const { PROFILES } = await import(pathToFileURL(join(here, "..", "dist", "agents.generated.js")).href);

function attributed(gw, id) {
  return `${gw.replace(/\/+$/, "")}/w/${id}`;
}

function profile(id) {
  const found = PROFILES.find((p) => p.id === id);
  assert.ok(found, `missing generated profile ${id}`);
  return found;
}

function fakeProfile(id, injection) {
  return {
    schema_version: "1",
    id,
    display_name: id,
    vendor: "test",
    homepage: "https://example.com",
    binary_names: [id],
    args: [],
    install: "test",
    wire_protocol: "openai-chat",
    injection,
    fallback: "generic-env",
    maintainer: null,
  };
}

function withEnv(patch, fn) {
  const old = new Map();
  for (const [k, v] of Object.entries(patch)) {
    old.set(k, process.env[k]);
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  try {
    return fn();
  } finally {
    for (const [k, v] of old.entries()) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
}

function captureStderr(fn) {
  const oldWrite = process.stderr.write;
  let stderr = "";
  process.stderr.write = (chunk, enc, cb) => {
    stderr += String(chunk);
    if (typeof enc === "function") enc();
    if (typeof cb === "function") cb();
    return true;
  };
  try {
    return { result: fn(), stderr };
  } finally {
    process.stderr.write = oldWrite;
  }
}

function runNode(env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ["-e", "process.stdout.write(process.env.OPENAI_BASE_URL || '')"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("readJson5Lenient strips comments and trailing commas without touching string content", () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-json5-"));
  const file = join(dir, "config.json");
  writeFileSync(file, `{
    "url": "https://example.com//kept",
    "block": "literal /* kept */ text",
    // line comment
    "items": [1, 2,],
    "nested": { "ok": true, },
    /* block
       comment */
    "after": "yes",
  }`);

  assert.deepEqual(readJson5Lenient(file), {
    url: "https://example.com//kept",
    block: "literal /* kept */ text",
    items: [1, 2],
    nested: { ok: true },
    after: "yes",
  });
});

test("deepMerge recursively merges plain objects and replaces arrays and scalars", () => {
  assert.deepEqual(
    deepMerge(
      { obj: { keep: 1, replace: 2 }, arr: [1], scalar: "base", keepRoot: true },
      { obj: { replace: 9, add: 3 }, arr: [2], scalar: { now: "object" }, nil: null },
    ),
    { obj: { keep: 1, replace: 9, add: 3 }, arr: [2], scalar: { now: "object" }, keepRoot: true, nil: null },
  );
});

test("config-file injection merges a rendered overlay over a temp base config", () => withEnv({
  FAKE_CONFIG_PATH_E2E: undefined,
  FAKE_BASE_CONFIG_E2E: undefined,
}, () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-config-file-"));
  const base = join(dir, "base.json");
  writeFileSync(base, `{
    "service": { "baseUrl": "old", "keep": "yes" },
    "list": [1],
  }`);
  process.env.FAKE_BASE_CONFIG_E2E = base;
  const agent = fakeProfile("fake-config-e2e", {
    method: "config-file",
    env_var: "FAKE_CONFIG_PATH_E2E",
    base_config: { path: join(dir, "missing.json"), env_var: "FAKE_BASE_CONFIG_E2E" },
    config_overlay: {
      local: {
        service: { baseUrl: "{{cave_base_url}}", added: true },
        list: [2],
      },
    },
  });

  const env = buildWrapEnv(agent, "http://127.0.0.1:19000");
  assert.match(dirname(env.FAKE_CONFIG_PATH_E2E), /caveman-wrap-/);
  assert.equal(basename(env.FAKE_CONFIG_PATH_E2E), `${agent.id}.json`);
  assert.equal(statSync(env.FAKE_CONFIG_PATH_E2E).mode & 0o777, 0o600);
  assert.equal(env.OPENAI_BASE_URL, attributed("http://127.0.0.1:19000", agent.id));
  const cfg = JSON.parse(readFileSync(env.FAKE_CONFIG_PATH_E2E, "utf8"));
  assert.deepEqual(cfg, {
    service: { baseUrl: attributed("http://127.0.0.1:19000", agent.id), keep: "yes", added: true },
    list: [2],
  });
}));

test("config-file injection lets overlay builders replace the static overlay", () => {
  const id = "fake-config-builder";
  const agent = fakeProfile(id, {
    method: "config-file",
    env_var: "FAKE_CONFIG_PATH_BUILDER",
    base_config: { path: join(tmpdir(), "caveman-missing-builder.json") },
    config_overlay: { local: { static: true } },
  });
  overlayBuilders[id] = () => ({ fromBuilder: "{{cave_base_url}}" });
  try {
    const env = buildWrapEnv(agent, "http://127.0.0.1:19001");
    const cfg = JSON.parse(readFileSync(env.FAKE_CONFIG_PATH_BUILDER, "utf8"));
    assert.deepEqual(cfg, { fromBuilder: attributed("http://127.0.0.1:19001", id) });
  } finally {
    delete overlayBuilders[id];
  }
});

test("config-file injection expands tilde in base_config path", () => {
  const home = mkdtempSync(join(tmpdir(), "cave-config-home-"));
  writeFileSync(join(home, "base.json"), JSON.stringify({ nested: { keep: true } }));
  return withEnv({
    HOME: home,
    FAKE_CONFIG_PATH_TILDE: undefined,
  }, () => {
    const agent = fakeProfile("fake-config-tilde", {
      method: "config-file",
      env_var: "FAKE_CONFIG_PATH_TILDE",
      base_config: { path: "~/base.json" },
      config_overlay: { local: { nested: { added: "{{cave_base_url}}" } } },
    });

    const env = buildWrapEnv(agent, "http://127.0.0.1:19004");
    const cfg = JSON.parse(readFileSync(env.FAKE_CONFIG_PATH_TILDE, "utf8"));
    assert.deepEqual(cfg, { nested: { keep: true, added: attributed("http://127.0.0.1:19004", agent.id) } });
  });
});

test("config-file injection fails open on corrupt base config and still launches", async () => withEnv({
  FAKE_CONFIG_PATH_BAD: undefined,
}, async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-config-bad-"));
  const bad = join(dir, "bad.json");
  writeFileSync(bad, "{ bad json");
  const agent = fakeProfile("fake-config-bad", {
    method: "config-file",
    env_var: "FAKE_CONFIG_PATH_BAD",
    base_config: { path: bad },
    config_overlay: { local: { never: "written" } },
  });

  const { result: env, stderr } = captureStderr(() => buildWrapEnv(agent, "http://127.0.0.1:19002"));
  assert.equal(env.FAKE_CONFIG_PATH_BAD, undefined);
  assert.equal(env.ANTHROPIC_BASE_URL, attributed("http://127.0.0.1:19002", agent.id));
  assert.match(stderr, /^caveman: fake-config-bad config-file injection failed;/);

  const launched = await runNode(env);
  assert.equal(launched.code, 0, launched.stderr);
  assert.equal(launched.stdout, attributed("http://127.0.0.1:19002", agent.id));
}));

test("config-file injection fails open when an overlay builder throws", () => {
  const id = "fake-config-builder-bad";
  const agent = fakeProfile(id, {
    method: "config-file",
    env_var: "FAKE_CONFIG_PATH_BUILDER_BAD",
    base_config: { path: join(tmpdir(), "caveman-missing-builder-bad.json") },
    config_overlay: { local: { never: "used" } },
  });
  overlayBuilders[id] = () => {
    throw new Error("builder broke");
  };
  try {
    const { result: env, stderr } = captureStderr(() => buildWrapEnv(agent, "http://127.0.0.1:19003"));
    assert.equal(env.FAKE_CONFIG_PATH_BUILDER_BAD, undefined);
    assert.equal(env.OPENAI_BASE_URL, attributed("http://127.0.0.1:19003", id));
    assert.match(stderr, /^caveman: fake-config-builder-bad config-file injection failed;/);
  } finally {
    delete overlayBuilders[id];
  }
});

test("config-file injection selects local vs managed overlays by gateway mode", () => {
  const agent = fakeProfile("fake-config-mode", {
    method: "config-file",
    env_var: "FAKE_CONFIG_PATH_MODE",
    base_config: { path: join(tmpdir(), "caveman-missing-mode.json") },
    config_overlay: {
      local: { mode: "local", url: "{{cave_base_url}}" },
      managed: { mode: "managed", url: "{{cave_base_url}}" },
    },
  });

  const localEnv = buildWrapEnv(agent, "http://localhost:8787");
  const localCfg = JSON.parse(readFileSync(localEnv.FAKE_CONFIG_PATH_MODE, "utf8"));
  assert.deepEqual(localCfg, { mode: "local", url: attributed("http://localhost:8787", agent.id) });

  const managedEnv = buildWrapEnv(agent, "https://gateway.example.com");
  const managedCfg = JSON.parse(readFileSync(managedEnv.FAKE_CONFIG_PATH_MODE, "utf8"));
  assert.deepEqual(managedCfg, { mode: "managed", url: attributed("https://gateway.example.com", agent.id) });
});

test("openclaw config-file injection keeps attribution header and uses path-attributed gateway", () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-openclaw-config-file-"));
  const home = mkdtempSync(join(tmpdir(), "cave-openclaw-home-"));
  const caveHome = mkdtempSync(join(tmpdir(), "cave-openclaw-dot-"));
  const base = join(dir, "openclaw.json");
  writeFileSync(base, JSON.stringify({
    agents: { defaults: { model: { primary: "myprov/gpt-test" } } },
    models: {
      providers: {
        myprov: {
          apiKey: "${MYPROV_API_KEY}",
          api: "openai-completions",
          models: [{ id: "gpt-test", name: "GPT Test" }],
        },
      },
    },
  }));

  return withEnv({
    HOME: home,
    CAVEMAN_HOME: caveHome,
    OPENCLAW_CONFIG_PATH: base,
    MYPROV_API_KEY: "sk-openclaw",
  }, () => {
    const agent = profile("openclaw");
    const env = buildWrapEnv(agent, "http://127.0.0.1:19006");
    const expectedGateway = attributed("http://127.0.0.1:19006", "openclaw");
    assert.equal(env.OPENAI_BASE_URL, expectedGateway);
    assert.equal(env.OPENAI_API_BASE, expectedGateway);
    assert.equal(env.GEMINI_BASE_URL, undefined);
    assert.equal(env.GOOGLE_GEMINI_BASE_URL, expectedGateway);

    const cfg = JSON.parse(readFileSync(env.OPENCLAW_CONFIG_PATH, "utf8"));
    assert.equal(cfg.models.providers.caveman.baseUrl, `${expectedGateway}/v1`);
    assert.equal(cfg.models.providers.caveman.headers["x-cave-agent"], "openclaw");
    assert.equal(cfg.models.providers.caveman.apiKey, "sk-openclaw");
  });
});
