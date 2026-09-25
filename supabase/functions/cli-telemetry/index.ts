// Receives the CLI's telemetry batches (packages/cli emitTelemetryEvents) and
// stores valid events with the client IP in public.cli_events. Always answers
// 202: the CLI never reads the response, and a uniform answer gives
// unauthenticated posters no validation oracle.
import { createClient } from "npm:@supabase/supabase-js@2.116.0";
import { clientIp, fromCli, MAX_BATCH, MAX_BODY_BYTES, readBounded, type Row, validateEvent } from "./ingest.ts";

declare const EdgeRuntime: { waitUntil(promise: Promise<unknown>): void };

const secretKeys = JSON.parse(Deno.env.get("SUPABASE_SECRET_KEYS") ?? "{}");
const db = createClient(
  Deno.env.get("SUPABASE_URL")!,
  secretKeys.default ?? Deno.env.get("SUPABASE_SERVICE_ROLE_KEY")!,
  { auth: { persistSession: false, autoRefreshToken: false } },
);

Deno.serve(async (req) => {
  if (req.method !== "POST") return new Response(null, { status: 405, headers: { allow: "POST" } });
  const accepted = () => new Response(null, { status: 202 });
  if (!fromCli(req.headers)) {
    console.warn("cli telemetry dropped browser request");
    return accepted();
  }

  const body = await readBounded(req.body, MAX_BODY_BYTES);
  let events: unknown = null;
  try {
    if (body !== null) events = JSON.parse(body);
  } catch { /* dropped below */ }
  if (!Array.isArray(events) || events.length > MAX_BATCH) {
    console.warn("cli telemetry dropped malformed request");
    return accepted();
  }

  const ip = clientIp(req.headers);
  const rows: Row[] = [];
  for (const event of events) {
    const row = validateEvent(event);
    if (row) rows.push({ ...row, ip });
  }
  if (rows.length < events.length) console.warn("cli telemetry dropped invalid events", { dropped: events.length - rows.length });
  // Answer before the insert: a cold database round trip can take seconds, and
  // the sender has nothing to do with the result.
  if (rows.length > 0 && Date.now() >= globalFullUntil) EdgeRuntime.waitUntil(store(rows));
  return accepted();
});

// While the table's global per-minute cap is full, every row would only be
// counted and dropped, so skip the insert until the minute rolls over. Asked
// only after an insert comes back short, so a normal minute costs nothing extra.
let globalFullUntil = 0;

async function store(rows: Row[]): Promise<void> {
  const { error, count } = await db.from("cli_events").insert(rows, { count: "exact" });
  if (error) {
    console.error("cli telemetry insert failed", { dropped: rows.length, code: error.code, message: error.message });
  } else if (count !== null && count < rows.length) {
    // The table's rate-limit trigger skips rows instead of failing the batch.
    console.warn("cli telemetry dropped by rate limit", { dropped: rows.length - count });
    const { data } = await db.rpc("cli_events_global_full");
    if (data === true) globalFullUntil = (Math.floor(Date.now() / 60_000) + 1) * 60_000;
  }
}
