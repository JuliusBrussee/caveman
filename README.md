<h1 align="center">caveman-codex</h1>

<p align="center">
  Codex-first port of <code>caveman</code>. Install as a <strong>Codex Desktop local plugin</strong> and trigger with <code>$caveman</code>.
</p>

---

This repository adapts the original **caveman** project for **OpenAI Codex Desktop** local-plugin installation.

For the full project overview, examples, features, and benchmarks, see the upstream README:

- https://github.com/JuliusBrussee/caveman#readme

## Install (Codex Desktop)

### One command

Run inside the project you want to enable Caveman for:

```bash
curl -fsSL https://raw.githubusercontent.com/yibie/caveman-codex/main/install.sh | bash
```

Or pass the target project explicitly:

```bash
curl -fsSL https://raw.githubusercontent.com/yibie/caveman-codex/main/install.sh | bash -s -- --project "/path/to/your/project"
```

The installer:

- Vendors the plugin into `<project>/.codex-plugins/caveman`
- Updates `<project>/.agents/plugins/marketplace.json` to reference `./.codex-plugins/caveman`

Then restart Codex Desktop and install `caveman` from the Plugins UI.

### macOS `.command`

If you cloned this repo locally:

```bash
/path/to/caveman-codex/install-codex-plugin.command "/path/to/your/project"
```

You can also double-click `install-codex-plugin.command` in Finder and choose the target folder.

## Usage

- Enable: `$caveman`
- Intensity: `$caveman lite`, `$caveman full`, `$caveman ultra`
- Disable: `stop caveman` or `normal mode`

## Notes

- This repo keeps Codex-specific install flow and docs. Upstream content stays upstream to avoid drift.
- If your Codex Desktop build or account only exposes official plugins, local plugin installation may still be blocked by the UI even when `marketplace.json` exists.

