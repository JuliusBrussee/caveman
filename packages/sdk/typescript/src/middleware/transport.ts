/** Node transport for the middleware runtime (spec §15): honors HTTP(S)_PROXY / NO_PROXY without Node's
 * NODE_USE_ENV_PROXY flag (CONNECT tunnel for https, absolute-form for http), accepts a custom CA, keeps connections
 * alive and never proxies loopback. node:http/https/tls load lazily, so edge bundles never touch them. */

// The SDK ships no @types/node; the node:* surface used here is typed loosely on purpose.
type Loose = any;
const load = (name: string): Promise<Loose> => import(name);
const isIp = (host: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(':');
const unbracket = (host: string) => host.replace(/^\[|\]$/g, '');

export interface NodeTransport {
  fetch: typeof globalThis.fetch;
  /** Destroys pooled and in-flight sockets, including tunnels still being set up. */
  close(): void;
}

/** Why this transport cannot use `proxy` (TLS to the proxy itself and SOCKS are unsupported), or null. Names the scheme,
 * never the URL, which may carry credentials. `escape` names the caller's way around it. */
export function unsupportedProxy(proxy: string, escape: string): string | null {
  let url: URL | null = null;
  try { url = new URL(proxy.includes('://') ? proxy : `http://${proxy}`); } catch { /* malformed */ }
  if (url?.protocol === 'http:' && url.hostname) return null;
  const scheme = /^([a-z][a-z0-9+.-]*):\/\//i.exec(proxy)?.[1]?.toLowerCase();
  return `${scheme && scheme !== 'http' ? `unsupported proxy scheme "${scheme}"` : 'malformed proxy URL'}: set HTTP(S)_PROXY to an http:// proxy URL, or pass a custom ${escape}`;
}

/** `proxy` is the resolveProxy() result for the runtime endpoint (null when none applies, loopback included). */
export function createNodeTransport(options: { proxy: string | null; ca?: string | readonly string[] | undefined }): NodeTransport {
  let closed = false;
  const connecting = new Set<Loose>();
  let setup: Promise<Loose> | null = null;

  const boot = async () => {
    const [http, https, tls] = await Promise.all([load('node:http'), load('node:https'), load('node:tls')]);
    const ca = options.ca === undefined ? {} : { ca: typeof options.ca === 'string' ? options.ca : [...options.ca] };
    const plain = new http.Agent({ keepAlive: true }), secure = new https.Agent({ keepAlive: true, ...ca });
    if (!options.proxy) return { http, https, plain, secure, agents: [plain, secure] };
    const proxy = new URL(options.proxy.includes('://') ? options.proxy : `http://${options.proxy}`);
    // ponytail: TLS to the proxy itself (https:// proxy URLs) is unsupported, as in the Python transport.
    if (proxy.protocol !== 'http:' || !proxy.hostname) throw new Error('unsupported proxy URL');
    const via = { host: unbracket(proxy.hostname), port: Number(proxy.port || 80) };
    // Like Python's unquote: an invalid %-escape stays literal instead of failing every call.
    const decode = (s: string) => { try { return decodeURIComponent(s); } catch { return s; } };
    const user = `${decode(proxy.username)}:${decode(proxy.password)}`;
    const auth: Record<string, string> = proxy.username ? { 'proxy-authorization': `Basic ${btoa(String.fromCharCode(...new TextEncoder().encode(user)))}` } : {};
    const tunnel = new https.Agent({ keepAlive: true, ...ca });
    // One CONNECT per new pooled connection; the caller's signal bounds it, and a dropped tunnel is never retried here.
    tunnel.createConnection = (o: Loose, done: (error: Error | null, socket?: Loose) => void) => {
      const authority = `${o.host.includes(':') ? `[${o.host}]` : o.host}:${o.port}`;
      const connect = http.request({ ...via, method: 'CONNECT', path: authority, agent: false, headers: { host: authority, ...auth },
        ...(o.cavemanSignal ? { signal: o.cavemanSignal } : {}) });
      let settled = false;
      const finish = (error: Error | null, socket?: Loose) => { if (!settled) { settled = true; connecting.delete(connect); done(error, socket); } };
      connecting.add(connect);
      connect.once('connect', (response: Loose, socket: Loose) => {
        if (response.statusCode !== 200 || closed) { socket.destroy(); finish(new Error(`proxy CONNECT answered ${response.statusCode}`)); return; }
        finish(null, tls.connect({ ...ca, socket, host: o.host, ...(isIp(o.host) ? {} : { servername: o.host }), ALPNProtocols: ['http/1.1'] }));
      });
      connect.once('error', (error: Error) => finish(error));
      connect.end();
    };
    return { http, https, plain, secure, tunnel, via, auth, agents: [plain, secure, tunnel] };
  };

  const fetch = async (input: string | URL | Request, init: RequestInit = {}): Promise<Response> => {
    const s = await (setup ??= boot());
    if (closed) throw new Error('transport closed');
    const url = new URL(String(input)), https = url.protocol === 'https:', method = init.method ?? 'GET';
    const headers = { ...(init.headers as Record<string, string> | undefined) };
    const signal = init.signal ?? undefined, common = { method, ...(signal ? { signal, cavemanSignal: signal } : {}) };
    const request: Loose = !s.via ? (https ? s.https : s.http).request(url, { ...common, headers, agent: https ? s.secure : s.plain })
      : https ? s.https.request(url, { ...common, headers, agent: s.tunnel })
      : s.http.request({ ...s.via, ...common, path: url.href, headers: { ...headers, host: url.host, ...s.auth }, agent: s.plain });
    return new Promise<Response>((resolve, reject) => {
      request.once('error', reject);
      request.once('response', (res: Loose) => {
        const status: number = res.statusCode, empty = status === 204 || status === 205 || status === 304;
        if (empty) res.resume();
        const body = empty ? null : new ReadableStream<Uint8Array>({
          start(controller) {
            res.on('data', (chunk: Uint8Array) => controller.enqueue(new Uint8Array(chunk)));
            res.once('end', () => controller.close());
            res.once('error', (error: Error) => controller.error(error));
          },
          cancel() { res.destroy(); },
        });
        const responseHeaders = new Headers();
        for (const [key, value] of Object.entries(res.headers as Record<string, string | string[] | undefined>)) {
          for (const item of [value ?? []].flat()) responseHeaders.append(key, item);
        }
        try { resolve(new Response(body, { status, headers: responseHeaders })); } catch (error) { res.destroy(); reject(error); }
      });
      request.end(typeof init.body === 'string' ? init.body : undefined);
    });
  };

  return {
    fetch,
    close() {
      closed = true;
      for (const connect of connecting) connect.destroy();
      connecting.clear();
      void setup?.then(s => { for (const agent of s.agents) agent.destroy(); }, () => {});
    },
  };
}
