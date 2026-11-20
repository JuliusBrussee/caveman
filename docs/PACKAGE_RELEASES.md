# Public package releases

Two workflows publish from `JuliusBrussee/caveman`:

- `release-packages.yml`: the npm and PyPI packages below, one tag per release.
- `release-binaries.yml`: the signed Go runtime binaries (`bin-v*`) and the
  `ghcr.io/juliusbrussee/caveman-proxy` container image.

Build jobs have no OIDC permission. Only the isolated publish jobs mint registry
tokens, through npm and PyPI trusted publishing; no long-lived registry token
exists in the repository. Trusted publishing from GitHub Actions attaches npm
provenance and PyPI attestations automatically.

## Release tags

Tags must be annotated, GitHub-verified, and point to a commit on `main`. The
workflow rejects a tag whose version differs from the package metadata.

| Tag | Artifact | GitHub Release |
|---|---|---|
| `sdk-ts-v*` | npm `@caveman-ai/sdk` | Yes |
| `sdk-python-v*` | PyPI `caveman-sdk` | Yes |
| `middleware-ts-v*` | npm `@caveman-ai/middleware` | Yes |
| `middleware-python-v*` | PyPI `caveman-middleware` | Yes |
| `contracts-v*` | npm `@caveman-ai/contracts` | Yes |
| `pi-v*` | npm `@caveman-ai/pi` | No |
| `bin-v*` | Go binaries and container image (pinned in `packages/cli/BINARY_RELEASE`) | Yes, with the binaries |
| `cli-v*` | npm `@caveman-ai/cli` | No |
| `v*` | Caveman product (installer, plugin, skills) | Yes, "Latest" |

`tests/verify_repo.py` checks that each SDK and middleware package's version,
version constant (`SDK_VERSION`, `MIDDLEWARE_VERSION`), and top `CHANGELOG.md`
heading agree, that the middleware packages' SDK floor is the repository SDK
version, and that every published package ships `LICENSE` and `NOTICE`.

`@caveman-ai/cli` publishes from `cli-v*` through `release-packages.yml` like
the others; its release order is in
[`packages/cli/PUBLISHING.md`](../packages/cli/PUBLISHING.md).

## What each lane checks before publishing

**npm lane** (`sdk-ts`, `pi`): `npm ci --ignore-scripts`
from the package's committed lockfile, `npm audit` (runtime graph at `low`, full
graph at `high`), full tests, `npm pack`, and a fresh-install import smoke of the
exact tarball.

**Bun lane** (`middleware-ts`): `@caveman-ai/middleware` depends on the
workspace SDK, so it builds in the Bun workspace (`bun install
--frozen-lockfile`), builds the SDK, runs the adapter suite with
`CAVEMAN_REQUIRE_FRAMEWORKS=1` plus the consumer test, and packs with `bun pm pack`, which
rewrites `workspace:^` to a concrete range. A fresh npm project then installs
the tarball with the AI SDK and imports `@caveman-ai/middleware/ai-sdk`; the
build fails if the workspace protocol leaked into the manifest.

**CLI lane** (`cli`): also a Bun workspace package. Before building it
requires every asset of the binary release pinned in
`packages/cli/BINARY_RELEASE` (36 binaries, `checksums.txt`,
`checksums.txt.keysig`, `RELEASE`) to download anonymously, since the CLI installs them.
It then checks the generated profile and binary-pin constants against their
sources (`node agents/compile.mjs`, `gen-binaries.mjs --check`), builds the real
proxy, runs the full CLI suite, packs with `bun pm pack`, and checks that the installed
tarball's `caveman --version` reports the tagged version and the binary pin.
`node agents/probe-installed.mjs --all` stays a release-machine check; the
`agent-conformance` workflow covers every profile in CI.

**All npm lanes** then unpack the tarball, resolve its runtime dependencies the
way a consumer would, run `npm audit --omit=dev --audit-level=low` on that
graph, and write a CycloneDX SBOM with `npm sbom`. For the middleware this is the
only audit: a workspace-wide audit covers the whole workspace (and fails on unrelated
packages), and npm cannot resolve the adapters' framework test graph, whose
optional peers conflict. Dependabot watches that test graph instead.

