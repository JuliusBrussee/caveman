# Publishing @caveman-ai/cli

Founder sign-off remains required. Publish from a reviewed repository checkout,
never from an unpacked tarball.

## Package name

- Publish as **`@caveman-ai/cli`** with `npm publish --access public`.
- Never publish under bare `caveman`; that npm name is unrelated.
- Package bin entries install both `caveman` and `cave`.

## Three release trains

| Tag | Owns |
|---|---|
| `v*` | Five Caveman Cloud server images through `release.yml` |
| `cli-v*` | Manual npm publication of this zero-runtime-dependency JS CLI |
| `bin-v*` | Six signed Go companions through `release-binaries.yml` |

Binary workflow cross-uploads compiled artifacts and signed manifests only to
`https://github.com/JuliusBrussee/caveman/releases/download`. Source never
crosses on this path. `public/cli/BINARY_RELEASE` independently pins binary
release consumed by CLI build; CLI semver does not imply binary version.

`caveman setup --install` verifies checksum manifest against public key compiled
into CLI, then verifies SHA-256 of each streamed artifact before atomic install.
This local check covers key-signed manifest chain. Workflow separately verifies
Sigstore certificate and transparency-log evidence.

## Release order

Ordering is load-bearing: assets must exist before npm package naming them.

1. Set `public/cli/BINARY_RELEASE` to binary tag about to be cut. Run CLI tests
   and commit generated constants. Require latest `Agent conformance` Actions run green;
   it covers every shipped profile against real compression and every pinned upstream binary.
2. Cut matching binary tag in this private repo. Workflow builds, signs, and
   cross-uploads assets to public repo.
3. Without `GITHUB_TOKEN`, `GH_TOKEN`, `~/.netrc`, or authenticated `gh`, verify
   anonymous HTTP 200 for all 24 binaries and four manifest files.
4. Bump `public/cli/package.json`, cut CLI tag, then run:

   ```bash
   node public/agents/compile.mjs
   pnpm --dir public/cli test
   node public/agents/probe-installed.mjs --all --json
   (cd public/cli && npm pack --dry-run)
   (cd public/cli && npm publish --access public)
   ```

   `probe-installed --all` is release-machine proof. Missing, broken, or version-drifted
   binaries fail the gate; CI installs each pin independently so local global installs cannot
   hide profile drift.

5. Trigger post-publish smoke from the matching private CLI tag, pinned to the
   exact npm version, then require it green:

   ```bash
   gh workflow run release-smoke.yml --ref "cli-vA.B.C" -f cli_version="A.B.C"
   ```

   This is explicit because the binary Release lives in the public repo, while
   `release-smoke.yml` lives here; GitHub release events do not cross repositories.

If smoke fails, deprecate npm version, delete public binary Release, revert
quickstart install text, and investigate before another cut.

## Package contents

Tarball remains JS only: `dist/`, `README.md`, `LICENSE`, and package metadata.
No `postinstall` network fetch exists. Users explicitly run
`caveman setup --install`; package-manager script policy cannot silently block
runtime setup.

Before publication:

```bash
node public/agents/compile.mjs
pnpm --dir public/cli test
node public/agents/probe-installed.mjs --all --json
(cd public/cli && npm pack --dry-run)
```

Inspect tarball. It must contain generated binary release constants in compiled
`dist/index.js`, but no private key, source tree, Go binary, or credential.
Do not substitute `npm --prefix public/cli pack`: npm 11 can ignore that prefix for
`pack`/`publish` and target the repository root package instead.
