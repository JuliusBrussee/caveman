import assert from "node:assert/strict";
import test from "node:test";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { createServer as createHttpsServer } from "node:https";
import { connect as netConnect } from "node:net";
import { once } from "node:events";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  createProxyAwareFetch,
  installProxyAwareFetch,
  resolveProxyUrl,
  shouldBypassProxy,
} from "../dist/proxy-fetch.js";

const PROXY = "http://proxy.internal:912";
const PROXY_FETCH_MODULE = new URL("../dist/proxy-fetch.js", import.meta.url).href;
const TLS_KEY = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgtdhLQlNEuoXVg7x+
pxbfijFC2nhlv3iy7t5xBOCGfaKhRANCAASg5AJ7gSIXNAXA0zvb4qIbXkBfaLhR
35KNMapSFSjz0CmfpKyBtbZsxZ2uTsEdET9sTt1dJ+s6XUTJGrN3QEXR
-----END PRIVATE KEY-----`;
const TLS_CERT = `-----BEGIN CERTIFICATE-----
MIIBjTCCATSgAwIBAgIUWj/iLMHYBgf6eS03UlVTAiJ5S/owCgYIKoZIzj0EAwIw
FDESMBAGA1UEAwwJMTI3LjAuMC4xMB4XDTI2MDgyOTIwMTk0NFoXDTM2MDgyNjIw
MTk0NFowFDESMBAGA1UEAwwJMTI3LjAuMC4xMFkwEwYHKoZIzj0CAQYIKoZIzj0D
AQcDQgAEoOQCe4EiFzQFwNM72+KiG15AX2i4Ud+SjTGqUhUo89Apn6SsgbW2bMWd
rk7BHRE/bE7dXSfrOl1EyRqzd0BF0aNkMGIwHQYDVR0OBBYEFA60M/ydjGvkF2tF
b8rMQ5/gzireMB8GA1UdIwQYMBaAFA60M/ydjGvkF2tFb8rMQ5/gzireMA8GA1Ud
EQQIMAaHBH8AAAEwDwYDVR0TAQH/BAUwAwEB/zAKBggqhkjOPQQDAgNHADBEAiAN
iyq9QYU5xMwETw2dRI6gf/LY+MRjugcJbYXhDXgG4gIgHQXeBNM32fb2DmwCGk8N
jjJqLa1BpFaHD/np3GvjRxs=
-----END CERTIFICATE-----`;

function listen(server, scheme = "http") {
  server.listen(0, "127.0.0.1");
  return once(server, "listening").then(() => `${scheme}://127.0.0.1:${server.address().port}`);
}

function runNodeModule(source, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ["--input-type=module", "--eval", source], {
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.once("error", reject);
    child.once("close", (code) => {
      if (code === 0) resolve(stdout);
      else reject(new Error(`child exited ${code}: ${stderr}`));
    });
  });
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

test("HTTPS targets use TLS for both an HTTPS proxy and the CONNECT tunnel", async () => {
  const target = createHttpsServer({ key: TLS_KEY, cert: TLS_CERT }, (_request, response) => response.end("secure target"));
  const targetUrl = await listen(target, "https");
  let connects = 0;
  const proxy = createHttpsServer({ key: TLS_KEY, cert: TLS_CERT });
  proxy.on("connect", (request, client, head) => {
    connects += 1;
    const [host, rawPort] = (request.url ?? "").split(":");
    const upstream = netConnect({ host, port: Number(rawPort) });
    upstream.once("connect", () => {
      client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head.length > 0) upstream.write(head);
      client.pipe(upstream);
      upstream.pipe(client);
    });
    upstream.once("error", () => client.destroy());
  });
  const proxyUrl = await listen(proxy, "https");
  const certDir = mkdtempSync(join(tmpdir(), "cave-proxy-ca-"));
  const certPath = join(certDir, "ca.pem");
  writeFileSync(certPath, TLS_CERT);
  try {
    const source = `
      const { createProxyAwareFetch } = await import(${JSON.stringify(PROXY_FETCH_MODULE)});
      const response = await createProxyAwareFetch(fetch, { HTTPS_PROXY: ${JSON.stringify(proxyUrl)} })(${JSON.stringify(targetUrl)});
      process.stdout.write(await response.text());
    `;
    const output = await runNodeModule(source, { ...process.env, NODE_EXTRA_CA_CERTS: certPath });
    assert.equal(output, "secure target");
    assert.equal(connects, 1);
  } finally {
    rmSync(certDir, { recursive: true, force: true });
    proxy.close();
    target.close();
  }
});

