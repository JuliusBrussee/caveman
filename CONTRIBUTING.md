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

Whole repo is [Apache-2.0](LICENSE) (see [LICENSING.md](LICENSING.md)).
Contributions are inbound = outbound: you license your change under the same
Apache-2.0 terms. Simple.

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

## Claude plugin packaging

Claude's marketplace installs `plugins/claude/`. This directory is generated;
edit the canonical files at the repository root, not the generated copies.

After changing a packaged source file:

1. Add any new source files to Git's index so the generator includes them.
2. Run `node scripts/build-claude-plugin.mjs`.
3. Run `node scripts/build-claude-plugin.mjs --check`.
4. Include both the source changes and generated output in the pull request.

The generator includes all tracked files inside each `skills/<name>/`
directory that has a tracked `SKILL.md`, plus the plugin manifest, cavecrew
agents, init command, hooks, init script, README, and license notices.
The root installer package manifest and pnpm lockfile are not plugin inputs.
The generated Claude payload is excluded from the npm installer tarball.

`python3 tests/verify_repo.py` checks generated-file parity. Post-merge
synchronization is a fallback, not a substitute for including current output
in a PR. Existing `plugins/caveman/` mirrors and the release ZIP retain their
separate synchronization requirements.

The generated plugin manifest copies the canonical release version.
In an isolated Claude Code 2.1.306 test, a packaging change at the same
version did not update an existing installation; a higher plugin version did.
Coordinate delivery with the project's release process rather than assigning
a separate version to the generated manifest.

Questions → open a discussion or ping us at **hello@caveman.so**.
