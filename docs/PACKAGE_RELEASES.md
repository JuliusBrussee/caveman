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

| Tag | Artifact | Latest published (2026-09-24) | In this branch (Caveman 3.0.0) | GitHub Release |
|---|---|---|---|---|
| `sdk-ts-v*` | npm `@caveman-ai/sdk` | `1.1.0` | `1.2.0` | Yes |
| `sdk-python-v*` | PyPI `caveman-sdk` | `1.1.0` | `1.2.0` | Yes |
| `middleware-ts-v*` | npm `@caveman-ai/middleware` | `0.1.0-alpha.2` | `1.0.0` | Yes |
| `middleware-python-v*` | PyPI `caveman-middleware` | `0.1.0a1` | `1.0.0` | Yes |
| `contracts-v*` | npm `@caveman-ai/contracts` | never published | `2.0.0` | Yes |
| `pi-v*` | npm `@caveman-ai/pi` | `0.1.1` | `0.2.0` | No |
| `bin-v*` | Go binaries and container image | `bin-v1.1.7` (`bin-v1.1.8` was pinned, never tagged) | `bin-v2.0.0` (pinned in `packages/cli/BINARY_RELEASE`) | Yes, with the binaries |
| `cli-v*` (manual) | npm `@caveman-ai/cli` | `1.3.4` | `2.0.0` | — |
| `v*` | Caveman product (installer, plugin, skills) | `v2.7.0` | `v3.0.0` | Yes, "Latest" |

`tests/verify_repo.py` checks that each SDK and middleware package's version,
version constant (`SDK_VERSION`, `MIDDLEWARE_VERSION`), and top `CHANGELOG.md`
heading agree, that the middleware packages' SDK floor is the repository SDK
version, and that every published package ships `LICENSE` and `NOTICE`.

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

**contracts lane** (`contracts`): a pnpm workspace package with no lockfile of
its own. It runs the schema validators, packs with pnpm, and checks that a fresh
npm install resolves the schemas and the OpenAPI document through the exports
map. It gets a GitHub Release from its `CHANGELOG.md`.

## Dist-tags and prereleases

Only a stable version takes the npm `latest` dist-tag, and only when it is
semver-greater than the current `latest` (read from the registry at publish
time). A stable patch on an older line, say `1.1.1` after `1.2.0`, publishes
under `release-<major>.x` instead. A prerelease publishes under its channel,
`alpha`, `beta`, or `rc`, and any other prerelease identifier under `next`. So
`npm install <package>` never resolves a prerelease once the package has a
stable version, and never moves backwards.

`@caveman-ai/middleware@latest` still points at `0.1.0-alpha.2`, published
before this rule. It moves to `1.0.0` when that ships (`1.0.0` is greater).

PyPI has no dist-tags: pip skips prereleases (`0.1.0a1`) unless the user pins
one or passes `--pre`.

## GitHub Releases

Middleware, SDK, and contracts tags get a GitHub Release, created only after
the registry publish succeeded:

- Notes are the package's `CHANGELOG.md` section for that version (heading
  `## <version> — <date>`; the version is the heading's first word). A missing or empty section fails the build job, so
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
`packages/middleware/python/CHANGELOG.md`,
`packages/shared/contracts/CHANGELOG.md`, `packages/pi-extension/CHANGELOG.md`.
Changes land under `## Unreleased`; the release PR renames that heading to
`## <version> — <YYYY-MM-DD>` (`verify_repo.py` requires the top heading of the
SDK, middleware, and contracts changelogs to be the package version).

## Binary and container releases

`release-binaries.yml` builds the 36-binary matrix, requires it complete, and
signs `checksums.txt` with the pinned release key (`checksums.txt.keysig`). The
CLI and the npm launchers check that signature against the public key compiled
into them before installing any binary. The signed manifest also covers the
license files attached to every binary release: `LICENSE` (Apache-2.0),
`NOTICE`, `LICENSING.md`, the third-party notices for the embedded pixel renderer,
its fonts, and `caveman-browse`, and `THIRD_PARTY_GO_LICENSES.tar.gz`: the
license texts of every third-party Go module the six binaries link on any
platform, plus the Go runtime's, collected with a pinned
`github.com/google/go-licenses/v2@v2.0.1`.

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
`version`, `revision`, `licenses` = `Apache-2.0`, `title`, `description`), ships the
license texts under `/licenses/` (third-party Go modules under
`/licenses/third_party/`, from the same pinned `go-licenses`), has buildx SBOM and provenance attestations,
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
  `create-agent-v*`, `pi-v*`, and `contracts-v*` still to be added; `pypi`: `sdk-python-v*`, `middleware-python-v*`;
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

