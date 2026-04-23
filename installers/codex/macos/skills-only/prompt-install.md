# Caveman Codex Skill-Only Install Prompt

Install Caveman from https://github.com/JuliusBrussee/caveman as plain Codex skills only. Do not install any plugin files. Do not write anything under `~/.codex/plugins` or `~/.agents/plugins`.

Follow these steps exactly:

1. Inspect repo and confirm it contains these top-level skills:
   - `skills/caveman/SKILL.md`
   - `skills/compress/SKILL.md`
   - `skills/caveman-commit/SKILL.md`
   - `skills/caveman-help/SKILL.md`
   - `skills/caveman-review/SKILL.md`

2. Clone only needed skill subtrees into workspace scratch:

   ```sh
   git clone --depth 1 --filter=blob:none --sparse https://github.com/JuliusBrussee/caveman <workspace>/.tmp/caveman-repo
   git -C <workspace>/.tmp/caveman-repo sparse-checkout set \
     skills/caveman \
     skills/compress \
     skills/caveman-commit \
     skills/caveman-help \
     skills/caveman-review
   ```

3. Ensure Codex skills directory exists:

   ```sh
   mkdir -p ~/.codex/skills
   ```

4. If any target skill already exists, stop and ask before overwriting:
   - `~/.codex/skills/caveman`
   - `~/.codex/skills/compress`
   - `~/.codex/skills/caveman-commit`
   - `~/.codex/skills/caveman-help`
   - `~/.codex/skills/caveman-review`

5. Move extracted skill directories into final Codex skills path:

   ```sh
   mv <workspace>/.tmp/caveman-repo/skills/caveman ~/.codex/skills/caveman
   mv <workspace>/.tmp/caveman-repo/skills/compress ~/.codex/skills/compress
   mv <workspace>/.tmp/caveman-repo/skills/caveman-commit ~/.codex/skills/caveman-commit
   mv <workspace>/.tmp/caveman-repo/skills/caveman-help ~/.codex/skills/caveman-help
   mv <workspace>/.tmp/caveman-repo/skills/caveman-review ~/.codex/skills/caveman-review
   ```

6. Verify install by checking these files exist:
   - `~/.codex/skills/caveman/SKILL.md`
   - `~/.codex/skills/compress/SKILL.md`
   - `~/.codex/skills/caveman-commit/SKILL.md`
   - `~/.codex/skills/caveman-help/SKILL.md`
   - `~/.codex/skills/caveman-review/SKILL.md`

7. Tell user final step:
   - Restart Codex so new skills are discovered cleanly.

Important constraints:
   - Install only into `~/.codex/skills`.
   - Do not create plugin marketplace entries.
   - Do not create `~/.codex/plugins/caveman`.
   - Do not modify `~/.codex/config.toml` for plugin enablement.
