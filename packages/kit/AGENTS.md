# packages/kit — cavekit, honesty-enforcing React surfaces (MIT, public)

Embeddable React surfaces that show Caveman savings **without ever lying about their
provenance**. KIT1 scope: the locked attribution mark + the basis badge + a headless honesty
core + the trial/usage-origin surface. Zero transport deps.

## Layout
- `src/core.ts` — the React-free honesty core (importable via `@caveman/kit/core`): the `Basis` union + `normalizeBasis`/`isVerified`/`basisLabel`.
- `src/badge.tsx` — `OptimizedByCaveman` (the locked OEM mark) + `BasisBadge`.
- `src/trial.tsx` — the trial/usage-origin surface: `UsageOriginReport`, `QuotaBadge`, `TrialMoveList` (all basis-labeled).
- `src/index.ts` — the package surface.
- `tests/` — `types.test.tsx` (compile-time guards), `core.runtime.mjs` + `badge.runtime.mjs` + `trial.runtime.mjs` (node:test via react-dom/server).

## Honesty, enforced structurally
- **basis is a required prop** on `BasisBadge` — omitting it is a compile error (proven by a `@ts-expect-error` in `types.test.tsx`).
- **unknown basis fails closed** — a value outside the union renders the conservative "Inferred" and is marked `unverified`, never "Verified".
- **the mark is locked** — `OptimizedByCaveman` has no `label`/`href` prop; a host can display it but not rebrand it.
- **no monthly projection** — there is no projection API; provenance is shown as given.

## Conventions
- Build/test: `make product-build PRODUCT=kit` / `make product-test PRODUCT=kit` (the test script builds first, then runs the type + runtime guards).
- `verified` is a distinct provenance the kit only ever *renders when given* — it never manufactures it. The wider suite (SavingsCounter, CavePlan, hooks/transport) is deferred (PRD §12).

See ../../CLAUDE.md (root)
