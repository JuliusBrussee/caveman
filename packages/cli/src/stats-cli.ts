// The proxy owns accounting and HTML. This module only parses the public
// command and renders its already-calculated report; it never recomputes money.
export const STATS_USAGE = "stats [--json] [--days N | --all-time] [--provider ID] [--model ID] [--agent ID] [--auth MODE] [--plain | --open] [--out PATH]";

export const STATS_HELP = `caveman stats — your local token and cost report

  caveman stats                    summary and visual dashboard
  caveman stats --days 7           last 7 UTC calendar days
  caveman stats --all-time         all recorded history
  caveman stats --provider openai  one provider
  caveman stats --model gpt-5.6    one recorded model
  caveman stats --auth subscription
  caveman stats --json             complete machine-readable report
  caveman stats --plain            terminal summary, no browser
  caveman stats --out savings.html --open

Filters: --provider, --model, --agent and --auth use exact recorded values.
Days: 30 by default; 1–3660, or --all-time. All dates use UTC.
The dashboard is a private, offline HTML snapshot. Run stats again to refresh.
API cost estimates and subscription API equivalents are separate. Neither
proves an invoice reduction. Unknown models and billing modes stay unpriced.
`;

export type StatsCLIOptions = {
  json: boolean;
  plain: boolean;
  open: boolean;
  out?: string;
  filters: string[];
};

export function parseStatsOptions(argv: string[]): StatsCLIOptions {
  const result: StatsCLIOptions = { json: false, plain: false, open: false, filters: [] };
  const seen = new Set<string>();
  const valueFlags = new Set(["--days", "--provider", "--model", "--agent", "--auth", "--out"]);
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!;
    if (seen.has(arg)) throw new Error(`duplicate ${arg}`);
    seen.add(arg);
    if (valueFlags.has(arg)) {
      const value = argv[++i];
      if (!value || value.startsWith("--") || /[\u0000-\u001f\u007f]/u.test(value)) throw new Error(`${arg} requires a value`);
      if (arg === "--days" && (!/^\d+$/u.test(value) || Number(value) < 1 || Number(value) > 3660)) {
        throw new Error("--days must be an integer from 1 to 3660");
      }
      if (arg === "--out") result.out = value;
      else result.filters.push(arg, value);
    } else if (arg === "--json") result.json = true;
    else if (arg === "--plain" || arg === "--no-open") result.plain = true;
    else if (arg === "--open") result.open = true;
    else if (arg === "--all-time") result.filters.push("--days", "0");
    else throw new Error(`unknown option ${arg}`);
  }
  if (seen.has("--days") && seen.has("--all-time")) throw new Error("choose --days or --all-time");
  if (result.plain && result.open) throw new Error("choose --plain or --open");
  if (result.json && result.open) throw new Error("--json cannot open a browser");
  return result;
}

type StatsMetrics = {
  requests: number;
  successful_requests: number;
  failed_requests: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  complete_usage_requests: number;
  partial_usage_requests: number;
  measured_requests: number;
  unmeasured_requests: number;
  before_tokens: number;
  after_tokens: number;
  saved_tokens: number;
  expanded_requests: number;
  legacy_requests: number;
  legacy_saved_tokens: number;
  api_spend_requests: number;
  api_savings_requests: number;
  equivalent_savings_requests: number;
  unknown_auth_requests: number;
  api_spend_usd: number | null;
  api_savings_usd: number | null;
  api_equivalent_savings_usd: number | null;
};

export type StatsCLIReport = {
  schema: "caveman.stats.v1";
  generated_at: string;
  window: { from: string; to: string; days: number; lifetime: boolean };
  totals: StatsMetrics;
  providers: (StatsMetrics & { provider: string })[];
  evidence: { caveats: string[] };
  report_path?: string;
};

