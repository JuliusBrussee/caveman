---
description: Drop the always-on caveman activation rule into the current repo for every IDE agent
argument-hint: "[--dry-run|--force] [--only <agent>]"
---

Write the per-repo caveman rule files (Cursor, Windsurf, Cline, Copilot, AGENTS.md) into the current repo, then report the result.

How to run the init script — pick the first that applies:

1. If `src/tools/caveman-init.js` exists in the current repo (you are inside a caveman checkout), run: `node src/tools/caveman-init.js $ARGUMENTS`
2. Otherwise, if `${CLAUDE_PLUGIN_ROOT}/src/tools/caveman-init.js` exists (you are running the installed plugin, which ships its own copy), run: `node "${CLAUDE_PLUGIN_ROOT}/src/tools/caveman-init.js" $ARGUMENTS`. On Windows Git Bash `${CLAUDE_PLUGIN_ROOT}` can read as `/c/Users/...`; rewrite that leading `/c/` as `C:/` if node cannot find the file.
3. Otherwise there is no local copy to run. Do NOT download the script from a branch and pipe it into node: that executes whatever is on `main` at call time, which defeats an install pinned to a reviewed commit. Instead tell the user they can run `npx -y github:JuliusBrussee/caveman -- --with-init`, which writes the same rule files from the copy it installs — and say plainly that this command **also installs caveman for every agent it detects on the machine**, writing user-level config, because `--with-init` adds the per-repo step rather than replacing the install. Let them decide; do not run it for them.

Use `--dry-run` first if the user did not pass `--force`, so we never silently overwrite an existing rule file.
