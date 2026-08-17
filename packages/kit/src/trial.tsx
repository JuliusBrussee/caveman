import * as React from "react";
import { BasisBadge } from "./badge.js";

export type TrialReportBasis = "inferred";
export type TrialBasis = TrialReportBasis | "observed-local" | "observed_provider" | "observed_local" | "estimated" | "linked_api" | "local_session" | "manual_import";

export interface UsageOrigin {
  source_kind: string;
  agent_slug: string;
  provider: string;
  model: string;
  requests: number;
  input_tokens: number;
  total_cost_usd: number;
  basis: TrialBasis;
}

export interface QuotaSnapshot {
  provider: string;
  plan_type: string;
  window: string;
  used_pct: number;
  resets_at: string;
  basis: TrialBasis;
}

export interface TrialMove {
  optimizer_id: string;
  title: string;
  safety_class: "S0_BYTE_SAFE" | "S1_PROVIDER_NATIVE" | "S2_STRUCTURAL" | "S3_BEHAVIORAL" | string;
  status: "safe_now" | "needs_eval" | "do_not_enable" | "insufficient_data" | string;
  savings_usd_base: number;
  confidence: "low" | "medium" | "high" | string;
}

export interface UsageOriginReportProps {
  origins: UsageOrigin[];
  basis: TrialReportBasis;
  className?: string;
}

export function UsageOriginReport({ origins, basis, className }: UsageOriginReportProps): React.JSX.Element {
  return (
    <section data-caveman-trial="usage-origins" className={className}>
      <header>
        <h2>Usage Origins</h2>
        <BasisBadge basis={localReportBasis(basis)} />
      </header>
      <table>
        <thead>
          <tr>
            <th>Source</th>
            <th>Agent</th>
            <th>Provider</th>
            <th>Model</th>
            <th>Requests</th>
            <th>Input tokens</th>
            <th>Cost</th>
            <th>Basis</th>
          </tr>
        </thead>
        <tbody>
          {origins.map((origin) => (
            <tr key={`${origin.source_kind}:${origin.agent_slug}:${origin.provider}:${origin.model}`}>
              <td>{origin.source_kind}</td>
              <td>{origin.agent_slug}</td>
              <td>{origin.provider}</td>
              <td>{origin.model}</td>
              <td>{origin.requests}</td>
              <td>{origin.input_tokens}</td>
              <td>{formatUSD(origin.total_cost_usd)}</td>
              <td><TrialBasisBadge basis={origin.basis} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

export interface QuotaBadgeProps {
  snapshot: QuotaSnapshot;
  basis: TrialReportBasis;
  className?: string;
}

export function QuotaBadge({ snapshot, basis, className }: QuotaBadgeProps): React.JSX.Element {
  return (
    <div data-caveman-trial="quota-badge" className={className}>
      <strong>{snapshot.provider}</strong>
      <span>{snapshot.plan_type}</span>
      <span>{snapshot.window}</span>
      <span>{Math.round(snapshot.used_pct)}%</span>
      <BasisBadge basis={localReportBasis(basis)} />
      <TrialBasisBadge basis={snapshot.basis} />
    </div>
  );
}

export interface TrialMoveListProps {
  moves: TrialMove[];
  basis: TrialReportBasis;
  className?: string;
}

export function TrialMoveList({ moves, basis, className }: TrialMoveListProps): React.JSX.Element {
  return (
    <section data-caveman-trial="moves" className={className}>
      <header>
        <h2>Trial Moves</h2>
        <BasisBadge basis={localReportBasis(basis)} />
      </header>
      <ol>
        {moves.map((move) => (
          <li key={move.optimizer_id}>
            <strong>{move.title}</strong>
            <code>{move.optimizer_id}</code>
            <span>{move.safety_class}</span>
            <span>{move.status}</span>
            <span>{formatUSD(move.savings_usd_base)}</span>
            <span>{move.confidence}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}

function formatUSD(value: number): string {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? "$" + value.toFixed(4)
    : "Unavailable";
}

function localReportBasis(_value: unknown): TrialReportBasis {
  return "inferred";
}

function TrialBasisBadge({ basis }: { basis: TrialBasis | string }): React.JSX.Element {
  const normalized = localBasis(basis);
  return <span data-caveman-trial-basis={normalized}>{trialBasisLabel(normalized)}</span>;
}

function localBasis(value: unknown): TrialBasis {
  switch (value) {
    case "observed-local":
    case "observed_provider":
    case "observed_local":
    case "estimated":
    case "linked_api":
    case "local_session":
    case "manual_import":
      return value;
    default:
      return "inferred";
  }
}

function trialBasisLabel(value: TrialBasis): string {
  switch (value) {
    case "observed-local":
    case "observed_local":
      return "Observed local";
    case "observed_provider":
      return "Observed provider";
    case "linked_api":
      return "Linked API";
    case "local_session":
      return "Local session";
    case "manual_import":
      return "Manual import";
    case "estimated":
      return "Estimated";
    default:
      return "Inferred";
  }
}
