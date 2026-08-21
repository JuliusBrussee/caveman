import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "node:http";
import { once } from "node:events";
import {
  createProxyAwareFetch,
  installProxyAwareFetch,
  resolveProxyUrl,
  shouldBypassProxy,
} from "../dist/proxy-fetch.js";

const PROXY = "http://proxy.internal:912";

function listen(server) {
  server.listen(0, "127.0.0.1");
  return once(server, "listening").then(() => `http://127.0.0.1:${server.address().port}`);
}

/** Proxy that serves absolute-form requests itself and counts what it saw. */
async function startCountingProxy(handler) {
  const seen = [];
  const server = createServer((request, response) => {
    seen.push(request.url);
    handler(request, response);
  });
  const origin = await listen(server);
  return { origin, seen, close: () => server.close() };
}

test("resolveProxyUrl picks the variable matching the target scheme", () => {
  const env = { HTTP_PROXY: "http://plain:912", HTTPS_PROXY: "http://secure:912" };
  assert.equal(resolveProxyUrl(new URL("http://example.test/x"), env).host, "plain:912");
  assert.equal(resolveProxyUrl(new URL("https://example.test/x"), env).host, "secure:912");
});

test("resolveProxyUrl prefers the lower case spelling", () => {
  const env = { http_proxy: "http://lower:912", HTTP_PROXY: "http://upper:912" };
  assert.equal(resolveProxyUrl(new URL("http://example.test"), env).host, "lower:912");
});

test("resolveProxyUrl falls back to ALL_PROXY for either scheme", () => {
  const env = { ALL_PROXY: "http://catch-all:912" };
  assert.equal(resolveProxyUrl(new URL("http://example.test"), env).host, "catch-all:912");
  assert.equal(resolveProxyUrl(new URL("https://example.test"), env).host, "catch-all:912");
});

test("resolveProxyUrl accepts a bare host:port and credentials", () => {
  assert.equal(resolveProxyUrl(new URL("https://example.test"), { HTTPS_PROXY: "proxy.internal:912" }).port, "912");
  const withAuth = resolveProxyUrl(new URL("https://example.test"), { HTTPS_PROXY: "http://user:pass@proxy:912" });
  assert.equal(withAuth.username, "user");
});

test("resolveProxyUrl returns null with nothing configured or for other schemes", () => {
  assert.equal(resolveProxyUrl(new URL("https://example.test"), {}), null);
  assert.equal(resolveProxyUrl(new URL("ftp://example.test"), { ALL_PROXY: PROXY }), null);
});

test("shouldBypassProxy honours exact hosts, domain suffixes, ports, and the wildcard", () => {
  const target = new URL("https://api.example.test/v1");
  assert.equal(shouldBypassProxy(target, "api.example.test"), true);
  assert.equal(shouldBypassProxy(target, ".example.test"), true);
  assert.equal(shouldBypassProxy(target, "example.test"), true);
  assert.equal(shouldBypassProxy(target, "*"), true);
  assert.equal(shouldBypassProxy(target, "other.test, API.EXAMPLE.TEST"), true);
  assert.equal(shouldBypassProxy(target, "api.example.test:443"), true);

  assert.equal(shouldBypassProxy(target, "api.example.test:8443"), false);
  assert.equal(shouldBypassProxy(target, "notexample.test"), false);
  assert.equal(shouldBypassProxy(target, ""), false);
  assert.equal(shouldBypassProxy(new URL("https://example.test.evil.test"), ".example.test"), false);
});

test("proxied requests reach the origin through the proxy", async () => {
  const proxy = await startCountingProxy((request, response) => {
    response.writeHead(200, { "content-type": "text/plain" });
    response.end(`proxied ${request.url}`);
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    const response = await proxyFetch("http://example.test/asset.bin");

    assert.equal(response.status, 200);
    assert.equal(await response.text(), "proxied http://example.test/asset.bin");
    assert.deepEqual(proxy.seen, ["http://example.test/asset.bin"]);
  } finally {
    proxy.close();
  }
});

test("bypassed targets skip the proxy entirely", async () => {
  const origin = createServer((_request, response) => response.end("direct"));
  const originUrl = await listen(origin);
  const proxy = await startCountingProxy((_request, response) => response.end("proxied"));
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin, NO_PROXY: "127.0.0.1" });
    assert.equal(await (await proxyFetch(originUrl)).text(), "direct");
    assert.deepEqual(proxy.seen, []);
  } finally {
    proxy.close();
    origin.close();
  }
});

test("redirects are followed and re-resolved per hop", async () => {
  const proxy = await startCountingProxy((request, response) => {
    if (request.url.endsWith("/start")) {
      response.writeHead(302, { location: "http://example.test/final" });
      response.end();
      return;
    }
    response.end("landed");
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    const response = await proxyFetch("http://example.test/start");

    assert.equal(await response.text(), "landed");
    assert.deepEqual(proxy.seen, ["http://example.test/start", "http://example.test/final"]);
  } finally {
    proxy.close();
  }
});

test("a request body and method survive the proxy hop", async () => {
  let received = "";
  const proxy = await startCountingProxy((request, response) => {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      received = Buffer.concat(chunks).toString();
      response.writeHead(201);
      response.end(request.method);
    });
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    const response = await proxyFetch("http://example.test/api", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ hello: "cave" }),
    });

    assert.equal(response.status, 201);
    assert.equal(await response.text(), "POST");
    assert.equal(received, '{"hello":"cave"}');
  } finally {
    proxy.close();
  }
});

test("an aborted request rejects instead of hanging", async () => {
  const proxy = await startCountingProxy(() => {
    /* never responds */
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    await assert.rejects(proxyFetch("http://example.test/slow", { signal: AbortSignal.timeout(50) }));
  } finally {
    proxy.close();
  }
});

test("installProxyAwareFetch only wraps when it should", () => {
  const untouched = { fetch };
  assert.equal(installProxyAwareFetch({}, untouched), false);
  assert.equal(untouched.fetch, fetch);

  const runtimeHandled = { fetch };
  assert.equal(installProxyAwareFetch({ NODE_USE_ENV_PROXY: "1", HTTP_PROXY: PROXY }, runtimeHandled), false);
  assert.equal(runtimeHandled.fetch, fetch);

  const wrapped = { fetch };
  assert.equal(installProxyAwareFetch({ HTTPS_PROXY: PROXY }, wrapped), true);
  assert.notEqual(wrapped.fetch, fetch);
});