## Caveman 3.0.0 release runbook

The `feat/middleware-enterprise` branch is the 3.0.0 release: the Apache-2.0
relicense plus stable middleware. Its last commit is the release commit. Run
these in order from a clean, up-to-date `main` checkout with a signing key
configured for `git tag -s` (every release workflow refuses unsigned,
lightweight, or off-`main` tags). Each `git tag` is followed by
`git push origin <tag>`.

**Why this order.** The binaries come first because everything else names
them: `packages/cli/BINARY_RELEASE` and the npm launchers already pin
`bin-v2.0.0`, and `deploy/*` names its image. The SDKs publish before the
middleware because middleware 1.0.0 needs SDK 1.2.0 (`^1.2.0` /
`caveman-sdk>=1.2,<2`); published first, a middleware install would fail to
resolve. `v3.0.0` goes out right after the merge because the README and
INSTALL one-liners (`raw.githubusercontent.com/.../v3.0.0/install.sh`) 404
until it exists, and the installer at that ref needs nothing unpublished.

1. **Merge** the branch to `main` (merge commit or fast-forward; the release
   commit must stay intact). Wait for `sync-skill.yml` to push its
   `[skip ci]` commit, then `git pull`.

2. **Product tag, immediately.**

   ```sh
   git tag -s v3.0.0 -m "Caveman 3.0.0"
   git push origin v3.0.0
   gh release create v3.0.0 --verify-tag --latest --title "Caveman 3.0.0" --notes-file <notes.md>
   ```

   The one-liners now resolve. `deploy/*` at this tag still carries
   `@sha256:REPLACE_AT_RELEASE`, so those manifests fail to pull (by design)
   until step 4; the docs point readers at `main`.

3. **Runtime binaries.** `release-binaries.yml` checks that
   `packages/cli/BINARY_RELEASE` already equals the tag (it does):

   ```sh
   node packages/cli/scripts/gen-binaries.mjs --check
   git tag -s bin-v2.0.0 -m "bin-v2.0.0"
   git push origin bin-v2.0.0
   gh run watch "$(gh run list --workflow release-binaries.yml --limit 1 --json databaseId --jq '.[0].databaseId')"
   ```

   Approve the `binary-release` environment. Then, without any GitHub
   credential, confirm anonymous HTTP 200 for all 36 binaries,
   `checksums.txt`, `checksums.txt.keysig` and
   `THIRD_PARTY_GO_LICENSES.tar.gz` (`packages/cli/PUBLISHING.md`, step 3).

4. **Pin the image digest** in a small PR to `main` (no script exists; this is
   the whole change):

   ```sh
   digest="$(docker buildx imagetools inspect ghcr.io/juliusbrussee/caveman-proxy:bin-v2.0.0 --format '{{json .Manifest}}' | jq -r .digest)"
   cosign verify "ghcr.io/juliusbrussee/caveman-proxy@$digest" \
     --certificate-identity-regexp '^https://github.com/JuliusBrussee/caveman/.github/workflows/release-binaries.yml@refs/tags/bin-v' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   sed -i.bak "s/sha256:REPLACE_AT_RELEASE/${digest}/" deploy/kubernetes.yaml deploy/kubernetes-ha.yaml deploy/aws-ecs-task-definition.json
   rm deploy/*.bak
   git grep -n REPLACE_AT_RELEASE -- deploy   # must print nothing
   ```

   The CLI and launcher pins (`packages/cli/BINARY_RELEASE`, generated by
   `node packages/cli/scripts/gen-binaries.mjs` and
   `node packages/cli/scripts/gen-wedge-installer.mjs`) already name
   `bin-v2.0.0` in the release commit; re-run both with `--check` / `git diff
   --exit-code` to prove nothing drifted.

5. **SDKs, and wait for both to publish** (approve `npm` and `pypi`):

   ```sh
   git tag -s sdk-ts-v1.2.0 -m "@caveman-ai/sdk 1.2.0" && git push origin sdk-ts-v1.2.0
   git tag -s sdk-python-v1.2.0 -m "caveman-sdk 1.2.0" && git push origin sdk-python-v1.2.0
   npm view @caveman-ai/sdk@1.2.0 version        # 1.2.0
   pip index versions caveman-sdk                # lists 1.2.0
   ```

