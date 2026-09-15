import assert from "node:assert/strict";
import test from "node:test";
import {
  gatewayHostPort,
  loginBrowserOpener,
  resolveLoginBaseUrl,
  resolveConfigBaseUrl,
  shouldOpenLoginBrowser,
} from "../dist/index.js";

test("gateway URL default ports follow protocol", () => {
  assert.deepEqual(gatewayHostPort("https://gateway.example"), { host: "gateway.example", port: 443 });
  assert.deepEqual(gatewayHostPort("http://gateway.example"), { host: "gateway.example", port: 80 });
  assert.deepEqual(gatewayHostPort("https://gateway.example:9443"), { host: "gateway.example", port: 9443 });
});

test("interactive login uses platform browser opener unless explicitly disabled", () => {
  const url = "https://caveman.so/device?code=ABCD";
  assert.deepEqual(loginBrowserOpener(url, "darwin"), { command: "open", args: [url] });
  assert.deepEqual(loginBrowserOpener(url, "linux"), { command: "xdg-open", args: [url] });
  assert.deepEqual(loginBrowserOpener(url, "win32"), { command: "rundll32.exe", args: ["url.dll,FileProtocolHandler", url] });
  assert.equal(shouldOpenLoginBrowser(false, true), true);
  assert.equal(shouldOpenLoginBrowser(true, true), false);
  assert.equal(shouldOpenLoginBrowser(false, false), false);
});

// caveman login is the funnel's wow moment (syncAfterLogin backfills locally
// recorded spans into the dashboard). Its default must point at the deployed
// control API, not a dev-only port nothing is listening on for a new developer
// who hasn't set CAVE_API_URL.
test("login base URL defaults to the production control API", () => {
  const prevEnv = process.env.CAVE_API_URL;
  delete process.env.CAVE_API_URL;
  try {
    assert.equal(resolveLoginBaseUrl([]), "https://api.caveman.so");
  } finally {
    if (prevEnv !== undefined) process.env.CAVE_API_URL = prevEnv;
  }
});

test("CAVE_API_URL overrides the production default", () => {
  const prevEnv = process.env.CAVE_API_URL;
  process.env.CAVE_API_URL = "http://localhost:8080";
  try {
    assert.equal(resolveLoginBaseUrl([]), "http://localhost:8080");
  } finally {
    if (prevEnv !== undefined) process.env.CAVE_API_URL = prevEnv;
    else delete process.env.CAVE_API_URL;
  }
});

test("--base-url overrides both CAVE_API_URL and the production default", () => {
  const prevEnv = process.env.CAVE_API_URL;
  process.env.CAVE_API_URL = "http://localhost:8080";
  try {
    assert.equal(
      resolveLoginBaseUrl(["--base-url", "http://127.0.0.1:9999"]),
      "http://127.0.0.1:9999",
    );
  } finally {
    if (prevEnv !== undefined) process.env.CAVE_API_URL = prevEnv;
    else delete process.env.CAVE_API_URL;
  }
});

// C8 review finding 6: config() (the fallback every connected verb uses when
// no config.json exists yet, e.g. CAVE_TOKEN-only CI runs) had its own
// separate localhost:8080 literal with no CAVE_API_URL precedence, so it
// could disagree with what caveman login had just resolved and persisted.
// resolveConfigBaseUrl mirrors resolveLoginBaseUrl's precedence minus the
// flag tier (this call site has no argv).
test("config base URL defaults to the production control API", () => {
  const prevEnv = process.env.CAVE_API_URL;
  delete process.env.CAVE_API_URL;
  try {
    assert.equal(resolveConfigBaseUrl(undefined), "https://api.caveman.so");
  } finally {
    if (prevEnv !== undefined) process.env.CAVE_API_URL = prevEnv;
  }
});

test("config base URL: CAVE_API_URL overrides the production default", () => {
  const prevEnv = process.env.CAVE_API_URL;
  process.env.CAVE_API_URL = "http://localhost:8080";
  try {
    assert.equal(resolveConfigBaseUrl(undefined), "http://localhost:8080");
  } finally {
    if (prevEnv !== undefined) process.env.CAVE_API_URL = prevEnv;
    else delete process.env.CAVE_API_URL;
  }
});

test("config base URL: a saved config.json value overrides CAVE_API_URL and the default", () => {
  const prevEnv = process.env.CAVE_API_URL;
  process.env.CAVE_API_URL = "http://localhost:8080";
  try {
    assert.equal(resolveConfigBaseUrl("http://127.0.0.1:9999"), "http://127.0.0.1:9999");
  } finally {
    if (prevEnv !== undefined) process.env.CAVE_API_URL = prevEnv;
    else delete process.env.CAVE_API_URL;
  }
});
