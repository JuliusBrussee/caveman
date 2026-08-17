# Contributing to Caveman

Thanks for helping make the cave bigger. PRs are welcome: new compressors,
integrations, agent recipes, bug fixes, docs.

A new content-type compressor can add support for CSV, HTML, SQL dumps, notebook
output, or OpenAPI specs. Each one makes Caveman
useful to a whole new slice of payloads. Start from an existing one under
`engine/` and copy its shape.

## Sign your commits (DCO)

This project uses the [Developer Certificate of Origin](https://developercertificate.org/).
It's a one-line promise that you wrote the code (or have the right to contribute
it). Add a sign-off to every commit:

```bash
git commit -s -m "your message"
```

That appends a `Signed-off-by: Your Name <your@email>` trailer. No CLA, no forms.

## Licensing of contributions

Caveman is split-licensed per directory (see [LICENSING.md](LICENSING.md)):

- MIT directories (`packages/{agent,create-caveman-agent,cli,sdk,kit,graders,mastra,subagent-tax}/`,
  `packages/shared/contracts/`, `shared/provider-catalog/`, the extension shell, the skill): contributions are
  inbound = outbound. You license your change under the same MIT terms. Simple.

- BSL-1.1 directories (`engine/`, `proxy/`, `cacheengine/`, `rewriter/`,
  `browse/`, `mcp/`, `shrink/`, the cavemem Go core, `shared/platform/`): by
  contributing, you also **grant Julius Brussee the
  right to relicense your contribution** under commercial or OEM terms. This keeps
  open-core model coherent. Community improvements to engine can ship in
  the commercial product and to OEM partners, instead of fragmenting the codebase.
  Your contribution stays BSL-1.1 in the open repo and sunsets to Apache-2.0 on the
  same Change Date as the rest of the engine.

If you're not comfortable with BSL relicense grant, contribute to MIT parts.

## Where changes land

Open a pull request against this repository. A maintainer checks license scope,
tests, generated artifacts, and security boundaries for affected directory.
Profile-only changes follow narrower process in
[`docs/CONTRIBUTING_PROFILES.md`](docs/CONTRIBUTING_PROFILES.md).

Keep generated files in same pull request as their source and retain commit
authorship and DCO sign-off.

## Before you open a PR

- Build and test the package you touched (`go test ./...`, `pnpm test`, or
  `pytest`, depending on the directory).
- Keep it byte-safe: compressors must round-trip or degrade gracefully. Never
  silently corrupt a payload. On any parse problem, pass the bytes through
  untouched.
- Match the surrounding style. Small, focused PRs review fastest.

Questions → open a discussion or ping us at **hello@caveman.so**.
