// What the CLI reads from Caveman Cloud's plan answer (GET /api/v1/auth/me,
// tiers spec §4): the routing row's state and numbers, the once-per-period
// "back to local" line, and `caveman billing`. Limits come from /me only and
// never touch a local module. A /me that cannot be read, or lacks a field, is
// "no answer": nothing fails because of it.
import { moduleHost } from "./apply.js";

export type CloudProduct = {
  id?: string;
  unit?: string;
  free_allowance?: number;
  used?: number;
  period_end?: string;
  state?: string;
  reason?: string;
};
export type CloudMe = { plan?: string; deployment?: string; data?: { level?: string }; products?: CloudProduct[] };

let me: Promise<CloudMe | null> | undefined;

// One /me per command, shared by the module states and the status row.
export function cloudMe(): Promise<CloudMe | null> {
  me ??= moduleHost().cloudMe().catch(() => null);
  return me;
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

// The period's end, or the next 1st (UTC) when /me does not say.
function periodEnd(product: CloudProduct, now: Date): Date {
  const end = product.period_end ? new Date(product.period_end) : new Date(NaN);
  return Number.isNaN(end.getTime()) ? new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1)) : end;
}

// "Free routing used for October. Back to local until Nov 1 · add a card:
// caveman billing", once per period: the period it was shown for is kept in
// the config.
export function allowanceNotice(product: CloudProduct | undefined, now = new Date()): string | undefined {
  if (product?.state !== "limited" || product.reason !== "allowance") return undefined;
  const end = periodEnd(product, now);
  const key = end.toISOString();
  const h = moduleHost();
  if (h.readConfig().routingAllowanceNotice === key) return undefined;
  h.mutateConfig((out) => { out.routingAllowanceNotice = key; });
  const month = new Date(end.getTime() - 1).toLocaleString("en-US", { month: "long", timeZone: "UTC" });
  const until = end.toLocaleString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
  return `Free routing used for ${month}. Back to local until ${until} · add a card: caveman billing`;
}

// The routing row's note and the allowance line, for `caveman status`. Asks
// Cloud only while signed in with routing on.
export async function routingStatus(on: boolean): Promise<{ note?: string; notice?: string }> {
  if (!on || !moduleHost().signedIn()) return {};
  const product = cloudProduct(await cloudMe(), "routing");
  const note = decisionsNote(product);
  const notice = allowanceNotice(product);
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