test("unsupported proxy schemes reject before opening a connection", async () => {
  const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: "socks5://127.0.0.1:9" });
  await assert.rejects(proxyFetch("http://example.test"), /unsupported proxy protocol: socks5:/);
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

test("a direct NO_PROXY hop can redirect back through the proxy", async () => {
  const origin = createServer((_request, response) => {
    response.writeHead(302, { location: "http://example.test/final" });
    response.end();
  });
  const originUrl = await listen(origin);
  const proxy = await startCountingProxy((_request, response) => response.end("proxied final"));
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin, NO_PROXY: "127.0.0.1" });
    const response = await proxyFetch(`${originUrl}/start`);
    assert.equal(await response.text(), "proxied final");
    assert.deepEqual(proxy.seen, ["http://example.test/final"]);
  } finally {
    proxy.close();
    origin.close();
  }
});

test("cross-origin redirects do not forward credentials or dropped-body headers", async () => {
  const seen = [];
  const proxy = await startCountingProxy((request, response) => {
    seen.push({ url: request.url, method: request.method, headers: request.headers });
    if (request.url.includes("first.test")) {
      response.writeHead(302, { location: "http://second.test/final" });
      response.end();
      return;
    }
    response.end("landed");
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    const response = await proxyFetch("http://first.test/start", {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        cookie: "session=secret",
        "content-type": "application/json",
      },
      body: "{}",
    });
    assert.equal(await response.text(), "landed");
    assert.equal(seen[0].headers.authorization, "Bearer secret");
    assert.equal(seen[1].method, "GET");
    assert.equal(seen[1].headers.authorization, undefined);
    assert.equal(seen[1].headers.cookie, undefined);
    assert.equal(seen[1].headers["content-type"], undefined);
    assert.equal(seen[1].headers["content-length"], undefined);
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

test("caller proxy authorization never enters destination headers", async () => {
  let received;
  const proxy = await startCountingProxy((request, response) => {
    received = request.headers["proxy-authorization"];
    response.end("ok");
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    await proxyFetch("http://example.test/asset.bin", {
      headers: { "proxy-authorization": "Basic caller-secret" },
    });
    assert.equal(received, undefined);
  } finally {
    proxy.close();
  }
});

test("proxy URL credentials stay on the proxy hop", async () => {
  let received;
  const proxy = await startCountingProxy((request, response) => {
    received = request.headers["proxy-authorization"];
    response.end("ok");
  });
  try {
    const credentialed = proxy.origin.replace("http://", "http://proxy-user:proxy-pass@");
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: credentialed });
    await proxyFetch("http://example.test/asset.bin");
    assert.equal(received, `Basic ${Buffer.from("proxy-user:proxy-pass").toString("base64")}`);
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

test("a request aborted before fetch starts never reaches the proxy", async () => {
  const proxy = await startCountingProxy((_request, response) => response.end("unexpected"));
  const controller = new AbortController();
  controller.abort();
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    await assert.rejects(proxyFetch("http://example.test/expired", { signal: controller.signal }), { name: "AbortError" });
    assert.deepEqual(proxy.seen, []);
  } finally {
    proxy.close();
  }
});

test("abort while buffering a non-GET body never reaches the proxy", async () => {
  const proxy = await startCountingProxy((_request, response) => response.end("unexpected"));
  const controller = new AbortController();
  const body = new ReadableStream({
    start(stream) {
      stream.enqueue(new TextEncoder().encode("partial"));
    },
  });
  try {
    const proxyFetch = createProxyAwareFetch(fetch, { HTTP_PROXY: proxy.origin });
    const pending = proxyFetch("http://example.test/upload", {
      method: "POST",
      body,
      duplex: "half",
      signal: controller.signal,
    });
    setTimeout(() => controller.abort(), 20);
    await assert.rejects(pending, { name: "AbortError" });
    assert.deepEqual(proxy.seen, []);
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
