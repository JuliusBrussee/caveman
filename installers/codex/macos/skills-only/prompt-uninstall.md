# Caveman Codex Skill-Only Uninstall Prompt

Remove Caveman plain Codex skills from `~/.codex/skills`. Do not remove any plugin files. Do not write anything under `~/.codex/plugins` or `~/.agents/plugins`.

Follow these steps exactly:

1. Check whether any of these paths exist:
   - `~/.codex/skills/caveman`
   - `~/.codex/skills/compress`
   - `~/.codex/skills/caveman-commit`
   - `~/.codex/skills/caveman-help`
   - `~/.codex/skills/caveman-review`

2. If none exist, tell user:
   - `Caveman skill-only install not found. Nothing to do.`
   - Stop.

3. Ask for confirmation before deleting anything unless user already made clear they want removal now.

4. Remove any of these directories that exist:

   ```sh
   rm -rf ~/.codex/skills/caveman
   rm -rf ~/.codex/skills/compress
   rm -rf ~/.codex/skills/caveman-commit
   rm -rf ~/.codex/skills/caveman-help
   rm -rf ~/.codex/skills/caveman-review
   ```

5. Verify these paths no longer exist:
   - `~/.codex/skills/caveman`
   - `~/.codex/skills/compress`
   - `~/.codex/skills/caveman-commit`
   - `~/.codex/skills/caveman-help`
   - `~/.codex/skills/caveman-review`

6. Tell user final step:
   - Restart Codex if skills still appear in current session.

Important constraints:
   - Remove only from `~/.codex/skills`.
   - Do not remove `~/.codex/plugins/caveman`.
   - Do not modify `~/.agents/plugins/marketplace.json`.
   - Do not modify `~/.codex/config.toml`.
