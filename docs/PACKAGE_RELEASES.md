# Public package releases

Two workflows publish from `JuliusBrussee/caveman`:

- `release-packages.yml`: the npm and PyPI packages below, one tag per release.
- `release-binaries.yml`: the signed Go runtime binaries (`bin-v*`) and the
  `ghcr.io/juliusbrussee/caveman-proxy` container image.

Build jobs have no OIDC permission. Only the isolated publish jobs mint registry
tokens, through npm and PyPI trusted publishing; no long-lived registry token
exists in the repository.

## Release tags

Tags must be annotated, GitHub-verified, and point to a commit on `main`. The
workflow rejects a tag whose version differs from the package metadata.

| Tag | Artifact | Latest published (2026-09-23) | GitHub Release |
|---|---|---|---|
| `sdk-ts-v*` | npm `@caveman-ai/sdk` | `1.1.0` | Yes |
| `sdk-python-v*` | PyPI `caveman-sdk` | `1.1.0` | Yes |
| `middleware-ts-v*` | npm `@caveman-ai/middleware` | `0.1.0-alpha.2` | Yes |
| `middleware-python-v*` | PyPI `caveman-middleware` | `0.1.0a1` | Yes |
| `agent-v*` | npm `@caveman-ai/agent` | `0.1.0` | No |
| `create-agent-v*` | npm `@caveman-ai/create-agent` | `0.1.0` | No |
| `pi-v*` | npm `@caveman-ai/pi` | `0.1.1` | No |
| `bin-v*` | Go binaries and container image | `bin-v1.1.7` (`bin-v1.1.8` is pinned in `packages/cli/BINARY_RELEASE`, not yet tagged) | Yes, with the binaries |

`@caveman-ai/cli` publishes by hand from `cli-v*`; see
[`packages/cli/PUBLISHING.md`](../packages/cli/PUBLISHING.md).

## What each lane checks before publishing

**npm lane** (`sdk-ts`, `agent`, `create-agent`, `pi`): `npm ci --ignore-scripts`
from the package's committed lockfile, `npm audit` (runtime graph at `low`, full
graph at `high`), full tests, `npm pack`, and a fresh-install import smoke of the
exact tarball.

**pnpm lane** (`middleware-ts`): `@caveman-ai/middleware` depends on the
workspace SDK, so it builds in the pnpm workspace (`pnpm install
--frozen-lockfile`), builds the SDK, runs the adapter suite with
`CAVEMAN_REQUIRE_FRAMEWORKS=1` plus the consumer test, and packs with pnpm, which
rewrites `workspace:^` to a concrete range. A fresh npm project then installs
the tarball with the AI SDK and imports `@caveman-ai/middleware/ai-sdk`; the
build fails if the workspace protocol leaked into the manifest.

**Both npm lanes** then unpack the tarball, resolve its runtime dependencies the
way a consumer would, run `npm audit --omit=dev --audit-level=low` on that
graph, and write a CycloneDX SBOM with `npm sbom`. For the middleware this is the
only audit: `pnpm audit` covers the whole workspace (and fails on unrelated
packages), and npm cannot resolve the adapters' framework test graph, whose
optional peers conflict. Dependabot watches that test graph instead.

**PyPI lanes** (`sdk-python`, `middleware-python`): build tools come from
`.github/requirements/release-python.txt`, installed with `--require-hashes`.
The sdist and wheel build with `--no-isolation` before any framework is
installed, so nothing unpinned reaches the artifact. The middleware lane then
runs the suite once per certified adapter family (`langchain`, `openai`,
`anthropic`, `litellm`), each in its own virtualenv with the SDK from this
checkout, and requires that family's adapter to run
(`CAVEMAN_REQUIRED_ADAPTERS`) instead of skipping. Wheel and sdist are each
installed into a fresh virtualenv and imported. `cyclonedx-py` writes a
CycloneDX SBOM of `pip install <wheel>` (no extras).

Experimental Python families are not in the release gate; they run in
`middleware-python.yml`. The TypeScript release lane runs every adapter family.
Outside releases, `engine-ci.yml` runs the TypeScript SDK and middleware suites
on Node 22 and 24, and the nightly `middleware-canary.yml` runs the TypeScript
suite against the latest release of every framework and opens an issue when it
fails.

## Dist-tags and prereleases

Only a stable version takes the npm `latest` dist-tag. A prerelease publishes
under its channel, `alpha`, `beta`, or `rc`, and any other prerelease identifier
under `next`. So `npm install <package>` never resolves a prerelease once the
package has a stable version.

`@caveman-ai/middleware@latest` still points at `0.1.0-alpha.2`, published
before this rule. npm cannot delete the `latest` tag; it moves when the first
stable version ships, or when the owner re-points it by hand
(`npm dist-tag add @caveman-ai/middleware@<version> latest`).

PyPI has no dist-tags: pip skips prereleases (`0.1.0a1`) unless the user pins
one or passes `--pre`.

## GitHub Releases

Middleware and SDK tags get a GitHub Release, created only after the registry
publish succeeded:

- Notes are the package's `CHANGELOG.md` section for that version (heading
  `## <version> — <date>`). A missing or empty section fails the build job, so
  the release stops before anything is published. Add the section in the
  release PR.
- Attached: the CycloneDX SBOM (`*.cdx.json`).
- Linked: the registry's provenance for the version (npm provenance, PyPI
  attestations from trusted publishing).
- Prereleases are marked as such, and no package release takes the repository's
  "Latest" badge; that belongs to the Caveman product release.

Changelogs: `packages/sdk/typescript/CHANGELOG.md`,
`packages/sdk/python/CHANGELOG.md`,
`packages/middleware/typescript/CHANGELOG.md`,
`packages/middleware/python/CHANGELOG.md`. Changes land under `## Unreleased`;
the release PR renames that heading to the version.