**PyPI lanes** (`sdk-python`, `middleware-python`): the build job installs only
`.github/requirements/release-python.txt` (`--require-hashes`), builds the sdist
and wheel with `--no-isolation`, uploads them, and records their SHA-256
digests as a job output. Nothing unpinned runs in that job. Everything that
installs third-party code from PyPI runs in a separate `python-gates` job on a
downloaded copy, and `publish-pypi` waits for both, then publishes the build
job's artifact only after its files match the recorded digests. In
`python-gates` the middleware lane runs the suite once per certified adapter family (`langchain`, `openai`,
`anthropic`, `litellm`), each in its own virtualenv with the SDK from this
checkout, and requires that family's adapter to run
(`CAVEMAN_REQUIRED_ADAPTERS`) instead of skipping. Wheel and sdist are each
installed into a fresh virtualenv and imported. `python-gates` uploads
nothing. The CycloneDX SBOM of `pip install <wheel>` (no extras) is written in
the build job instead, by the hash-pinned `cyclonedx-py`, into a virtualenv
filled with `--only-binary :all:`, so installing runs no third-party code
(no sdist builds). `cyclonedx-py` then starts that virtualenv's interpreter to
read its `sys.path`; the workflow first deletes every `*.pth`,
`sitecustomize.py` and `usercustomize.py` in it, so site startup cannot execute
code a dependency wheel shipped there. The SBOM comes from each package's
`dist-info` metadata; no dependency module is imported.

Experimental Python families are not in the release gate; they run in
`middleware-python.yml`. The TypeScript release lane runs every adapter family.
Outside releases, `engine-ci.yml` runs the TypeScript SDK and middleware suites
on Node 22 and 24, and the nightly `middleware-canary.yml` runs the TypeScript
suite against the latest release of every framework and opens an issue when it
fails.

**contracts lane** (`contracts`): a Bun workspace package with no lockfile of
its own. It runs the schema validators, packs with `bun pm pack`, and checks that a fresh
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

PyPI has no dist-tags: pip skips prereleases (`0.1.0a1`) unless the user pins
one or passes `--pre`.

## GitHub Releases

Middleware, SDK, and contracts tags get a GitHub Release, created only after
the registry publish succeeded:

- Notes are the package's `CHANGELOG.md` section for that version (heading
  `## <version> — <date>`; the version is the heading's first word). A missing or empty section fails the build job, so
  the release stops before anything is published. Add the section in the
  release PR.
- Attached: the CycloneDX SBOM (`<name>-<version>.cdx.json`). The release job
  downloads the build job's `github-release` artifact by exact name and
  requires its two files (`notes.md` and that SBOM) to match digests the build
  job recorded as an output, so nothing uploaded later in the run can swap them.
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
`LICENSE-MIT` (the pre-3.0.0 MIT text), `NOTICE`, `LICENSING.md`, the third-party notices for the embedded pixel renderer,
its fonts, and `caveman-browse`, the notices for what `zig cc` links into the
cgo binaries (`LICENSE.zig`, `COPYRIGHT.musl`, `COPYING.mingw-w64`), and
`THIRD_PARTY_GO_LICENSES.tar.gz`: the license texts of every third-party Go
module the six binaries link on any platform, plus the Go runtime's, collected
with a pinned `github.com/google/go-licenses/v2@v2.0.1`.

`caveman-proxy` and `caveman-engine` are cgo builds, so they carry the
tree-sitter code compressor (TypeScript, JavaScript, Python, Rust, Java, C,
C++); `scripts/build-release-binaries.mjs` cross-compiles them with `zig cc`
(static musl on Linux, mingw-w64 on Windows, macOS 12 floor, no debug info so
two builds produce the same bytes) and refuses any zig but
`RELEASE_ZIG_VERSION`, which both workflows download by sha256. The other four
binaries stay pure Go, and so does the container image (`Dockerfile`), whose
code compressor therefore parses Go only; the release notes say so. The workflow runs the linux/amd64 engine's embedded evals
and requires the Linux cgo binaries (amd64 and arm64) to be static before
anything is signed. On every PR, engine-ci's `release-shape` job builds the
linux/amd64 set, runs the release-shape tests and evals, and checks that set is
static; the cross-build of all six targets runs only after merge. Locally:
`ZIG=/path/to/zig node scripts/build-release-binaries.mjs --test` builds the
host target, runs the `TestReleaseShape*` tests with the release flags, and runs
the built engine's evals.

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

