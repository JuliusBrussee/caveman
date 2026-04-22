# Caveman Installers for Codex on macOS

This folder contains two macOS installer flows for Caveman:

- `plugin-with-all-skills/`: installs Caveman as a local Codex plugin, with all shipped skills bundled under that plugin.
- `skills-only/`: installs Caveman skills directly into your Codex skills directory, with no plugin marketplace integration.
- `expunge-all-caveman-files.sh`: runs both uninstall flows with `--yes` so users can clear installer-managed Caveman files before a fresh install.

Choose one. Most users should not install both.

## Which installer should you use?

### Use `plugin-with-all-skills` if:

- you use Codex Desktop
- you want Caveman to appear in Codex's local plugin marketplace
- you want one plugin entry that contains all Caveman skills
- you want plugin uninstall to remove plugin files cleanly

### Use `skills-only` if:

- you want simplest install
- you want plain skills under `~/.codex/skills`
- you do not need plugin marketplace integration
- you use Codex CLI or prefer direct skill install

## What gets installed?

### `skills-only`

Installs these skill directories into `~/.codex/skills/`:

- `caveman`
- `compress`
- `caveman-commit`
- `caveman-help`
- `caveman-review`

### `plugin-with-all-skills`

Installs local plugin at:

- `~/.codex/plugins/caveman`

That plugin contains these skills under `~/.codex/plugins/caveman/skills/`:

- `caveman`
- `compress`
- `caveman-commit`
- `caveman-help`
- `caveman-review`

It also adds local marketplace entry at:

- `~/.agents/plugins/marketplace.json`

After script finishes, plugin files exist locally but plugin is not fully active until you restart Codex and install `Caveman` from `Local Plugins` inside marketplace.

## Requirements

### Common

