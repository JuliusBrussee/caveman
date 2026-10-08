import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const reporter = join(root, "agents", "drift-report.mjs");
const registry = JSON.parse(readFileSync(join(root, "agents", "agents.json"), "utf8"));
const profile = registry.agents.find((candidate) => candidate.id === "kilo");
if (!profile) throw new Error("kilo profile missing from registry fixture");

function nextPatch(version) {
  const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(version);
  if (!match) throw new Error(`test requires a plain semver profile pin, got ${version}`);
  return `${match[1]}.${match[2]}.${Number(match[3]) + 1}`;
}

function validDrift() {
  return {
    schema: "caveman.agent-probe.v1",
    results: registry.agents.map((candidate) => candidate.id === profile.id
      ? {
          id: candidate.id,
          status: "drift",
          binary: "/tmp/kilo",
          tested: candidate.tested_agent_version,
          observed: nextPatch(candidate.tested_agent_version),
          version_ok: true,
          help_ok: true,
          version_matches: false,
        }
      : {
          id: candidate.id,
          status: "not-installed",
          tested: candidate.tested_agent_version,
        }),
  };
}

function resultFor(artifact, id = profile.id) {
  const result = artifact.results.find((candidate) => candidate.id === id);
  if (!result) throw new Error(`artifact result missing for ${id}`);
  return result;
}

