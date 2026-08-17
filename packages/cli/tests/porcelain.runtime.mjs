import { test } from "node:test";
import assert from "node:assert";
import { createServer } from "node:http";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

const HELP = `caveman

run
  caveman <agent>        run an agent on the layer  (aider | claude | codex | gemini | hermes | openclaw | opencode)
  caveman run -- <cmd>   run anything else on the layer

understand
  caveman learn          short setup score + top moves
  caveman status         what the layer did today

connect
  caveman login          free · 1 seat · no card

more
  caveman tools          local, no account   ·  caveman help tools
  caveman cloud          connected           ·  caveman help cloud
`;

test("bare caveman and --help render byte-equal compact fixture", async () => {
  const isolated = isolatedCliEnv();
  try {
    const bare = await runCli([], { env: isolated.env });
    const flag = await runCli(["--help"], { env: isolated.env });
    assert.equal(bare.code, 0, bare.stderr);
    assert.equal(flag.code, 0, flag.stderr);
    assert.equal(bare.stdout, HELP);
    assert.equal(flag.stdout, HELP);
  } finally {
    isolated.cleanup();
  }
});

test("missing required binary adds only setup install pointer", async () => {
  const isolated = isolatedCliEnv({ CAVEMAN_PROXY_BIN: "/definitely/missing/caveman-proxy" });
  try {
    const out = await runCli(["--help"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stdout, `${HELP}caveman setup --install   install the missing binaries\n`);
  } finally {
    isolated.cleanup();
  }
});

test("tools and cloud discovery keep named product order, tiering, and caps", async () => {
  const isolated = isolatedCliEnv();
  try {
    const tools = await runCli(["tools"], { env: isolated.env });
    const toolsAll = await runCli(["help", "tools", "--all"], { env: isolated.env });
    const cloud = await runCli(["help", "cloud"], { env: isolated.env });
    assert.equal(tools.code, 0, tools.stderr);
    assert.equal(toolsAll.code, 0, toolsAll.stderr);
    assert.equal(cloud.code, 0, cloud.stderr);
    assert.ok(tools.stdout.indexOf("think") < tools.stdout.indexOf("remember"));
    assert.ok(tools.stdout.indexOf("remember") < tools.stdout.indexOf("execute"));
    assert.ok(tools.stdout.indexOf("execute") < tools.stdout.indexOf("inspect"));
    assert.ok(cloud.stdout.indexOf("account") < cloud.stdout.indexOf("evidence"));
    assert.ok(cloud.stdout.indexOf("evidence") < cloud.stdout.indexOf("governance"));
    const defaultToolCount = tools.stdout.split("\n").filter((line) => /^\s{2}[a-z]/.test(line)).length;
    const allToolCount = toolsAll.stdout.split("\n").filter((line) => /^\s{2}[a-z]/.test(line)).length;
    const cloudCount = cloud.stdout.split("\n").filter((line) => /^\s{2}[a-z]/.test(line)).length;
    assert.equal(defaultToolCount, 15);
    assert.ok(defaultToolCount <= 15, "default tools cap");
    assert.equal(allToolCount, 15);
    assert.ok(allToolCount <= 15, "all-tools cap");
    assert.equal(cloudCount, 14);
    assert.ok(cloudCount <= 15, "cloud cap");
    assert.doesNotMatch(tools.stdout, /shrink-hook|practices|check/);
    assert.doesNotMatch(tools.stdout, /caveman help tools --all/);
    assert.doesNotMatch(toolsAll.stdout, /shrink-hook|practices|check/);
  } finally {
    isolated.cleanup();
  }
});

test("namespace --help stays inside its namespace", async () => {
  const isolated = isolatedCliEnv();
  try {
    const tools = await runCli(["tools", "--help"], { env: isolated.env });
    const cloud = await runCli(["cloud", "-h"], { env: isolated.env });
    assert.equal(tools.code, 0, tools.stderr);
    assert.equal(cloud.code, 0, cloud.stderr);
    assert.match(tools.stdout, /^caveman tools · local, no account/m);
    assert.match(cloud.stdout, /^caveman cloud · connected, login required/m);
  } finally {
    isolated.cleanup();
  }
});

test("help learn explains summary, detail, and agent implementation paths", async () => {
  const isolated = isolatedCliEnv();
  try {
    const out = await runCli(["help", "learn"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /default\s+interactive setup score \+ grouped top moves/);
    assert.match(out.stdout, /--plain\s+compact text; no animation or keyboard menu/);
    assert.match(out.stdout, /--all\s+every finding/);
    assert.match(out.stdout, /implement\s+open Claude Code or Codex/);
  } finally {
    isolated.cleanup();
  }
});

test("mcp usage echoes invoked namespace while legacy output stays legacy", async () => {
  const isolated = isolatedCliEnv();
  try {
    const legacy = await runCli(["mcp"], { env: isolated.env });
    const grouped = await runCli(["mcp"], { env: isolated.env, prefix: "tools" });
    assert.equal(legacy.code, 2);
    assert.equal(grouped.code, 2);
    assert.match(legacy.stderr, /^usage: caveman mcp install\|uninstall \[agent\]/);
    assert.match(grouped.stderr, /^usage: caveman tools mcp install\|uninstall \[agent\]/);
    assert.equal(
      grouped.stderr.replaceAll("caveman tools mcp", "caveman mcp"),
      legacy.stderr,
      "namespace must change only invoked prefix",
    );
  } finally {
    isolated.cleanup();
  }
});

test("namespaced telemetry emits resolved command and subcommand", async () => {
  const posts = [];
  const sockets = new Set();
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      posts.push(JSON.parse(body));
      res.writeHead(202, { "content-type": "application/json" });
      res.end("{}");
    });
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const port = server.address().port;
  const isolated = isolatedCliEnv({
    CAVEMAN_TELEMETRY: "1",
    CAVEMAN_TELEMETRY_URL: `http://127.0.0.1:${port}/telemetry`,
  });
  try {
    const out = await runCli(["sdk", "snippet"], { env: isolated.env, prefix: "tools" });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(posts.length, 1);
    assert.equal(posts[0][0].command, "sdk");
    assert.equal(posts[0][0].subcommand, "snippet");
  } finally {
    isolated.cleanup();
    for (const socket of sockets) socket.destroy();
    await new Promise((resolve) => server.close(resolve));
  }
});

test("unknown typo, wrong namespace, and binary each get distinct guidance", async () => {
  const isolated = isolatedCliEnv();
  try {
    const typo = await runCli(["shrnk"], { env: isolated.env });
    assert.equal(typo.code, 2);
    assert.match(typo.stderr, /^unknown command "shrnk" — did you mean `caveman tools shrink`\?\nsee: caveman --help\n$/);

    const wrongGroup = await runCli(["compress"], { env: isolated.env, prefix: "cloud" });
    assert.equal(wrongGroup.code, 2);
    assert.match(wrongGroup.stderr, /^unknown command "compress" — did you mean `caveman tools compress`\?\nsee: caveman --help\n$/);

    const binary = await runCli([process.execPath], { env: isolated.env });
    assert.equal(binary.code, 2);
    assert.equal(binary.stderr, "not a known agent — use `caveman run -- <cmd>`\n");
  } finally {
    isolated.cleanup();
  }
});
