/**
 * Type-level guards for cavekit, compiled by `tsc -p tsconfig.test.json`.
 * Each `@ts-expect-error` MUST trigger an error, or tsc fails (TS2578) — so this
 * file actively proves the honesty constraints hold in the type system.
 */
import { BasisBadge, OptimizedByCaveman, QuotaBadge, TrialMoveList, UsageOriginReport, type Basis } from "../src/index.js";

// Valid usage compiles.
const _ok = <BasisBadge basis="inferred" />;
const _verified = <BasisBadge basis="verified" />;
const _mark = <OptimizedByCaveman />;
const _markVariant = <OptimizedByCaveman variant="footer" />;
const _b: Basis = "measured";
const _originReport = <UsageOriginReport basis="inferred" origins={[]} />;
const _moves = <TrialMoveList basis="inferred" moves={[]} />;
const _quota = <QuotaBadge basis="inferred" snapshot={{ provider: "anthropic", plan_type: "max", window: "five_hour", used_pct: 50, resets_at: "", basis: "linked_api" }} />;

// `basis` is REQUIRED — omitting it is a compile error.
// @ts-expect-error basis is a required prop
const _missing = <BasisBadge />;

// `basis` is the closed union — an arbitrary string is a compile error.
// @ts-expect-error "trust-me" is not a Basis
const _bogus = <BasisBadge basis="trust-me" />;

// The attribution mark is locked: there is no `label` or `href` prop to override.
// @ts-expect-error OptimizedByCaveman has no label prop
const _override = <OptimizedByCaveman label="Acme" />;

// Trial report surfaces must also declare figure provenance.
// @ts-expect-error basis is a required prop
const _trialMissingBasis = <UsageOriginReport origins={[]} />;

// Local trial surfaces cannot display verified provenance.
// @ts-expect-error verified is not a local trial report basis
const _trialVerified = <TrialMoveList basis="verified" moves={[]} />;
// @ts-expect-error measured is not a local trial report basis
const _trialMeasured = <TrialMoveList basis="measured" moves={[]} />;
// @ts-expect-error realized is not a local trial report basis
const _trialRealized = <TrialMoveList basis="realized" moves={[]} />;

void _ok;
void _verified;
void _mark;
void _markVariant;
void _b;
void _originReport;
void _moves;
void _quota;
void _missing;
void _bogus;
void _override;
void _trialMissingBasis;
void _trialVerified;
void _trialMeasured;
void _trialRealized;