function runReporter(t, artifact, { expectedId = profile.id, inputBasename = `${expectedId}.json`, includeExpectedId = true, registryOverride, openIssues = [] } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "caveman-drift-report-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  // The reporter reads agents.json from its own directory. A test that needs a
  // registry shape the live one may not have (a prerelease pin) runs a copy of
  // the reporter next to a synthesized registry instead of depending on live data.
  let reporterPath = reporter;
  if (registryOverride) {
    const agentsDir = join(dir, "agents");
    mkdirSync(agentsDir);
    writeFileSync(join(agentsDir, "drift-report.mjs"), readFileSync(reporter));
    writeFileSync(join(agentsDir, "version.mjs"), readFileSync(join(root, "agents", "version.mjs")));
    writeFileSync(join(agentsDir, "agents.json"), JSON.stringify(registryOverride));
    reporterPath = join(agentsDir, "drift-report.mjs");
  }
  const input = join(dir, inputBasename);
  const calls = join(dir, "gh-called");
  const issues = join(dir, "gh-issues.json");
  const bin = join(dir, "bin");
  mkdirSync(bin);
  writeFileSync(input, JSON.stringify(artifact));
  writeFileSync(issues, JSON.stringify(openIssues));
  const gh = join(bin, "gh");
  writeFileSync(gh, `#!/bin/sh\nprintf '%s\\n' "$*" >> "$GH_CALLED_FILE"\nif [ "$1 $2" = "issue list" ]; then cat "$GH_ISSUES_FILE"; fi\n`);
  chmodSync(gh, 0o755);
  const argv = [reporterPath, "--input", input];
  if (includeExpectedId) argv.push("--expected-id", expectedId);
  const result = spawnSync(process.execPath, argv, {
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${bin}${delimiter}${process.env.PATH || ""}`,
      GH_CALLED_FILE: calls,
      GH_ISSUES_FILE: issues,
    },
  });
  return {
    ...result,
    ghCalls: existsSync(calls) ? readFileSync(calls, "utf8") : "",
  };
}

test("validated drift artifact may call gh only after validation", { skip: process.platform === "win32" && "gh stub is a sh script; the reporter runs on POSIX CI only" }, (t) => {
  const result = runReporter(t, validDrift());
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.ghCalls, /^issue list/m);
  assert.match(result.ghCalls, /^issue create/m);
});

test("complete matching non-drift artifact performs no gh operation", (t) => {
  const artifact = validDrift();
  const expected = resultFor(artifact);
  expected.status = "ok";
  expected.observed = expected.tested;
  expected.version_matches = true;
  const result = runReporter(t, artifact);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /no drift observed/);
  assert.equal(result.ghCalls, "");
});

test("release version outranks matching prerelease", { skip: process.platform === "win32" && "gh stub is a sh script; the reporter runs on POSIX CI only" }, (t) => {
  // Synthesize the prerelease pin on kilo rather than hunting the live registry
  // for one: whether any shipped pin is a prerelease changes with every drift bump.
  const prereleasePin = `${profile.tested_agent_version}-rc.1`;
  const registryOverride = structuredClone(registry);
  const prereleaseProfile = registryOverride.agents.find((candidate) => candidate.id === profile.id);
  prereleaseProfile.tested_agent_version = prereleasePin;
  const artifact = validDrift();
  const expected = resultFor(artifact, prereleaseProfile.id);
  Object.assign(expected, {
    status: "drift",
    binary: `/tmp/${prereleaseProfile.id}`,
    tested: prereleasePin,
    observed: profile.tested_agent_version,
    version_ok: true,
    help_ok: true,
    version_matches: false,
  });
  const result = runReporter(t, artifact, { expectedId: prereleaseProfile.id, registryOverride });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.ghCalls, /^issue create/m);
});

test("broken latest probe opens an issue without copying untrusted probe output", { skip: process.platform === "win32" && "gh stub is a sh script; the reporter runs on POSIX CI only" }, (t) => {
  // #1054: hermes@main probed broken for weeks and nothing surfaced, because only
  // `drift` reached gh. A broken @latest is the louder signal, not a quieter one.
  const artifact = validDrift();
  Object.assign(resultFor(artifact), {
    status: "broken",
    help_ok: false,
    version_error: "",
    help_error: "ModuleNotFoundError: No module named 'ruamel' @maintainer",
  });
  const result = runReporter(t, artifact);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.ghCalls, /^issue list/m);
  assert.match(result.ghCalls, /^issue create --title agent-drift: kilo .*fails the latest probe/m);
  assert.doesNotMatch(result.ghCalls, /ruamel|@maintainer/);
});

function brokenAtDriftVersion(artifact) {
  Object.assign(resultFor(artifact), { status: "broken", help_ok: false, version_error: "", help_error: "" });
  return artifact;
}

function openIssue(stateLine, eol = "\n") {
  return {
    number: 7,
    title: `agent-drift: ${profile.id} — earlier report`,
    body: [`<!-- caveman-agent-drift:${profile.id} -->`, "", "Earlier report.", "", stateLine, "- profile `tested_agent_version`: `x`"].join(eol),
  };
}

// The existing-issue branch keys "already current" on the exact state line, so a
// repeat run must not rewrite the issue (that would clobber maintainer edits nightly),
// and a drift that turns broken at the same version must. Bodies written before broken
// reports existed, and bodies GitHub hands back with CRLF after a web-UI edit, still count.
for (const [name, artifact, stateLine, eol, edits] of [
  ["old-format drift body, same version", () => validDrift(), (v) => `- installed (@latest): \`${v}\``, "\n", false],
  ["CRLF drift body, same version", () => validDrift(), (v) => `- installed (@latest): \`${v}\``, "\r\n", false],
  ["broken body, still broken at same version", () => brokenAtDriftVersion(validDrift()), (v) => `- installed (@latest): \`${v}\` (probe broken)`, "\n", false],
  ["drift body, now broken at same version", () => brokenAtDriftVersion(validDrift()), (v) => `- installed (@latest): \`${v}\``, "\n", true],
  ["broken body, now drifted at same version", () => validDrift(), (v) => `- installed (@latest): \`${v}\` (probe broken)`, "\n", true],
]) {
  test(`existing drift issue: ${name}`, { skip: process.platform === "win32" && "gh stub is a sh script; the reporter runs on POSIX CI only" }, (t) => {
    const input = artifact();
    const issue = openIssue(stateLine(resultFor(input).observed), eol);
    const result = runReporter(t, input, { openIssues: [issue] });
    assert.equal(result.status, 0, result.stderr);
    assert.doesNotMatch(result.ghCalls, /^issue create/m);
    if (edits) {
      assert.match(result.ghCalls, /^issue edit 7 /m);
    } else {
      assert.doesNotMatch(result.ghCalls, /^issue edit/m);
      assert.match(result.stdout, /issue #7 already current/);
    }
  });
}

test("hermes latest drift probe installs the latest release, not main", () => {
  // #1054: hermes main moved its deps to Python 3.14-only markers, so @main probed
  // broken on the runner's 3.12 while every tagged release installs cleanly.
  const workflow = readFileSync(join(root, ".github", "workflows", "agent-conformance.yml"), "utf8");
  const line = /- id: hermes\s*\n\s*install_latest:\s*([^\n]+)/.exec(workflow)?.[1] || "";
  assert.match(line, /hermes-agent\/releases\/latest/);
  assert.doesNotMatch(line, /@main\b/);
});

test("workflow passes trusted artifact basename as expected profile id", () => {
  const workflow = readFileSync(join(root, ".github", "workflows", "agent-conformance.yml"), "utf8");
  assert.match(workflow, /expected_id="\$\(basename "\$probe" \.json\)"/);
  assert.match(workflow, /drift-report\.mjs --input "\$probe" --expected-id "\$expected_id"/);
});

const invalidCases = [
  ["wrong top-level schema", () => ({ ...validDrift(), schema: "caveman.agent-probe.v2" }), /top level must contain exactly/],
  ["extra top-level field", () => ({ ...validDrift(), attacker: true }), /top level must contain exactly/],
  ["result count outside exact registry set", () => ({
    schema: "caveman.agent-probe.v1",
    results: [...validDrift().results, structuredClone(validDrift().results[0])],
  }), /results must contain the complete/],
  ["duplicate profile id", () => {
    const artifact = validDrift();
    artifact.results.at(-1).id = artifact.results[0].id;
    return artifact;
  }, /duplicates profile/],
  ["unknown profile id", () => {
    const artifact = validDrift();
    artifact.results.at(-1).id = "unknown-agent";
    return artifact;
  }, /id must name a known profile/],
  ["tested version mismatch", () => {
    const artifact = validDrift();
    resultFor(artifact).tested = "0.0.1";
    return artifact;
  }, /tested must exactly equal registry tested_agent_version/],
  ["unknown status", () => {
    const artifact = validDrift();
    resultFor(artifact).status = "newer";
    return artifact;
  }, /unknown status or fields outside its exact status schema/],
  ["unsafe observed version", () => {
    const artifact = validDrift();
    const expected = resultFor(artifact);
    expected.observed = `${expected.observed}\n--body=forged`;
    return artifact;
  }, /observed must be empty or a strict version/],
  ["non-newer drift version", () => {
    const artifact = validDrift();
    const expected = resultFor(artifact);
    expected.observed = expected.tested;
    expected.version_matches = true;
    return artifact;
  }, /drift status requires a strict observed version newer than tested/],
  ["prerelease does not outrank matching release", () => {
    const artifact = validDrift();
    const expected = resultFor(artifact);
    expected.observed = `${expected.tested}-beta`;
    return artifact;
  }, /drift status requires a strict observed version newer than tested/],
  ["extra result field", () => {
    const artifact = validDrift();
    resultFor(artifact).title = "forged";
    return artifact;
  }, /unknown status or fields outside its exact status schema/],
  ["another lifecycle profile reports drift", () => {
    const artifact = validDrift();
    const otherProfile = registry.agents.find((candidate) => candidate.id !== profile.id && /^\d+\.\d+\.\d+$/.test(candidate.tested_agent_version));
    if (!otherProfile) throw new Error("second semver profile missing from registry fixture");
    const other = resultFor(artifact, otherProfile.id);
    Object.assign(other, {
      status: "drift",
      binary: `/tmp/${otherProfile.id}`,
      observed: nextPatch(otherProfile.tested_agent_version),
      version_ok: true,
      help_ok: true,
      version_matches: false,
    });
    return artifact;
  }, /cannot report drift.*artifact is bound to kilo/],
  ["required expected profile marked not installed", () => {
    const artifact = validDrift();
    const index = artifact.results.findIndex((candidate) => candidate.id === profile.id);
    artifact.results[index] = { id: profile.id, status: "not-installed", tested: profile.tested_agent_version };
    return artifact;
  }, /must carry its required-probe result/],
];

for (const [name, artifact, message] of invalidCases) {
  test(`invalid drift artifact cannot invoke gh: ${name}`, (t) => {
    const result = runReporter(t, artifact());
    assert.equal(result.status, 2, `stdout=${result.stdout}\nstderr=${result.stderr}`);
    assert.match(result.stderr, message);
    assert.equal(result.ghCalls, "", "gh must not run before full artifact validation");
  });
}

test("drift reporter requires trusted expected profile id", (t) => {
  const result = runReporter(t, validDrift(), { includeExpectedId: false, inputBasename: `${profile.id}.json` });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /usage:.*--expected-id/);
  assert.equal(result.ghCalls, "");
});

test("drift reporter binds input basename to expected profile id", (t) => {
  const result = runReporter(t, validDrift(), { inputBasename: "qwen.json" });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /input basename must exactly match expected profile id/);
  assert.equal(result.ghCalls, "");
});

test("drift reporter rejects unknown expected profile id", (t) => {
  const result = runReporter(t, validDrift(), { expectedId: "unknown-agent" });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /--expected-id must name a known profile/);
  assert.equal(result.ghCalls, "");
});
