Proposed Caveman updates for OpenCode integration

Context
- Request: "Caveman updates for OpenCode Repository https://github.com/juliusbrussee/caveman"
- Local repo: C:\\Users\\Christian2 (origin -> https://github.com/juliusbrussee/caveman.git)
- Goal: Add minimal, safe CI + docs changes so OpenCode integration can be validated by GitHub Actions and reviewed by maintainers.

Summary of proposed changes
1) CI: Ensure GitHub Actions run tests on PRs and main branch with a small matrix (linux/windows), enable caching for package manager/build artifacts.
2) Docs: Add a short "OpenCode integration" section to README with usage, install instructions, and Windows/PowerShell notes.
3) Smoke test: Add a lightweight smoke workflow (.github/workflows/smoke.yml) that runs the project's test command and lint on PRs.
4) Releases: Ensure release workflow builds artifacts and publishes GitHub releases/autobuilds.

Implementation plan
- Branch: opencode-updates
- Changes to create: PROPOSED_CAVEMAN_UPDATES.md (this file)
- Follow-up: implement README + CI edits on the branch and open a PR. CI will run and validate.

Notes
- Local environment may not have all toolchains; CI is best place to run tests. Commit + PR created so maintainers can review and CI can validate.
