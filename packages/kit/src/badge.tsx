import * as React from "react";
import { type Basis, basisLabel, isVerified } from "./core.js";

export type OptimizedByCavemanVariant = "inline" | "badge" | "footer";

export interface OptimizedByCavemanProps {
  variant?: OptimizedByCavemanVariant;
  className?: string;
}

/**
 * OptimizedByCaveman is the locked attribution mark — the Robert-OEM v0 piece.
 * The wordmark and link are fixed and cannot be overridden by props (no label or
 * href prop): a host may display it but may not rebrand it. Zero transport deps.
 */
export function OptimizedByCaveman({ variant = "badge", className }: OptimizedByCavemanProps): React.JSX.Element {
  return (
    <a
      href="https://caveman.so"
      target="_blank"
      rel="noreferrer noopener"
      data-caveman-mark="optimized"
      data-variant={variant}
      className={className}
    >
      Optimized by Caveman
    </a>
  );
}

export interface BasisBadgeProps {
  /** REQUIRED. Omitting it is a compile error — a savings figure must declare its provenance. */
  basis: Basis;
  className?: string;
}

/**
 * BasisBadge renders a basis label. It fails closed: a value outside the union
 * that reaches it at runtime renders the conservative "Inferred" and is marked
 * unverified — never "Verified".
 */
export function BasisBadge({ basis, className }: BasisBadgeProps): React.JSX.Element {
  return (
    <span data-caveman-basis={isVerified(basis) ? "verified" : "unverified"} className={className}>
      {basisLabel(basis)}
    </span>
  );
}
