/**
 * Proxy support for the CLI's outbound HTTP.
 *
 * Node's global `fetch` ignores the conventional proxy variables, so on a host
 * where egress only leaves through a proxy every `fetch` in this CLI fails even
 * though `curl` and `npm` — which do read those variables — succeed. The most
 * visible symptom is `caveman setup --install` reporting the release download as
 * unreachable, with no hint that a proxy is involved.
 *
 * Node 24 can opt in via `NODE_USE_ENV_PROXY=1` and 22 carries the same switch
 * behind an experimental warning, but it is off by default and a process cannot
 * turn it on for itself after start-up. This module implements the variables
 * directly on `node:http`/`node:https`, adds no dependencies, and stays out of
 * the way when the runtime is already handling them.
 */
import { request as httpRequest, type OutgoingHttpHeaders } from "node:http";
import { request as httpsRequest, type RequestOptions } from "node:https";
import { Readable } from "node:stream";
import { connect as tlsConnect } from "node:tls";
import type { Socket } from "node:net";

/** Redirect hops to follow before giving up, matching the fetch spec's limit. */
const MAX_REDIRECTS = 20;

const DEFAULT_PORTS: Record<string, string> = { "http:": "80", "https:": "443" };

/**
 * Reads the first non-empty variable in `names`.
 *
 * Lower case wins over upper case: `http_proxy` is the older spelling and stays
 * authoritative where both are set, which is what curl and the Python and Go
 * toolchains do.
 */
function readEnv(env: NodeJS.ProcessEnv, names: readonly string[]): string | null {
  for (const name of names) {
    const value = env[name]?.trim();
    if (value) return value;
  }
  return null;
}

/** Accepts `http://host:port`, `host:port`, and credentialed forms alike. */
function parseProxyUrl(value: string): URL | null {
  const candidate = /^[a-z][a-z0-9+.-]*:\/\//i.test(value) ? value : `http://${value}`;
  try {
    const url = new URL(candidate);
    return url.hostname ? url : null;
  } catch {
    return null;
  }
}

function portOf(url: URL): string {
  return url.port || DEFAULT_PORTS[url.protocol] || "";
}

/**
 * Applies one `NO_PROXY` entry to a target.
 *
 * Entries match a host exactly or as a domain suffix, with or without a leading
 * dot, and may pin a port. `*` disables proxying outright.
 */
function entryMatches(entry: string, target: URL): boolean {
  if (entry === "*") return true;

  const [rawHost, rawPort] = entry.startsWith("[")
    ? [entry.slice(0, entry.indexOf("]") + 1), entry.slice(entry.indexOf("]") + 1).replace(/^:/, "")]
    : entry.split(":");
  if (rawPort && rawPort !== portOf(target)) return false;

  const host = (rawHost ?? "").replace(/^\.+/, "").toLowerCase();
  if (!host) return false;

  const targetHost = target.hostname.toLowerCase().replace(/^\[|\]$/g, "");
  const bareHost = host.replace(/^\[|\]$/g, "");
  return targetHost === bareHost || targetHost.endsWith(`.${bareHost}`);
}

/** True when `NO_PROXY` exempts this target from proxying. */
export function shouldBypassProxy(target: URL, noProxy: string | null | undefined): boolean {
  if (!noProxy) return false;
  return noProxy
    .split(/[,\s]+/)
    .map((entry) => entry.trim().toLowerCase())
    .filter(Boolean)
    .some((entry) => entryMatches(entry, target));
}

/**
 * Resolves the proxy for one target, or null when it should be reached directly.
 *
 * `ALL_PROXY` is the fallback for both schemes, so a single variable can cover a
 * host that only has one way out.
 */
export function resolveProxyUrl(target: URL, env: NodeJS.ProcessEnv = process.env): URL | null {
  if (target.protocol !== "http:" && target.protocol !== "https:") return null;
  if (shouldBypassProxy(target, readEnv(env, ["no_proxy", "NO_PROXY"]))) return null;

  const names =
    target.protocol === "https:"
      ? ["https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY"]
      : ["http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"];
  const configured = readEnv(env, names);
  return configured ? parseProxyUrl(configured) : null;
}

function proxyAuthHeader(proxy: URL): OutgoingHttpHeaders {
  if (!proxy.username && !proxy.password) return {};
  const credentials = `${decodeURIComponent(proxy.username)}:${decodeURIComponent(proxy.password)}`;
  return { "proxy-authorization": `Basic ${Buffer.from(credentials).toString("base64")}` };
}

function abortError(): Error {
  return new DOMException("This operation was aborted", "AbortError") as unknown as Error;
}

/**
 * Opens a CONNECT tunnel so TLS is negotiated with the target, not the proxy.
 *
 * Terminating TLS at the proxy would hand it the request and, for a signed
 * release download, the bytes we are about to trust.
 */
function openTunnel(target: URL, proxy: URL, signal: AbortSignal | null): Promise<Socket> {
  return new Promise((resolve, reject) => {
    const request = httpRequest({
      host: proxy.hostname,
      port: portOf(proxy),
      method: "CONNECT",
      path: `${target.hostname}:${portOf(target)}`,
      headers: { host: `${target.hostname}:${portOf(target)}`, ...proxyAuthHeader(proxy) },
    });

    const onAbort = () => {
      request.destroy(abortError());
      reject(abortError());
    };
    signal?.addEventListener("abort", onAbort, { once: true });

    request.once("connect", (response, socket) => {
      signal?.removeEventListener("abort", onAbort);
      if (response.statusCode !== 200) {
        socket.destroy();
        reject(new Error(`proxy refused CONNECT to ${target.host}: ${response.statusCode} ${response.statusMessage ?? ""}`.trim()));
        return;
      }
      resolve(socket);
    });
    request.once("error", (error) => {
      signal?.removeEventListener("abort", onAbort);
      reject(error);
    });
    request.end();
  });
}

