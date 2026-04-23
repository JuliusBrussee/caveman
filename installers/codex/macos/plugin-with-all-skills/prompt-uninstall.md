# Caveman Codex Desktop App Uninstall Prompt

Remove Caveman Codex plugin state completely. This includes local-plugin installs and marketplace-installed Caveman cache/config state. Do not remove standalone skills from `~/.codex/skills`.

Follow these steps exactly:

1. Check whether any of these Caveman plugin artifacts exist:
   - `~/.codex/plugins/caveman`
   - any cache path matching `~/.codex/plugins/cache/*/caveman`
   - `~/.agents/plugins/marketplace.json` containing plugin entry named `caveman`
   - `~/.codex/config.toml` containing any section matching `[plugins."caveman@..."]`

2. If none exist, tell user:
   - `Caveman not installed. Nothing to do.`
   - Stop.

3. Ask for confirmation before deleting anything unless user already made clear they want removal now.

4. If `~/.agents/plugins/marketplace.json` exists:
   - remove plugin entry named `caveman` from its `plugins` array
   - if file becomes empty and still matches default local marketplace shape:
     - `name` = `local-plugins`
     - `interface.displayName` = `Local Plugins`
     - `plugins` = empty
     then delete `~/.agents/plugins/marketplace.json`

5. If `~/.codex/config.toml` exists and contains any section matching `[plugins."caveman@..."]`:
   - remove every such Caveman plugin section

6. Remove these directories if they exist:

   ```sh
   rm -rf ~/.codex/plugins/caveman
   rm -rf ~/.codex/plugins/cache/*/caveman
   ```

7. Remove these parent directories only if they exist and are empty:
   - `~/.agents/plugins`
   - any now-empty parent under `~/.codex/plugins/cache/*`
   - `~/.agents`

8. Verify:
   - `~/.codex/plugins/caveman` does not exist
   - no cache path matching `~/.codex/plugins/cache/*/caveman` exists
   - `~/.agents/plugins/marketplace.json` does not contain plugin entry named `caveman`
   - `~/.codex/config.toml` does not contain any section matching `[plugins."caveman@..."]`

9. Tell user final step:
   - Restart Codex if plugin still appears in current session.

Important constraints:
   - Do not remove anything from `~/.codex/skills`.
   - Do not remove unrelated local plugin entries from `~/.agents/plugins/marketplace.json`.
   - Only remove empty parent directories.
   - Remove Caveman plugin state regardless of marketplace source suffix after `@`.
