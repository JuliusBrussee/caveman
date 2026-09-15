# Public package releases

`release-packages.yml` publishes four reviewed adoption artifacts from
`JuliusBrussee/caveman`. Build jobs have no OIDC permission; only isolated
publish jobs can mint registry tokens.

## Registry status and remaining setup

Bootstrap releases published and anonymously verified on 2026-08-11:

- npm `@caveman-ai/agent@0.1.0`
- npm `@caveman-ai/sdk@1.0.0`
- npm `@caveman-ai/create-agent@0.1.0`
- PyPI `caveman-sdk==1.0.0`

Fresh installs and runtime imports passed, and registry downloads matched
reviewed artifact SHA-256 hashes. Release artifacts contain no registry
credential.
Remaining work moves future publication to trusted publishers:

1. Mirror reviewed source with `tools/publish-public.sh --apply`, inspect diff,
   then commit and push it to `JuliusBrussee/caveman`. Script never commits or
   pushes.
2. In public repository, create protected GitHub environments named `npm` and
   `pypi`. Restrict deployment to signed release tags on `main`; require founder
   approval.
3. In npm, confirm founder owns `@caveman-ai` scope. Configure each package's
   trusted publisher as owner `JuliusBrussee`, repository `caveman`, workflow
   `release-packages.yml`, environment `npm`, action `npm publish`; then disallow
   long-lived publish tokens.
4. In PyPI, configure trusted publisher for project `caveman-sdk`: owner
   `JuliusBrussee`, repository `caveman`, workflow `release-packages.yml`,
   environment `pypi`. Python import remains `caveman_cloud`.

Registry identity fields are case-sensitive. Official setup references:
[npm trusted publishing](https://docs.npmjs.com/trusted-publishers/) and
[PyPI OIDC from GitHub](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-pypi).

## Release tags

Tags must be annotated, GitHub-verified, and point to `main`. Workflow rejects a
tag whose version differs from package metadata.

| Tag | Artifact | Current version |
|---|---|---|
| `sdk-ts-v1.0.0` | npm `@caveman-ai/sdk` | `1.0.0` |
| `sdk-python-v1.0.0` | PyPI `caveman-sdk` | `1.0.0` |
| `agent-v0.1.0` | npm `@caveman-ai/agent` | `0.1.0` |
| `create-agent-v0.1.0` | npm `@caveman-ai/create-agent` | `0.1.0` |

Release build uses portable committed lockfiles, `npm ci --ignore-scripts`, full
tests, runtime and full-graph audits, tarball/wheel builds, one-day artifacts,
and OIDC publishing. Python publishes wheel plus sdist. npm publishes exact
tarball tested by build job.

## Post-publish proof

Do not flip public install commands until each registry endpoint resolves to this
project from clean environments. Prove exact version, package owner/repository,
fresh install, import, and initializer output. For Agent SDK, run `caveman-agent
doctor` in generated project without provider call, then one credential-backed
stranger response. Provider spend is outside release workflow.

If smoke fails, deprecate affected npm version or yank PyPI release, remove public
install command, fix forward with new version, and preserve failed artifact and
workflow logs. Never overwrite a published version.