- macOS
- internet access to clone [github.com/JuliusBrussee/caveman](https://github.com/JuliusBrussee/caveman)
- permission to write inside your home directory

### `skills-only/install.sh`

Needs:

- `git`
- `mv`
- `mkdir`

### `skills-only/uninstall.sh`

Needs:

- `rm`
- `rmdir`

### `plugin-with-all-skills/install.sh`

Needs:

- `git`
- `python3`
- `mv`
- `mkdir`

### `plugin-with-all-skills/uninstall.sh`

Needs:

- `python3`
- `rm`
- `rmdir`

## Safety and overwrite behavior

Both installers are conservative:

- they clone only needed parts of repo with Git sparse checkout
- they fail instead of overwriting existing target install
- they verify expected files exist after install
- they clean up temporary clone directory on exit

### Existing install behavior

`skills-only/install.sh` stops if any of these already exist:

- `~/.codex/skills/caveman`
- `~/.codex/skills/compress`
- `~/.codex/skills/caveman-commit`
- `~/.codex/skills/caveman-help`
- `~/.codex/skills/caveman-review`

`plugin-with-all-skills/install.sh` stops if this already exists:

- `~/.codex/plugins/caveman`

Neither installer offers in-place upgrade. If you want clean reinstall, uninstall first.

## Script install

Run from this directory or by absolute path.

### Skills-only install

```sh
chmod +x installers/codex/macos/skills-only/install.sh
installers/codex/macos/skills-only/install.sh
```

Result:

- clones only required skill folders
- creates `~/.codex/skills` if missing
- moves five skill directories into `~/.codex/skills`
- tells you to restart Codex

### Plugin install

```sh
chmod +x installers/codex/macos/plugin-with-all-skills/install.sh
installers/codex/macos/plugin-with-all-skills/install.sh
```

Result:

- clones plugin subtree plus companion skill folders
- creates `~/.codex/plugins` and `~/.agents/plugins` if missing
- writes or updates `~/.agents/plugins/marketplace.json` with local `caveman` entry
- installs plugin to `~/.codex/plugins/caveman`
- moves companion skills into plugin's `skills/` directory
- tells you to restart Codex, open marketplace, then install `Caveman` from `Local Plugins`

## Prompt install

Each installer folder also includes `prompt-install.md`.

These files are not shell scripts. They are exact step-by-step prompts for Codex to perform install manually.

Use prompt version if:

- you want Codex to execute install for you
- you prefer reviewed instructions over running shell script directly
- you want same install logic expressed as agent workflow

### Prompt files

- `skills-only/prompt-install.md`
- `plugin-with-all-skills/prompt-install.md`

Important differences:

- `skills-only/prompt-install.md` explicitly forbids writing under `~/.codex/plugins` or `~/.agents/plugins`
- `plugin-with-all-skills/prompt-install.md` explicitly uses local plugin marketplace flow for Codex Desktop

## Uninstall

Prompt-based uninstall docs also exist:

- `skills-only/prompt-uninstall.md`
- `plugin-with-all-skills/prompt-uninstall.md`

These are manual Codex prompts, not shell scripts.

### Full expunge

Use this when you want highest-confidence cleanup before reinstalling.

```sh
chmod +x installers/codex/macos/expunge-all-caveman-files.sh
installers/codex/macos/expunge-all-caveman-files.sh
```

Behavior:

- runs `skills-only/uninstall.sh --yes`
- runs `plugin-with-all-skills/uninstall.sh --yes`
- removes anything those uninstallers manage, in one pass
- prints `Nothing to do` where one install type is not present
- tells you to restart Codex before fresh install

Important note:

- if you are working inside this `caveman` repo, Codex may still show `Caveman Repo` in plugin marketplace even after full expunge
- that entry comes from repo-local source files in this workspace, not from installed user state
- specifically, this repo includes `.agents/plugins/marketplace.json` and `plugins/caveman`
- full expunge removes installed Caveman state under your home directory, but it does not remove source files that are part of this repo

Recommended use:

- switching from one install mode to another
- cleaning up partial installs
- resetting machine before testing install docs

### Skills-only uninstall

```sh
chmod +x installers/codex/macos/skills-only/uninstall.sh
installers/codex/macos/skills-only/uninstall.sh
```

Optional non-interactive mode:

```sh
installers/codex/macos/skills-only/uninstall.sh --yes
```

Behavior:

- prompts for confirmation unless `--yes`
- removes any of these if present:
  - `~/.codex/skills/caveman`
  - `~/.codex/skills/compress`
  - `~/.codex/skills/caveman-commit`
  - `~/.codex/skills/caveman-help`
  - `~/.codex/skills/caveman-review`
- prints `Nothing to do` if none exist
- tells you to restart Codex if skills still appear in current session

### Plugin uninstall

```sh
chmod +x installers/codex/macos/plugin-with-all-skills/uninstall.sh
installers/codex/macos/plugin-with-all-skills/uninstall.sh
```

Optional non-interactive mode:

```sh
installers/codex/macos/plugin-with-all-skills/uninstall.sh --yes
```

Behavior:

- prompts for confirmation unless `--yes`
- removes plugin directory `~/.codex/plugins/caveman` if present
- removes installed plugin cache at any path matching `~/.codex/plugins/cache/*/caveman`
- removes `caveman` entry from `~/.agents/plugins/marketplace.json`
- deletes `~/.agents/plugins/marketplace.json` entirely if file becomes empty and still matches default local-marketplace shape
- removes any config section matching `[plugins."caveman@..."]` from `~/.codex/config.toml`
- does not remove standalone skills under `~/.codex/skills`
- removes empty parent dirs like `~/.agents/plugins`, empty Caveman cache parents under `~/.codex/plugins/cache`, and `~/.agents` when they become empty

It leaves standalone installs untouched, including:

- `~/.codex/skills/caveman`
- `~/.codex/skills/compress`
- `~/.codex/skills/caveman-commit`
- `~/.codex/skills/caveman-help`
- `~/.codex/skills/caveman-review`

## Recommended path

### Best default

Use `skills-only` if you want lowest-friction install.

### Use plugin flow only when you specifically want:

- Codex Desktop local plugin marketplace integration
- one plugin container for all Caveman skills
- plugin-style install/uninstall lifecycle

## Verify install

### Skills-only

Check:

- `~/.codex/skills/caveman/SKILL.md`
- `~/.codex/skills/compress/SKILL.md`
- `~/.codex/skills/caveman-commit/SKILL.md`
- `~/.codex/skills/caveman-help/SKILL.md`
- `~/.codex/skills/caveman-review/SKILL.md`

Then restart Codex.

### Plugin install

Check:

- `~/.codex/plugins/caveman/.codex-plugin/plugin.json`
- `~/.codex/plugins/caveman/skills/caveman/SKILL.md`
- `~/.codex/plugins/caveman/skills/compress/SKILL.md`
- `~/.codex/plugins/caveman/skills/caveman-commit/SKILL.md`
- `~/.codex/plugins/caveman/skills/caveman-help/SKILL.md`
- `~/.codex/plugins/caveman/skills/caveman-review/SKILL.md`
- `~/.agents/plugins/marketplace.json`

Then:

1. Restart Codex.
2. Open plugin marketplace.
3. Install `Caveman` from `Local Plugins`.

## Troubleshooting

### Error: target already exists

You already have previous install or partial install at target path. Uninstall first, or remove conflicting install manually if you know what you are doing.

### Error: missing required command

Install missing dependency first. Script checks commands before doing real work.

### Marketplace entry exists but plugin not visible

Restart Codex first. Plugin flow depends on Codex reloading local marketplace after files land on disk.

### Prompt install vs script install

Use one method per target. Running prompt flow after script flow usually hits existing-path safety checks.

## Folder map

```text
installers/codex/macos/
├── expunge-all-caveman-files.sh
├── README.md
├── plugin-with-all-skills/
│   ├── install.sh
│   ├── prompt-install.md
│   ├── prompt-uninstall.md
│   └── uninstall.sh
└── skills-only/
    ├── install.sh
    ├── prompt-install.md
    ├── prompt-uninstall.md
    └── uninstall.sh
```