interface ProxiedRequest {
  readonly method: string;
  readonly url: URL;
  readonly headers: OutgoingHttpHeaders;
  readonly body: Uint8Array | null;
  readonly signal: AbortSignal | null;
}

function sendThroughProxy(request: ProxiedRequest, proxy: URL): Promise<Response> {
  const { method, url, headers, body, signal } = request;

  return new Promise<Response>((resolve, reject) => {
    const finish = async (options: RequestOptions, send: typeof httpsRequest) => {
      const outbound = send(options, (response) => {
        const status = response.statusCode ?? 502;
        const empty = status === 204 || status === 304 || method === "HEAD";
        resolve(
          new Response(empty ? null : (Readable.toWeb(response) as ReadableStream<Uint8Array>), {
            status,
            statusText: response.statusMessage ?? "",
            headers: Object.entries(response.headers).flatMap(([name, value]) =>
              value === undefined ? [] : [[name, Array.isArray(value) ? value.join(", ") : value] as [string, string]],
            ),
          }),
        );
      });

      const onAbort = () => outbound.destroy(abortError());
      signal?.addEventListener("abort", onAbort, { once: true });
      outbound.once("error", reject);
      outbound.once("close", () => signal?.removeEventListener("abort", onAbort));

      if (body) outbound.write(body);
      outbound.end();
    };

    if (url.protocol === "http:") {
      // Plain HTTP is proxied in absolute form; no tunnel needed.
      void finish(
        {
          host: proxy.hostname,
          port: portOf(proxy),
          method,
          path: url.toString(),
          headers: { ...headers, host: url.host, ...proxyAuthHeader(proxy) },
        },
        httpRequest,
      );
      return;
    }

    openTunnel(url, proxy, signal)
      .then((socket) =>
        finish(
          {
            method,
            path: `${url.pathname}${url.search}`,
            headers: { ...headers, host: url.host },
            createConnection: () => tlsConnect({ socket, servername: url.hostname }),
          },
          httpsRequest,
        ),
      )
      .catch(reject);
  });
}

/** `BodyInit` in the bundled DOM types predates byte-array request bodies. */
function asBodyInit(body: Uint8Array | null): BodyInit | null {
  return body as unknown as BodyInit | null;
}

/** Per the fetch spec, these statuses continue as GET without a body. */
function nextMethod(status: number, method: string): string {
  if (status === 303) return method === "HEAD" ? "HEAD" : "GET";
  if ((status === 301 || status === 302) && method === "POST") return "GET";
  return method;
}

/**
 * Wraps a fetch implementation so proxied targets go through the proxy and
 * everything else keeps its existing path.
 *
 * The proxy is resolved per hop, so a redirect that crosses into a bypassed host
 * is followed directly instead of being forced back through the proxy.
 */
export function createProxyAwareFetch(baseFetch: typeof fetch, env: NodeJS.ProcessEnv = process.env): typeof fetch {
  return async function proxyAwareFetch(input, init) {
    const request = new Request(input as RequestInfo, init);
    const target = new URL(request.url);
    const proxy = resolveProxyUrl(target, env);
    if (!proxy) return baseFetch(input as RequestInfo, init);

    const headers: OutgoingHttpHeaders = {};
    request.headers.forEach((value, name) => {
      headers[name] = value;
    });
    const buffered = ["GET", "HEAD"].includes(request.method) ? null : new Uint8Array(await request.arrayBuffer());

    let current: ProxiedRequest = {
      method: request.method,
      url: target,
      headers,
      body: buffered,
      signal: request.signal ?? null,
    };
    let hop = resolveProxyUrl(current.url, env);

    for (let redirects = 0; ; redirects += 1) {
      const response = hop ? await sendThroughProxy(current, hop) : await baseFetch(current.url, {
        method: current.method,
        headers: current.headers as HeadersInit,
        body: asBodyInit(current.body),
        signal: current.signal,
        redirect: "follow",
      });

      const location = response.headers.get("location");
      const redirectable = [301, 302, 303, 307, 308].includes(response.status);
      if (!redirectable || !location || request.redirect === "manual") return response;
      if (redirects >= MAX_REDIRECTS) throw new TypeError("fetch failed: too many redirects");
      if (request.redirect === "error") throw new TypeError("fetch failed: unexpected redirect");

      const next = new URL(location, current.url);
      const method = nextMethod(response.status, current.method);
      void response.body?.cancel();
      current = {
        method,
        url: next,
        headers: current.headers,
        body: method === current.method ? current.body : null,
        signal: current.signal,
      };
      hop = resolveProxyUrl(next, env);
    }
  } as typeof fetch;
}

/**
 * Routes `globalThis.fetch` through the environment's proxy when one is set.
 *
 * Returns whether the wrapper was installed. It is skipped when nothing is
 * configured and when `NODE_USE_ENV_PROXY` is set, because the runtime then owns
 * proxy selection and wrapping on top of it would proxy twice.
 */
export function installProxyAwareFetch(
  env: NodeJS.ProcessEnv = process.env,
  scope: { fetch: typeof fetch } = globalThis,
): boolean {
  if (env.NODE_USE_ENV_PROXY) return false;
  if (!readEnv(env, ["https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"])) return false;

  scope.fetch = createProxyAwareFetch(scope.fetch.bind(scope), env);
  return true;
}
