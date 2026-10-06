// What the CLI reads from Caveman Cloud's plan answer (GET /api/v1/auth/me,
// tiers spec §4) and from caveman-proxy's route-state.json: the routing row's
// state and numbers, the once-per-period pause line, and `caveman billing`.
// Limits never touch a local module. A /me that cannot be read, or lacks a
// field, is "no answer": nothing fails because of it.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { moduleHost } from "./apply.js";
import { cavemanHome } from "./config-home.js";

export type CloudProduct = {
  id?: string;
  unit?: string;
  free_allowance?: number;
  used?: number;
  period_end?: string;
  state?: string;
  reason?: string;
  notice?: string;
};
export type CloudMe = { plan?: string; deployment?: string; data?: { level?: string }; products?: CloudProduct[] };
// status 0: no answer (offline, timeout, not signed in).
export type MeAnswer = { status: number; me: CloudMe | null };

let answer: Promise<MeAnswer> | undefined;

// One /me per command, shared by the module states, the status row and the
// sign-in lines.
export function cloudAnswer(): Promise<MeAnswer> {
  answer ??= moduleHost().cloudMe().catch(() => ({ status: 0, me: null }));
  return answer;
}

export async function cloudMe(): Promise<CloudMe | null> {
  return (await cloudAnswer()).me;
}

const LEVELS = ["off", "counts", "usage", "decisions"];

// What caveman-proxy sends per request, by the same rule: the CLI telemetry
// opt-out sends nothing; otherwise /me's data.level, counts when /me names
// none or cannot be read (never more).
export function runtimeDataLevel(me: CloudMe | null, telemetryOff: boolean): string {
  if (telemetryOff) return "off";
  const level = me?.data?.level;
  return typeof level === "string" && LEVELS.includes(level) ? level : "counts";
}

// What routing sends, with its off switch: said at sign-in, by `caveman on
// routing` and by setup when already signed in. The long form is SECURITY.md.
export const ROUTING_ON_LINE = "routing is on · sends your latest ask (with what your agent attaches to it), the one before it and the end of the agent's last reply to Caveman Cloud to pick the model; on the Free plan Caveman may keep them to improve routing · caveman off routing to stop";

// After every sign-in: what routing does now and what data leaves the machine,
// each with the way to stop it.
export async function printSignInLines(): Promise<void> {
  const h = moduleHost();
  const lines: string[] = [];
  // The stored switch, as caveman-proxy reads it, not the registry default.
  const modules = h.readConfig().modules;
  const routing = !!modules && typeof modules === "object" && (modules as Record<string, unknown>).routing === true;
  lines.push(routing ? ROUTING_ON_LINE : "routing is off · caveman on routing to turn it on");
  const telemetryOff = h.telemetryOff();
  const level = runtimeDataLevel(await cloudMe(), telemetryOff);
  lines.push(telemetryOff
    ? "runtime data: nothing sent (telemetry is off) · caveman telemetry on to send counts"
    : level === "off"
      ? "runtime data: nothing sent (your organization's data level is off)"
      : `runtime data: ${level} per request to your Cloud, never prompt text · caveman telemetry off to stop`);
  for (const line of lines) process.stderr.write(`${line}\n`);
}

export function cloudProduct(answer: CloudMe | null, id: string): CloudProduct | undefined {
  return Array.isArray(answer?.products) ? answer.products.find((product) => product?.id === id) : undefined;
}

// "1,204 of 100,000 free decisions this month", or nothing without numbers.
export function decisionsNote(product: CloudProduct | undefined): string | undefined {
  const count = (n: unknown) => typeof n === "number" && Number.isFinite(n) && n >= 0 ? n.toLocaleString("en-US") : undefined;
  const used = count(product?.used);
  const allowance = count(product?.free_allowance);
  if (used && allowance) return `${used} of ${allowance} free decisions this month`;
  return used ? `${used} decisions this month` : undefined;
}

