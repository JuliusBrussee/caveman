<!-- Published as docs/CONTRIBUTING_PROFILES.md in JuliusBrussee/caveman. -->

# Contributing to Caveman

Caveman accepts community contributions. Fastest path is
one agent profile: one JSON file that teaches `caveman run` how to launch another
AI coding harness through an existing wire protocol and hook surface.

## License boundary

Profiles, CLI launcher, SDKs, contracts, kit, graders, provider catalog,
integration recipes, extension shell, and non-core skills are MIT. Engine-linked
runtime source uses BSL 1.1. Both are contribution targets under terms in
[`CONTRIBUTING.md`](../CONTRIBUTING.md) and [`LICENSING.md`](../LICENSING.md).

## Add an agent profile

A pure profile pull request adds `agents/profiles/<id>.json` for a harness that:

- speaks one existing `wire_protocol`;
- uses one existing `injection.method`;
- uses an existing command or memory hook method, or omits hooks;
- needs no new CLI installer or gateway adapter.

Run:

```bash
node agents/compile.mjs
npm install --ignore-scripts --no-audit --no-fund --no-package-lock --prefix packages/cli
npm --prefix packages/cli run build
node --test packages/cli/tests/agent-registry.runtime.mjs packages/cli/tests/agent-shortcut.runtime.mjs packages/cli/tests/porcelain.runtime.mjs
```

Commit profile plus regenerated `agents/agents.json` and
`packages/cli/src/agents.generated.ts`. Profile-only CI re-runs compiler,
fake-harness matrix, and first-screen help fixture; full server release gate is
separate.

These compiler controls are security boundaries:

1. Environment key: `injection.env` keys must match
   `^[A-Z][A-Z0-9_]*_(BASE_URL|API_BASE|API_KEY|AUTH_TOKEN|HOST)$`. Loader,
   path, and proxy controls are denied. Failure: `injection.env key "<key>" is
   not allowlisted`.
2. Environment value: value must be one exact
   `{{cave_base_url}}`, `{{cave_proxy_url}}`, `{{cave_api_key}}`, or
   `{{cave_org_id}}` token, or a safe identifier literal. Literal URLs and
   unknown templates fail.
3. Profile path: instruction and base-config paths stay under
   `~/.<profile-id>/`, with no `..`. Absolute paths and another profile's home
   fail.
4. Reserved command: profile id and every binary name must not collide with
   porcelain, namespaces, printed commands, or legacy aliases. Failure names
   colliding command.

Unknown top-level keys fail. There is no force flag and no per-profile
exception.

## When code review is required

These are core changes, not pure JSON profile changes:

- new wire protocol;
- new injection or hook method;
- new environment-key shape or template vocabulary;
- change to compiler allowlists or reserved commands;
- profile that needs a hand-written installer.

Profile-only pull requests that stay inside existing contracts merge on
CI-green alone. Changes to compiler enums or allowlists require mandatory
founder review.

## Provider price updates

See `shared/provider-catalog/CONTRIBUTING.md`. Price changes require a
cited source, fresh `verified_at`, and immutable dated snapshot. Unknown models
remain zero-priced and tagged `unpriced:`; never guess.

## Sign commits

This project uses the
[Developer Certificate of Origin](https://developercertificate.org/). Sign every
commit:

```bash
git commit -s -m "your message"
```

That adds `Signed-off-by: Your Name <your@email>`. Contribution license follows
directory rules in [`CONTRIBUTING.md`](../CONTRIBUTING.md).

Keep pull requests small and focused. Questions: open a discussion or email
`hello@caveman.so`.