## Binary and container releases

`release-binaries.yml` builds the 36-binary matrix, requires it complete, and
signs `checksums.txt` with the pinned release key (`checksums.txt.keysig`). The
CLI and the npm launchers check that signature against the public key compiled
into them before installing any binary. The signed manifest also covers the
license files attached to every binary release: `LICENSE`, `LICENSE.BSL`,
`LICENSING.md`, and the third-party notices for the embedded pixel renderer,
its fonts, and `caveman-browse`.

Optional platform code signing runs when its secrets exist on the
`binary-release` environment and is skipped when they don't:

- macOS: Developer ID signing and notarization with `rcodesign` (bare binaries
  cannot be stapled; Gatekeeper finds the ticket online). Secrets:
  `APPLE_DEVELOPER_ID_P12_BASE64`, `APPLE_DEVELOPER_ID_P12_PASSWORD`,
  `APPLE_NOTARY_API_KEY_JSON` (the App Store Connect API key JSON written by
  `rcodesign encode-app-store-connect-api-key`).
- Windows: timestamped Authenticode with `osslsigncode`. Secrets:
  `WINDOWS_AUTHENTICODE_PFX_BASE64`, `WINDOWS_AUTHENTICODE_PFX_PASSWORD`.

Signing rewrites the binaries, so the workflow recomputes `checksums.txt` before
the release key signs it.

The container image carries OCI labels (`org.opencontainers.image.source`,
`version`, `revision`, `licenses` = `BUSL-1.1`, `title`, `description`), ships the
license texts under `/licenses/`, has buildx SBOM and provenance attestations,
and is signed keyless with cosign through GitHub OIDC. Verify a release image
with:

```sh
cosign verify ghcr.io/juliusbrussee/caveman-proxy:bin-vX.Y.Z \
  --certificate-identity-regexp '^https://github.com/JuliusBrussee/caveman/.github/workflows/release-binaries.yml@refs/tags/bin-v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Images released before this step are unsigned.

### Mirroring the binaries

`caveman setup --install` and the `caveman-mcp`, `caveman-shrink`, and
`caveman-browse` npm launchers download binaries from
`https://github.com/JuliusBrussee/caveman/releases/download` unless
`CAVE_BINARY_RELEASE_BASE` names another base URL. A mirror must serve the
release layout unchanged:

```text
$CAVE_BINARY_RELEASE_BASE/<bin-vX.Y.Z>/checksums.txt
$CAVE_BINARY_RELEASE_BASE/<bin-vX.Y.Z>/checksums.txt.keysig
$CAVE_BINARY_RELEASE_BASE/<bin-vX.Y.Z>/<binary>_<darwin|linux|win32>_<amd64|arm64>
```

The release tag is the one pinned in the installed CLI or launcher, not one the
mirror chooses. The signature and every SHA-256 are still checked against the
compiled-in public key, so a mirror can serve the bytes but cannot swap them.
`CAVE_SETUP_TIMEOUT` (seconds, default 300) bounds each download for slow
mirrors. To fill a mirror, copy the pinned GitHub Release's assets as they are,
for example
`gh release download <bin-vX.Y.Z> --repo JuliusBrussee/caveman --dir <mirror>/<bin-vX.Y.Z>`.

## Repository settings the release path depends on

These live in GitHub settings, not in this repository, and only the owner can
change them. State checked with `gh api` on 2026-09-23.

Required, and in place:

- Environments `npm`, `pypi`, and `binary-release` each require approval from
  `JuliusBrussee` and accept deployments only from their own release tag
  patterns (`npm`: `sdk-ts-v*`, `middleware-ts-v*`, `agent-v*`,
  `create-agent-v*`, `pi-v*`; `pypi`: `sdk-python-v*`, `middleware-python-v*`;
  `binary-release`: `bin-v*`).
- `binary-release` holds `CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM`, matching
  `packages/cli/BINARY_SIGNING_PUBKEY.pub`.
- Private vulnerability reporting is on.

Required, and **not** in place yet:

- A ruleset on `main` that blocks force-pushes and deletion. `main` is
  unprotected today, and every release gate trusts ancestry from `main`.
- Dependabot alerts and Dependabot security updates (Settings, Code security).
  Both are off; `.github/dependabot.yml` only configures version updates.
- CodeQL default setup must stay **off** (it is today): `codeql.yml` is the
  advanced setup, and GitHub refuses advanced-setup uploads while default setup
  is on.

Recommended:

- A tag ruleset limiting who can create release tags. The workflows already
  refuse unsigned, lightweight, or off-`main` tags, and publishing still waits
  for environment approval.
- The Apple and Windows signing secrets above, once the certificates exist.

Registry side, per package (identity fields are case-sensitive): npm trusted
publisher = owner `JuliusBrussee`, repository `caveman`, workflow
`release-packages.yml`, environment `npm`; PyPI trusted publisher = the same
owner, repository, and workflow with environment `pypi`. References:
[npm trusted publishing](https://docs.npmjs.com/trusted-publishers/) and
[PyPI OIDC from GitHub](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-pypi).
The Python SDK's import name stays `caveman_cloud`.

## Post-publish proof

Do not flip public install commands until each registry endpoint resolves to this
project from clean environments. Prove exact version, package owner/repository,
fresh install, import, and initializer output. For Agent SDK, run `caveman-agent
doctor` in generated project without provider call, then one credential-backed
stranger response. Provider spend is outside release workflow.

If smoke fails, deprecate affected npm version or yank PyPI release, remove public
install command, fix forward with new version, and preserve failed artifact and
workflow logs. Never overwrite a published version.
