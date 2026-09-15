# Publishing @caveman-ai/cli

Founder sign-off remains required. Publish from a reviewed repository checkout,
never from an unpacked tarball.

## Package name

- Publish as **`@caveman-ai/cli`** with `npm publish --access public`.
- Never publish under bare `caveman`; that npm name is unrelated.
- Package bin entries install both `caveman` and `cave`.

## Release trains

| Tag | Owns |
|---|---|
| `cli-v*` | Manual npm publication of this zero-runtime-dependency JS CLI |
| `bin-v*` | Signed Go companions through `release-binaries.yml` |

(Caveman Cloud server images release from the Caveman Cloud repository, not
from here.)

The binary workflow publishes compiled artifacts and signed manifests to this
repository's Releases at
`https://github.com/JuliusBrussee/caveman/releases/download`.
`packages/cli/BINARY_RELEASE` independently pins the binary release consumed by
the CLI build; CLI semver does not imply binary version.

The repository environment `binary-release` needs one founder-controlled
secret: `CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM`, matching the committed public
key. Release upload uses the workflow's own `GITHUB_TOKEN` with job-scoped
`contents: write`; no cross-repo PAT exists. Windows assets use
`win32/{arm64,amd64}` names and are installed with `.exe` filenames.

`caveman setup --install` verifies checksum manifest against public key compiled
into CLI, then verifies SHA-256 of each streamed artifact before atomic install.
This local check covers key-signed manifest chain. Workflow derives public key
from private key before signing and verifies generated bundle locally before
upload.

## Release order

Ordering is load-bearing: assets must exist before npm package naming them.

1. Set `packages/cli/BINARY_RELEASE` to the binary tag about to be cut. Run CLI
   tests and commit generated constants. Require the latest `profile-contract`
   Actions run green; it covers shipped profiles against real compression.
2. Cut the matching binary tag in this repository (annotated, signed, on
   `main`). `release-binaries.yml` builds, signs, and publishes assets to this
   repository's Releases behind the `binary-release` environment approval.
3. Without `GITHUB_TOKEN`, `GH_TOKEN`, `~/.netrc`, or authenticated `gh`, verify
   anonymous HTTP 200 for all 36 binaries and two manifest files.
4. Bump `packages/cli/package.json`, cut CLI tag, then run:

   ```bash
   node agents/compile.mjs
   pnpm --dir packages/cli test
   node agents/probe-installed.mjs --all --json
   (cd packages/cli && npm pack --dry-run)
   (cd packages/cli && npm publish --access public)
   ```

   `probe-installed --all` is release-machine proof. Missing, broken, or version-drifted
   binaries fail the gate; CI installs each pin independently so local global installs cannot
   hide profile drift.

5. Post-publish smoke on clean machines (macOS ARM, Linux x64, Windows x64),
   pinned to the exact npm version: install the CLI, `caveman setup --install`,
   `caveman --version`, `caveman wrap --off`, one compression plus byte-exact
   retrieval, then uninstall without damaging user config.

If smoke fails, deprecate the npm version, revert quickstart install text, and
investigate before another cut. Never overwrite a published version or delete
an ordinary binary Release; remove assets only for an active security risk.

## Package contents

Tarball remains JS only: `dist/`, `README.md`, `LICENSE`, and package metadata.
No `postinstall` network fetch exists. Users explicitly run
`caveman setup --install`; package-manager script policy cannot silently block
runtime setup.

Before publication:

```bash
node agents/compile.mjs
pnpm --dir packages/cli test
node agents/probe-installed.mjs --all --json
(cd packages/cli && npm pack --dry-run)
```

Inspect tarball. It must contain generated binary release constants in compiled
`dist/index.js`, but no private key, source tree, Go binary, or credential.
Do not substitute `npm --prefix packages/cli pack`: npm 11 can ignore that prefix for
`pack`/`publish` and target the repository root package instead.