From `bin-v2.0.0` the signed manifest names its release: the workflow attaches
a `RELEASE` file holding the tag and lists its SHA-256 in `checksums.txt` like
any other file (so the npm launchers' strict parsers still accept it).
`scripts/sign-binary-checksums.mjs` takes the tag as its fourth argument and
refuses a manifest without the matching entry. A CLI pinned to `bin-v2.0.0` or
later refuses a manifest that does not name its pinned release, and so do the
browse, MCP and shrink npm launchers (their shared installer requires the entry
unconditionally), so a release page serving an older signed manifest and
binaries fails the install. CLIs pinned to earlier runtimes accept a manifest
without the entry.

The release also carries the `caveman-blocks` binaries of the `scripts` module
and `modules.json`, the module index. The workflow downloads the
[caveman-ai/blocks](https://github.com/caveman-ai/blocks) release pinned in
`packages/cli/BLOCKS_RELEASE`, which holds the tag and the SHA-256 of that
release's `checksums.txt` (release pages are mutable). The workflow refuses a
`checksums.txt` with another digest, downloads only the archives it lists for
targets the hub builds, checks each against its listed digest, and attaches
each binary as `caveman-blocks_<os>_<arch>` (targets Blocks does not list are
left out). Bumping Blocks means a new tag and its `checksums.txt` digest:
`gh release download <tag> -R caveman-ai/blocks -p checksums.txt -O - | shasum -a 256`. `packages/cli/scripts/gen-modules-index.mjs` then
writes `modules.json` from the module registry and the manifest: per module
its kind, defaults and every platform build's SHA-256. Both are listed in
`checksums.txt` before it is signed. The CLI (`src/modules/index-file.ts`)
refuses a `modules.json` the signed manifest does not list byte for byte.

The container image carries OCI labels (`org.opencontainers.image.source`,
`version`, `revision`, `licenses` = `Apache-2.0`, `title`, `description`), ships the
license texts under `/licenses/` (third-party Go modules under
`/licenses/third_party/`, from the same pinned `go-licenses`), has buildx SBOM and provenance attestations,
and is signed keyless with cosign through GitHub OIDC. The `:latest` tag moves
to an image only after it is signed and its GitHub Release exists, only for a
stable tag (no prerelease suffix) that is the highest stable `bin-v*` tag, so
an older-line patch or a prerelease leaves it alone; the workflow then checks
that `:latest` resolves to the signed digest. The gate ranks every `bin-v*`
tag in the repository, including tags whose release workflow failed, so a
pushed higher tag keeps `:latest` pinned where it is until that tag is
released or deleted. If the `:latest` step fails after `gh release create`
succeeded, point it by hand at the signed digest (after the `cosign verify`
below):

```sh
digest="$(docker buildx imagetools inspect ghcr.io/juliusbrussee/caveman-proxy:bin-vX.Y.Z --format '{{json .Manifest}}' | jq -r .digest)"
docker buildx imagetools create -t ghcr.io/juliusbrussee/caveman-proxy:latest "ghcr.io/juliusbrussee/caveman-proxy@$digest"
```

Binary releases never take the repository's "Latest" badge. Verify a release image
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
$CAVE_BINARY_RELEASE_BASE/<bin-vX.Y.Z>/modules.json
```

The release tag is the one pinned in the installed CLI or launcher, not one the
mirror chooses. The signature and every SHA-256 are still checked against the
compiled-in public key, so a mirror can serve the bytes but cannot swap them.
`CAVE_SETUP_TIMEOUT` (seconds, default 300) bounds each download for slow
mirrors. To fill a mirror, copy the pinned GitHub Release's assets as they are,
for example
`gh release download <bin-vX.Y.Z> --repo JuliusBrussee/caveman --dir <mirror>/<bin-vX.Y.Z>`.

## Registry setup

Every release workflow refuses an unsigned, lightweight, or off-`main` tag.
The `npm`, `pypi`, and `binary-release` GitHub environments each need the
maintainer's approval and accept only their own tag patterns.
`binary-release` holds `CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM`, which must
match `packages/cli/BINARY_SIGNING_PUBKEY.pub`.

Trusted publishers (identity fields are case-sensitive): npm = owner
`JuliusBrussee`, repository `caveman`, workflow `release-packages.yml`,
environment `npm`; PyPI = the same owner, repository, and workflow with
environment `pypi`. After the first trusted publish, set the npm package's
publishing access to require 2FA and disallow tokens. The Python SDK's import
name stays `caveman_cloud`.

### First publish of a new npm package

npm attaches a trusted publisher only to a package that already exists, and
does not allow a first publish through OIDC
([npm/cli#8544](https://github.com/npm/cli/issues/8544)). A new package needs
one manual, owner-only placeholder publish, so the real version still comes
from CI with provenance:

```sh
dir="$(mktemp -d)"
cat > "$dir/package.json" <<'EOF'
{"name":"@caveman-ai/<name>","version":"0.0.0-bootstrap.0","description":"Name placeholder for trusted publishing.","license":"Apache-2.0","repository":{"type":"git","url":"git+https://github.com/JuliusBrussee/caveman.git","directory":"<package dir>"}}
EOF
npm login                       # interactive, 2FA; no token is stored in CI
npm publish "$dir" --access public --tag bootstrap
```

Then add the trusted publisher on npmjs.com, add the tag pattern to the `npm`
environment's policy, push the real tag, and once it is live deprecate the
placeholder with `npm deprecate`. The dist-tag rule gives the real version
`latest`.

## Release order

Dependencies decide the order. Each `git tag -s` is followed by
`git push origin <tag>`; each run waits for environment approval.

1. Merge to `main`. Wait for `sync-skill.yml` to push its `[skip ci]` mirror
   commit, then pull.
2. Push the product tag `v*` right away and create its GitHub Release
   (`gh release create <tag> --verify-tag --latest --notes-file <notes>`). The
   README and INSTALL one-liners pin this tag and 404 until it exists. Notes
   live in `docs/releases/<version>.md`.
3. Runtime binaries (`bin-v*`). `release-binaries.yml` checks that
   `packages/cli/BINARY_RELEASE` already names the tag; run
   `node packages/cli/scripts/gen-binaries.mjs --check` first. Confirm every
   asset downloads anonymously (`packages/cli/PUBLISHING.md`).
4. To move `deploy/*` to the new image, pin its signed digest
   (`tag@sha256:...`) in a small PR, after `cosign verify` as above.
5. SDKs before middleware, because the middleware packages depend on the SDK
   version in this repository. Wait until `npm view` and `pip index versions`
   show the SDK.
6. Middleware, contracts, and Pi, one `release-packages.yml` run each.
7. The CLI last. Its lane refuses to build until every asset of the pinned
   binary release downloads. Run `node agents/probe-installed.mjs --all --json`
   on the release machine first.

A rejected or failed run publishes nothing. Fix forward with a new version;
never re-tag.

## Post-publish proof

Do not flip public install commands until each registry endpoint resolves to this
project from clean environments. Prove exact version, package owner/repository,
fresh install, and import. Provider spend is outside release workflow.

For the CLI, dispatch `release-smoke.yml` with the exact published `cli-version`
and expected `binary-tag`. Its macOS ARM, Linux x64 and Windows x64 jobs run
`.blocks/release-smoke.py` in temporary homes without inherited credentials.
Each job installs the registry package, verifies its version and binary pin,
installs the signed companions, checks record-mode wrapping against the published
proxy, compresses and retrieves byte-exact content, then uninstalls the npm
package while preserving an unrelated configuration file. The block also runs
locally with `--version`, `--binary-tag` and `--repo`; it cleans up its proxy and home.

If smoke fails, deprecate affected npm version or yank PyPI release, remove public
install command, fix forward with new version, and preserve failed artifact and
workflow logs. Never overwrite a published version.
