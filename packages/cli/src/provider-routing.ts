// Endpoint proof shared by the OpenClaw overlay and Pi's selected-model router.
// Compare the URL the host SDK would request with the URL this listener would
// forward. API compatibility or a matching hostname alone is not authorization
// to send a provider's credentials to a different endpoint or tenant path.
export type PublishedUpstreams = {
  provider_upstreams?: Readonly<Record<string, string>>;
  compat_upstreams?: Readonly<Record<string, string>>;
  compat_forward_headers?: Readonly<Record<string, readonly string[]>>;
};

export const COMPAT_NAME_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

function endpoint(raw: string | undefined): URL | undefined {
  if (!raw || /[\s?#]/.test(raw)) return undefined;
  try {
    const url = new URL(raw);
    if (!['http:', 'https:'].includes(url.protocol) || !url.hostname
      || url.username || url.password || url.search || url.hash || url.pathname.includes("//")) return undefined;
    // Host SDKs disagree on repeated separators; refuse them rather than
    // normalize a tenant path. Go adapters join decoded path segments.
    // Do not claim equivalence for
    // escaped separators, dot segments, or backslashes normalized by WHATWG URL.
    if (/%|\\/.test(raw) || /(?:^|\/)\.{1,2}(?:\/|$)/.test(raw)) return undefined;
    return url;
  } catch { return undefined; }
}

export function publishedUpstreamsOf(raw: unknown): Record<string, string> {
  const out: Record<string, string> = Object.create(null);
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return out;
  for (const [name, value] of Object.entries(raw)) {
    if (!COMPAT_NAME_RE.test(name)) continue;
    // Retain an invalid mount as unavailable; it must not fall through to a
    // same-named native provider after its credential-bearing URL is omitted.
    out[name] = typeof value === 'string' && endpoint(value) ? value : '';
  }
  return out;
}

function append(base: URL, path: string): string {
  return base.origin + base.pathname.replace(/\/+$/, '') + path;
}

function compatTarget(base: URL, path: string): string {
  const leftPath = base.pathname.replace(/^\/+|\/+$/g, '');
  const rightPath = path.replace(/^\/+|\/+$/g, '');
  const left = leftPath ? leftPath.split('/') : [];
  const right = rightPath ? rightPath.split('/') : [];
  let overlap = Math.min(left.length, right.length);
  while (overlap > 0 && left.slice(-overlap).join('/') !== right.slice(0, overlap).join('/')) overlap--;
  return base.origin + '/' + [...left, ...right.slice(overlap)].join('/');
}

export function verifiedProviderRoute(
  gateway: string, api: string | undefined, provider: string,
  originalBaseUrl: string | undefined, published?: PublishedUpstreams,
): string | undefined {
  const original = endpoint(originalBaseUrl);
  if (!original || !endpoint(gateway)) return undefined;
  const family = api === 'anthropic-messages' ? 'anthropic'
    : api === 'openai-completions' || api === 'openai-responses' ? 'openai'
      : api === 'google-generative-ai' ? 'gemini' : undefined;
  if (!family) return undefined;
  const operation = api === 'anthropic-messages' ? '/v1/messages'
    : api === 'openai-completions' ? '/chat/completions'
      : api === 'openai-responses' ? '/responses' : '/models/caveman-route-proof:streamGenerateContent';
  const expected = append(original, operation);
  const mount = COMPAT_NAME_RE.test(provider) && published?.compat_upstreams
    && Object.hasOwn(published.compat_upstreams, provider) ? published.compat_upstreams[provider] : undefined;
  if (mount !== undefined) {
    const upstream = endpoint(mount);
    if (!upstream || family === 'gemini') return undefined;
    const suffix = family === 'openai' ? '/v1' : '';
    if (compatTarget(upstream, suffix + operation) !== expected) return undefined;
    return gateway.replace(/\/+$/, '') + `/compat/${provider}${suffix}`;
  }
  const upstream = endpoint(published?.provider_upstreams?.[family]);
  if (!upstream) return undefined;
  const versions = family === 'gemini' ? ['/v1beta', '/v1'] : [family === 'openai' ? '/v1' : ''];
  for (const version of versions) {
    if (append(upstream, version + operation) === expected) {
      return gateway.replace(/\/+$/, '') + `/${family}${version}`;
    }
  }
  return undefined;
}

// This is the gateway's ordinary end-to-end header contract for these three
// wire protocols. Additional mount headers require explicit operator config;
// preserving them in an overlay alone does not make the proxy forward them.
const COPIED_HEADERS = ["content-type", "content-encoding", "accept", "accept-encoding", "idempotency-key", "openai-organization", "openai-project", "session_id", "x-session-id", "x-client-request-id", "x-session-affinity", "anthropic-version", "anthropic-beta", "api-version", "user-agent"];
const COMPAT_HEADERS: Readonly<Record<string, readonly string[]>> = {
  openrouter: ["http-referer", "x-openrouter-title", "x-openrouter-categories"],
  nvidia: ["x-billing-invoke-origin"],
  opencode: ["x-opencode-session", "x-opencode-client", "x-opencode-project", "x-opencode-request"],
  "opencode-go": ["x-opencode-session", "x-opencode-client", "x-opencode-project", "x-opencode-request"],
};

export function publishedForwardHeadersOf(raw: unknown): Record<string, string[]> {
  const out: Record<string, string[]> = Object.create(null);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return out;
  for (const [name, values] of Object.entries(raw)) {
    if (COMPAT_NAME_RE.test(name) && Array.isArray(values)) out[name] = values.filter((v): v is string => typeof v === "string").map(v => v.toLowerCase());
  }
  return out;
}

export function unforwardedProviderHeaders(api: string | undefined, provider: string, headers: unknown, published?: PublishedUpstreams): string[] {
  if (headers == null) return [];
  if (typeof headers !== "object" || Array.isArray(headers)) return ["invalid header configuration"];
  const allowed = new Set(COPIED_HEADERS);
  if (api === "openai-completions" || api === "openai-responses") allowed.add("authorization");
  if (api === "anthropic-messages") { allowed.add("authorization"); allowed.add("x-api-key"); }
  if (api === "google-generative-ai") { allowed.add("authorization"); allowed.add("x-goog-api-key"); allowed.add("x-goog-user-project"); }
  if (published?.compat_upstreams && Object.hasOwn(published.compat_upstreams, provider)) {
    for (const name of published.compat_forward_headers?.[provider] ?? []) allowed.add(name.toLowerCase());
    if (Object.hasOwn(COMPAT_HEADERS, provider)) for (const name of COMPAT_HEADERS[provider]!) allowed.add(name);
  }
  return Object.entries(headers).filter(([name, value]) => {
    if (value === undefined) return false;
    const lower = name.toLowerCase();
    // SDK custom headers override their generated Authorization verbatim.
    // The proxy's credential mapper supports Bearer on these routes, so Basic,
    // Digest, empty, or explicitly suppressed auth cannot be certified by name.
    if (lower === "authorization") return typeof value !== "string" || !/^Bearer +\S+$/i.test(value.trim());
    if (["x-api-key", "x-goog-api-key"].includes(lower) && (typeof value !== "string" || !value.trim())) return true;
    return value !== null && !allowed.has(lower);
  }).map(([name]) => name).sort();
}