// caveman-proxy's record of a routing pause the person can act on: a refused
// key (outcome degraded, reason cloud_401|cloud_403), a used-up allowance or a
// billing limit (outcome paused), with Cloud's notice. Gone once `until` passed.
export type RouteState = { outcome?: string; reason?: string; notice?: string; until?: string };

export function routeState(now = new Date()): RouteState | undefined {
  try {
    const state = JSON.parse(readFileSync(join(cavemanHome(), "route-state.json"), "utf8")) as RouteState;
    return Date.parse(state?.until ?? "") > now.getTime() ? state : undefined;
  } catch {
    return undefined;
  }
}

// The routing pause from /me, else from the proxy's record.
export function routingPause(product: CloudProduct | undefined): CloudProduct | undefined {
  if (product?.state === "limited") return product;
  const proxy = routeState();
  if (proxy?.outcome !== "paused") return undefined;
  return { state: "limited", ...(proxy.reason ? { reason: proxy.reason } : {}), ...(proxy.notice ? { notice: proxy.notice } : {}),
    ...(proxy.reason === "allowance" && proxy.until ? { period_end: proxy.until } : {}) };
}

// The period's end, or the next 1st (UTC) when nobody says.
function periodEnd(product: CloudProduct, now: Date): Date {
  const end = product.period_end ? new Date(product.period_end) : new Date(NaN);
  return Number.isNaN(end.getTime()) ? new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1)) : end;
}

// Cloud's text reaches a terminal: no control or bidi characters, bounded.
function printable(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069]/g, "").slice(0, 240);
}

// The pause line, once per period (the period it was shown for is kept in the
// config): Cloud's notice when it sends one, else "Free routing used for
// October. Back to local until Nov 1 · add a card: caveman billing".
export function pauseNotice(pause: CloudProduct | undefined, now = new Date()): string | undefined {
  if (pause?.reason !== "allowance" && pause?.reason !== "billing_limit") return undefined;
  const end = periodEnd(pause, now);
  const key = `${pause.reason}@${end.toISOString()}`;
  const h = moduleHost();
  if (h.readConfig().routingPauseNotice === key) return undefined;
  h.mutateConfig((out) => { out.routingPauseNotice = key; });
  if (pause.notice && printable(pause.notice)) return printable(pause.notice);
  const month = new Date(end.getTime() - 1).toLocaleString("en-US", { month: "long", timeZone: "UTC" });
  const until = end.toLocaleString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
  return pause.reason === "allowance"
    ? `Free routing used for ${month}. Back to local until ${until} · add a card: caveman billing`
    : `Routing reached your billing limit for ${month} · raise it: caveman billing`;
}

// The routing row's note and the pause line, for `caveman status`. Asks Cloud
// only while signed in with routing on.
export async function routingStatus(on: boolean): Promise<{ note?: string; notice?: string }> {
  if (!on || !moduleHost().signedIn()) return {};
  const product = cloudProduct(await cloudMe(), "routing");
  const note = decisionsNote(product);
  const notice = pauseNotice(routingPause(product));
  return { ...(note ? { note } : {}), ...(notice ? { notice } : {}) };
}

// The web app beside a control API: api.<domain> → app.<domain>, the local
// dev API on :8080 → the dev web on :3000, otherwise the same origin.
export function appUrl(base: string): string {
  const url = new URL(base || "https://api.caveman.so");
  if (url.hostname.startsWith("api.")) url.hostname = `app.${url.hostname.slice(4)}`;
  else if ((url.hostname === "localhost" || url.hostname === "127.0.0.1") && url.port === "8080") url.port = "3000";
  return url.origin;
}

// `caveman billing`: open the signed-in Cloud's billing page; always print it.
export function billingCommand(): void {
  const h = moduleHost();
  const base = h.readConfig().baseURL;
  const url = `${appUrl((typeof base === "string" && base) || process.env.CAVE_API_URL || "")}/billing`;
  console.log(url);
  if (h.interactive()) h.openBrowser(url);
}
