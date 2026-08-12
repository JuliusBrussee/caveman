# @caveman/kit

Honesty-enforcing React surfaces for Caveman savings. KIT1 ships the locked
`OptimizedByCaveman` mark, the `BasisBadge`, and a headless honesty core — with
the honesty rules built into the types, not just the docs.

```tsx
import { OptimizedByCaveman, BasisBadge } from "@caveman/kit";

<OptimizedByCaveman variant="footer" />     {/* locked mark — cannot be rebranded */}
<BasisBadge basis="inferred" />             {/* basis is required */}
```

Headless core (no React):

```ts
import { normalizeBasis, isVerified } from "@caveman/kit/core";

isVerified("verified");      // true
isVerified("trust-me");      // false
normalizeBasis("trust-me");  // "inferred" — unknown fails closed, never "verified"
```

## Guarantees

- **`basis` is required** — omitting it on `BasisBadge` is a compile error.
- **Unknown basis fails closed** — anything outside the union renders as
  conservative "Inferred" / `unverified`, never "Verified".
- **The mark is locked** — `OptimizedByCaveman` exposes only `variant` and
  `className`; the wordmark and link cannot be overridden.
- **No monthly projection** — the kit shows provenance as given and never
  re-projects savings.
