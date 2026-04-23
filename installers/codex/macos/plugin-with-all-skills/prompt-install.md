# Caveman Codex Desktop App Install Prompt

Install the `caveman` Codex plugin from https://github.com/JuliusBrussee/caveman using the exact manual local-plugin flow below. Do not use a generic skill installer. Do not invent a different plugin path.

Follow these steps exactly:

1. Inspect the repo and confirm it contains:
   - Codex plugin files:
   - `plugins/caveman/.codex-plugin/plugin.json`
   - `plugins/caveman/skills/caveman/SKILL.md`
   - `plugins/caveman/skills/compress/SKILL.md`
   - companion skills that will be copied into plugin `skills/` directory:
   - `skills/caveman-commit/SKILL.md`
   - `skills/caveman-help/SKILL.md`
   - `skills/caveman-review/SKILL.md`

2. Clone only the needed plugin and companion skill subtrees into workspace scratch:

   ```sh
   git clone --depth 1 --filter=blob:none --sparse https://github.com/JuliusBrussee/caveman <workspace>/.tmp/caveman-repo
   git -C <workspace>/.tmp/caveman-repo sparse-checkout set \
     plugins/caveman \
     skills/caveman-commit \
     skills/caveman-help \
     skills/caveman-review
   ```

3. Create the local marketplace file at `~/.agents/plugins/marketplace.json` with this exact JSON if the file does not already exist:

   ```json
   {
     "name": "local-plugins",
     "interface": {
       "displayName": "Local Plugins"
     },
     "plugins": [
       {
         "name": "caveman",
         "source": {
           "source": "local",
           "path": "./.codex/plugins/caveman"
         },
         "policy": {
           "installation": "AVAILABLE",
           "authentication": "ON_INSTALL"
         },
         "category": "Productivity"
       }
     ]
   }
   ```

4. If `~/.agents/plugins/marketplace.json` already exists, do not overwrite whole file. Merge new `caveman` entry into existing `plugins` array with same values shown above, and leave existing top-level fields unchanged.

5. Ensure parent directories exist:

   ```sh
   mkdir -p ~/.codex/plugins ~/.agents/plugins
   ```

6. Move extracted plugin directory into final Codex plugin path:

   ```sh
   mv <workspace>/.tmp/caveman-repo/plugins/caveman ~/.codex/plugins/caveman
   ```

7. Move companion skills into plugin skills path so they install/uninstall as part of main plugin:

   ```sh
   mv <workspace>/.tmp/caveman-repo/skills/caveman-commit ~/.codex/plugins/caveman/skills/caveman-commit
   mv <workspace>/.tmp/caveman-repo/skills/caveman-help ~/.codex/plugins/caveman/skills/caveman-help
   mv <workspace>/.tmp/caveman-repo/skills/caveman-review ~/.codex/plugins/caveman/skills/caveman-review
   ```

8. Verify install by checking these files exist:
   - `~/.codex/plugins/caveman/.codex-plugin/plugin.json`
   - `~/.codex/plugins/caveman/skills/caveman/SKILL.md`
   - `~/.codex/plugins/caveman/skills/compress/SKILL.md`
   - `~/.codex/plugins/caveman/skills/caveman-commit/SKILL.md`
   - `~/.codex/plugins/caveman/skills/caveman-help/SKILL.md`
   - `~/.codex/plugins/caveman/skills/caveman-review/SKILL.md`
   - `~/.agents/plugins/marketplace.json`

9. Tell the user final step:
   - Restart Codex so it reloads local marketplace.
   - Open plugin marketplace.
   - Install `Caveman` from `Local Plugins`.

Important constraints:
   - If `~/.codex/plugins/caveman` already exists, stop and ask before overwriting.
   - Use local plugin path `./.codex/plugins/caveman` exactly.
   - If creating new `~/.agents/plugins/marketplace.json`, use marketplace name `local-plugins` and display name `Local Plugins`.
   - If `~/.agents/plugins/marketplace.json` already exists, preserve its existing top-level fields and only merge `caveman` into `plugins`.
   - This flow stages plugin plus companion skills locally first, then Codex picks it up after restart.
   - Companion skills must live under `~/.codex/plugins/caveman/skills/` so uninstalling plugin removes them too.