const count = (n: number): string => Number.isFinite(n) ? n.toLocaleString("en-US") : "unknown";
const money = (n: number | null): string => typeof n === "number" && Number.isFinite(n)
  ? `${n < 0 ? "−" : ""}$${Math.abs(n).toFixed(Math.abs(n) > 0 && Math.abs(n) < 0.01 ? 6 : 4)}`
  : "unavailable";
const label = (value: string): string => value.replace(/[\u001b\u009b][[()#;?]*(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-ORZcf-nqry=><]/gu, "").replace(/[\u0000-\u001f\u007f-\u009f]/gu, "");

export function renderStatsSummary(report: StatsCLIReport): string {
  const m = report.totals;
  const usageCount = (n: number): string => {
    if (m.complete_usage_requests === 0 && n === 0) return "unknown";
    return `${count(n)}${m.partial_usage_requests > 0 ? " observed" : ""}`;
  };
  const lines = [
    "Caveman Stats",
    `${report.window.lifetime ? "All recorded history" : `Last ${report.window.days} days`} · UTC · local estimates`,
    "",
  ];
  if (m.requests === 0) {
    lines.push("No routed requests in this window.", "Run an agent through Caveman, then run caveman stats again.");
  } else {
    const reduction = m.measured_requests > 0
      ? `${count(m.saved_tokens)} tokens${m.before_tokens > 0 ? ` (${(100 * m.saved_tokens / m.before_tokens).toFixed(1)}%)` : ""}`
      : "unavailable — no request comparisons recorded yet";
    lines.push(
      `Input reduction estimate   ${reduction}`,
      `Compared request tokens    ${m.measured_requests > 0 ? `${count(m.before_tokens)} original / ${count(m.after_tokens)} delivered` : "unavailable"}`,
      `API input cost estimate    ${money(m.api_savings_usd)} reduction · ${count(m.api_savings_requests)} priced requests`,
      `Subscription equivalent    ${money(m.api_equivalent_savings_usd)} at API rates · ${count(m.equivalent_savings_requests)} priced requests`,
      `API spend estimate         ${money(m.api_spend_usd)} · ${count(m.api_spend_requests)} priced requests`,
      "",
      `Requests                   ${count(m.requests)} total / ${count(m.successful_requests)} successful / ${count(m.failed_requests)} failed`,
      `Provider usage             ${usageCount(m.input_tokens)} input / ${usageCount(m.output_tokens)} output`,
      `Cache usage                ${usageCount(m.cache_read_tokens)} read / ${usageCount(m.cache_write_tokens)} written`,
      `Usage coverage             ${count(m.complete_usage_requests)} complete / ${count(m.partial_usage_requests)} partial or unavailable`,
      `Comparison coverage        ${count(m.measured_requests)} measured / ${count(m.unmeasured_requests)} unavailable`,
    );
    if (m.expanded_requests > 0) lines.push(`Overhead regressions        ${count(m.expanded_requests)} requests (included in net delta)`);
    if (m.legacy_requests > 0) lines.push(`Legacy segment reductions  ${count(m.legacy_saved_tokens)} tokens / ${count(m.legacy_requests)} requests (separate)`);
    if (m.unknown_auth_requests > 0) lines.push(`Unknown billing            ${count(m.unknown_auth_requests)} requests (excluded from money totals)`);
    if (report.providers?.length) {
      lines.push("", "By provider");
      for (const p of report.providers) {
        lines.push(`  ${label(p.provider)}: ${count(p.requests)} requests · ${p.measured_requests > 0 ? count(p.saved_tokens) : "unknown"} tokens · API ${money(p.api_savings_usd)} · subscription equivalent ${money(p.api_equivalent_savings_usd)}`);
      }
    }
    lines.push("", "API estimates assume the observed cache mix. Subscription equivalents do not reduce your bill.");
  }
  if (report.report_path) lines.push("", `Dashboard: ${report.report_path}`);
  lines.push("Full breakdown: caveman stats --json");
  return `${lines.join("\n")}\n`;
}