6. **Packages** (each is its own `release-packages.yml` run; approve each):

   ```sh
   for tag in middleware-ts-v1.0.0 middleware-python-v1.0.0 contracts-v2.0.0 pi-v0.2.0; do
     git tag -s "$tag" -m "$tag" && git push origin "$tag"
   done
   ```

   `contracts-v2.0.0` needs `contracts-v*` in the `npm` environment's tag
   policy first (user-owned, below). Then the CLI, which publishes by hand
   (`packages/cli/PUBLISHING.md`, step 4) and needs `bin-v2.0.0` live:

   ```sh
   git tag -s cli-v2.0.0 -m "@caveman-ai/cli 2.0.0" && git push origin cli-v2.0.0
   node agents/compile.mjs && pnpm --dir packages/cli test && node agents/probe-installed.mjs --all --json
   (cd packages/cli && npm pack --dry-run && npm publish --access public)
   ```

7. **Environment approvals.** Every run above waits on JuliusBrussee:
   `binary-release` once (step 3), `npm` for `sdk-ts`, `middleware-ts`,
   `contracts`, `pi`, and `pypi` for `sdk-python`, `middleware-python`. A
   rejected or failed run publishes nothing; fix forward with a new version,
   never re-tag.

8. **Post-release checks.**
   - `npm view @caveman-ai/middleware dist-tags` shows `latest: 1.0.0` (moved
     off `0.1.0-alpha.2`); `@caveman-ai/sdk` `latest: 1.2.0`;
     `@caveman-ai/contracts` `latest: 2.0.0`; `@caveman-ai/pi` `latest: 0.2.0`.
   - `pip install 'caveman-middleware[langchain]==1.0.0'` in a fresh venv pulls
     `caveman-sdk` 1.2.0, and `python -c "import caveman_middleware; print(caveman_middleware.__version__)"` prints `1.0.0`.
   - Each package's GitHub Release exists with its SBOM, and none took
     "Latest" (that is `v3.0.0`).
   - `node tests/middleware-e2e/skew.mjs` passes against the published
     clients; set `N1_RUNTIME` there to `bin-v2.0.0` only when the next
     runtime release makes it N-1.
   - The README one-liners install from `v3.0.0` on macOS, Linux and Windows;
     `caveman setup --install` fetches `bin-v2.0.0`; the image runs from its
     pinned digest (`docker run --rm ghcr.io/juliusbrussee/caveman-proxy@<digest> version`
     prints `bin-v2.0.0`) and lists `/licenses/third_party`.
   - Then the checks in [Post-publish proof](#post-publish-proof).

**User-owned before or during the release** (GitHub settings and other repos;
agents cannot change these):

- The `main` ruleset blocking force-push and deletion (every tag gate trusts
  ancestry from `main`).
- Dependabot alerts and security updates.
- The Apple and Windows signing secrets, if platform-signed binaries are
  wanted for this release (the workflow skips signing without them).
- Add `contracts-v*` to the `npm` environment's deployment tag policy, and a
  trusted publisher for `@caveman-ai/contracts` on npm (new package).
- The docs.caveman.so pages: middleware 1.0 / SDK 1.2 install lines, the
  Apache-2.0 license, and the pages the package READMEs link.
- The same Apache-2.0 relicense in `caveman-browse` (source of `browse/`;
  otherwise the next sync reverts it) and `caveman-agent-sdk` (source of
  `packages/agent` and `packages/create-caveman-agent`).

**Operator migration notes** (put these in the `bin-v2.0.0` and `v3.0.0`
release notes):

- **Principal names carry their source.** OIDC principals are now
  `oidc:<issuer>#<claim>` and certificate principals
  `mtls:{uri,dns,cn}:<name>`. Rename token-map entries that configure them,
  and expect sessions such principals created before the upgrade to be
  unreachable (`404`). Steps: [deploy.md, Identity](technical/deploy.md#identity).
- **Python log prefix.** The middleware warn-once line now reads
  `Caveman middleware passed content through unchanged: adapter=… reason=…`,
  the same as TypeScript. Log filters matching the old
  `Caveman middleware decision:` prefix need updating.
- **First 1.1 deploy resets persisted choices once**, because scope ids
  changed: the first call per session after the upgrade compresses from
  scratch. Grants a 1.0 runtime issued keep reading from the shared recovery
  store until they expire, and `sessions/delete` reports
  `originals_deleted: false` for sessions that hold them.

## Post-publish proof

Do not flip public install commands until each registry endpoint resolves to this
project from clean environments. Prove exact version, package owner/repository,
fresh install, and import. Provider spend is outside release workflow.

If smoke fails, deprecate affected npm version or yank PyPI release, remove public
install command, fix forward with new version, and preserve failed artifact and
workflow logs. Never overwrite a published version.
