# Releasing

Every release comes out of one pipeline (`.github/workflows/release.yml`), fed by two sources.

## Your own development → release

Actions tab → **release** → Run workflow. Pick `patch`/`minor`/`major` (or type an explicit version) and a one-line reason. The pipeline:

1. Runs every test suite (installer, stats, expand, init, config, symlink, pip shim). A red suite aborts the release.
2. Bumps the version across all four sync points via `scripts/release-bump.js` — `pyproject.toml`, `python/caveman_agent/__init__.py`, `PINNED_REF` in `bin/install.js`, `package.json`. The script refuses to run if they already disagree.
3. Regenerates the hook checksum manifest, builds sdist + wheel.
4. Commits `chore(release): vX.Y.Z`, tags, pushes, and creates a GitHub Release with the wheel/sdist attached and notes generated from the commit log.
5. Publishes to PyPI **only** if a `PYPI_API_TOKEN` repo secret exists — otherwise that step is skipped silently and the git tag is the distribution channel.

Users pin releases in requirements.txt:

```
caveman-agent @ git+https://github.com/<this repo>@vX.Y.Z
```

The pip auto-bootstrap re-runs once per version, so shipping a release means users pick it up on their next `pip install -r requirements.txt` + first Python run.

## Upstream releases → release (automated)

`.github/workflows/upstream-watch.yml` runs weekly (Mondays 06:17 UTC, or on demand) and checks the two upstreams tracked in `.github/upstream-versions.json`:

| Upstream | Policy | Why |
|---|---|---|
| `rtk-ai/rtk` | `minor` | RTK is 0.x, where a minor bump is the breaking/major equivalent |
| `JuliusBrussee/caveman` | `major` | Only major upstream releases are worth an automated integration |

Change a policy by editing the JSON (`major` / `minor` / `patch` = smallest bump that triggers).

**When RTK releases:** the watcher smoke-tests the new version against every point where this repo touches RTK — the Windows asset name our installer downloads, the official install script, the binary running, and `rtk gain --all --format json` parsing through our stats extractor. Pass → tracked version updated, **patch release cut automatically**. Fail → an issue is opened naming the likely break point.

**When upstream caveman releases a major:** the watcher attempts a clean `git merge` of the upstream tag. Clean merge + full test suite green → tracked version updated, **minor release cut automatically**. Merge conflicts or red tests → an issue is opened with the manual-integration steps; a diverged fork is never auto-mangled.

## Invariants

- A release only ever exists with a green test suite — both entry points run the same suites.
- `scripts/release-bump.js --check` is the drift alarm; run it any time.
- "Integration done ⇒ new release" holds by construction: the watcher's only success path ends in dispatching `release.yml`.
