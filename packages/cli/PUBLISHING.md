# Publishing @caveman-ai/cli

CI publishes from a `cli-v*` tag through `release-packages.yml`, behind the
`npm` environment approval. Never publish from a laptop or an unpacked tarball.

- The package is `@caveman-ai/cli` (public, npm provenance from trusted
  publishing). Never publish under bare `caveman`; that npm name belongs to
  someone else.
- `bin` installs both `caveman` and `cave`.
- The tarball is JS only: `dist/`, `README.md`, `LICENSE`, `NOTICE`. There is
  no `postinstall` fetch; users run `caveman setup --install`.

## Two release trains

| Tag | Workflow | Publishes |
|---|---|---|
| `bin-v*` | `release-binaries.yml` (`binary-release` environment) | Signed Go binaries and manifests to this repository's GitHub Releases |
| `cli-v*` | `release-packages.yml` (`npm` environment) | The npm package |

`packages/cli/BINARY_RELEASE` pins the binary release the CLI installs; CLI
semver does not imply a binary version. The `binary-release` environment holds
one secret, `CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM`, matching the committed
`BINARY_SIGNING_PUBKEY.pub`. `caveman setup --install` verifies the manifest
signature with the key compiled into the CLI, then each artifact's SHA-256,
before an atomic install.

## Release order

Assets must exist before an npm version names them.

1. Set `BINARY_RELEASE` to the binary tag about to be cut, run the CLI tests and
   commit the regenerated constants. The latest `profile-contract` run
   (`profiles.yml`) must be green.
2. Cut the binary tag (annotated, signed, on `main`). `release-binaries.yml`
   builds, signs and uploads after the `binary-release` approval.
3. Check every asset downloads anonymously (no `GITHUB_TOKEN`, `GH_TOKEN`,
   `~/.netrc` or signed-in `gh`): the 36 binaries from
   `node scripts/build-release-binaries.mjs --list`, plus `checksums.txt`,
   `checksums.txt.keysig` and `RELEASE`.
4. Bump `packages/cli/package.json` in a PR to `main`. On the release machine
   run `node agents/probe-installed.mjs --all --json`; it fails on missing,
   broken or version-drifted binaries. Then tag:

   ```bash
   git tag -s cli-v<version> -m "@caveman-ai/cli <version>"
   git push origin cli-v<version>
   ```

   `release-packages.yml` requires a verified annotated tag whose version
   matches `package.json`, repeats the asset check, checks the generated
   constants, runs the full suite against a freshly built proxy, packs,
   checks `caveman --version` from the installed tarball (version and
   `binary_release`), audits the runtime graph, writes a CycloneDX SBOM and
   publishes after the `npm` approval. It needs `cli-v*` in the `npm`
   environment's tag policy and an npm trusted publisher for
   `@caveman-ai/cli` (`docs/PACKAGE_RELEASES.md`).
5. Smoke-test the exact npm version on clean macOS arm64, Linux x64 and Windows
   x64 machines: install, `caveman setup --install`, `caveman --version`,
   `caveman wrap --off`, one compression with byte-exact retrieval, then
   uninstall without damaging user config.

If the smoke test fails, deprecate the npm version, revert install text in the
docs and investigate before another cut. Never overwrite a published version or
delete a binary release; remove assets only for an active security risk.

## Inspect the tarball before tagging

```bash
node agents/compile.mjs
env -u FORCE_COLOR -u ANTHROPIC_BASE_URL bun run --cwd packages/cli test
(cd packages/cli && npm pack --dry-run)
```

`dist/index.js` must contain the generated binary release constants, and the
tarball must hold no private key, source tree, Go binary or credential. Do not
use `npm --prefix packages/cli pack`: npm 11 can ignore the prefix for `pack`
and `publish` and pack the repository root instead.
