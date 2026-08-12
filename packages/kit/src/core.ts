// Headless, React-free honesty core for cavekit. Importable on its own via
// "@caveman/kit/core". It encodes the one rule the kit must never break: a
// savings figure's provenance (its basis) is shown exactly as given and never
// upgraded — an unknown basis can never render as "verified".

/**
 * Basis is the provenance of a savings figure. These are distinct provenances,
 * not a quality ranking: `inferred` is a self-measured estimate, `verified` is
 * earned only by the Cloud eval-gated active path. The kit renders what it is
 * given; it never manufactures `verified`.
 */
export type Basis = "measured" | "inferred" | "verified" | "realized";

const KNOWN: readonly string[] = ["measured", "inferred", "verified", "realized"];

/**
 * normalizeBasis fails closed: any value that is not an exact known basis —
 * including any near-miss of "verified" — collapses to the conservative
 * "inferred". Untyped data crossing the boundary can therefore never be shown as
 * verified.
 */
export function normalizeBasis(value: unknown): Basis {
  return typeof value === "string" && KNOWN.includes(value) ? (value as Basis) : "inferred";
}

/** isVerified is true only for an exact "verified" — never for an unknown value. */
export function isVerified(value: unknown): boolean {
  return value === "verified";
}

/** basisLabel is the display label for a basis, fail-closed for unknowns. */
export function basisLabel(value: unknown): string {
  switch (normalizeBasis(value)) {
    case "measured":
      return "Measured";
    case "verified":
      return "Verified";
    case "realized":
      return "Realized";
    default:
      return "Inferred";
  }
}
