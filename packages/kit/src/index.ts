// @caveman/kit — honesty-enforcing React surfaces for Caveman savings.
//
// KIT1 scope: the locked OptimizedByCaveman attribution mark, the BasisBadge,
// and the headless honesty core. The wider component suite (SavingsCounter,
// CavePlan, hooks/transport) is intentionally not here yet (PRD §12).
//
// Honesty, structural:
//   - `basis` is a REQUIRED prop on BasisBadge (omitting it is a compile error).
//   - an unknown basis fails closed to the conservative label, never `verified`.
//   - there is no monthly-projection API — savings provenance is shown as given,
//     never re-projected.
export { type Basis, normalizeBasis, isVerified, basisLabel } from "./core.js";
export {
  OptimizedByCaveman,
  type OptimizedByCavemanProps,
  type OptimizedByCavemanVariant,
  BasisBadge,
  type BasisBadgeProps,
} from "./badge.js";
export {
  UsageOriginReport,
  QuotaBadge,
  TrialMoveList,
  type TrialReportBasis,
  type TrialBasis,
  type UsageOrigin,
  type QuotaSnapshot,
  type TrialMove,
  type UsageOriginReportProps,
  type QuotaBadgeProps,
  type TrialMoveListProps,
} from "./trial.js";
