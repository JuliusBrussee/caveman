# caveman — uninstaller for the SessionStart + UserPromptSubmit hooks (Windows PowerShell)
# Removes: hook files in ~/.claude/hooks, settings.json entries, and the flag file
# Usage: powershell -ExecutionPolicy Bypass -File hooks\uninstall.ps1
#   or:  irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/uninstall.ps1 | iex
param()

$ErrorActionPreference = "Stop"

$ClaudeDir = Join-Path $env:USERPROFILE ".claude"
$HooksDir = Join-Path $ClaudeDir "hooks"
$Settings = Join-Path $ClaudeDir "settings.json"
$FlagFile = Join-Path $ClaudeDir ".caveman-active"

$HookFiles = @("caveman-config.js", "caveman-activate.js", "caveman-mode-tracker.js", "caveman-statusline.sh", "caveman-statusline.ps1")

# Detect if caveman is installed as a plugin
$PluginInstalled = $false
$PluginsDir = Join-Path $ClaudeDir "plugins"
if (Test-Path $PluginsDir) {
    $found = Get-ChildItem -Path $PluginsDir -Recurse -Filter "plugin.json" -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -match "caveman" }
    if ($found) { $PluginInstalled = $true }
}

if ($PluginInstalled) {
    Write-Host "Caveman appears to be installed as a Claude Code plugin." -ForegroundColor Yellow
    Write-Host "To uninstall the plugin, run:"
    Write-Host ""
    Write-Host "  claude plugin disable caveman" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "This script removes standalone hooks (installed via install.ps1)."
    Write-Host "Continuing with standalone hook removal..."
    Write-Host ""
}

Write-Host "Uninstalling caveman hooks..."

# 1. Remove hook files
$RemovedFiles = 0
foreach ($hook in $HookFiles) {
    $path = Join-Path $HooksDir $hook
    if (Test-Path $path) {
        Remove-Item $path -Force
        Write-Host "  Removed: $path"
        $RemovedFiles++
    }
}

if ($RemovedFiles -eq 0) {
    Write-Host "  No hook files found in $HooksDir"
}

# 2. Remove caveman entries from settings.json (idempotent)
if (Test-Path $Settings) {
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        Write-Host "WARNING: 'node' not found - cannot safely edit settings.json." -ForegroundColor Yellow
        Write-Host "         Remove the caveman SessionStart and UserPromptSubmit"
        Write-Host "         entries from $Settings manually."
    } else {
        # Back up before editing
        Copy-Item $Settings "$Settings.bak" -Force

        # Pass path via env var — avoids injection if username contains a single quote.
        # Use a single-quote here-string so PowerShell does NOT expand $variables inside.
        $env:CAVEMAN_SETTINGS = $Settings -replace '\\', '/'
        $env:CAVEMAN_HOOKS_DIR = $HooksDir -replace '\\', '/'

        $nodeScript = @'
const fs = require('fs');
const settingsPath = process.env.CAVEMAN_SETTINGS;
const hooksDir = process.env.CAVEMAN_HOOKS_DIR;
const managedStatusLinePath = hooksDir + '/caveman-statusline.ps1';
const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));

const isCavemanEntry = (entry) =>
  entry && entry.hooks && entry.hooks.some(h =>
    h.command && h.command.includes('caveman')
  );

let removed = 0;
if (settings.hooks) {
  for (const event of ['SessionStart', 'UserPromptSubmit']) {
    if (Array.isArray(settings.hooks[event])) {
      const before = settings.hooks[event].length;
      settings.hooks[event] = settings.hooks[event].filter(e => !isCavemanEntry(e));
      removed += before - settings.hooks[event].length;
      if (settings.hooks[event].length === 0) {
        delete settings.hooks[event];
      }
    }
  }
  if (Object.keys(settings.hooks).length === 0) {
    delete settings.hooks;
  }
}

if (settings.statusLine) {
  const cmd = typeof settings.statusLine === 'string'
    ? settings.statusLine
    : (settings.statusLine.command || '');
  if (cmd.includes(managedStatusLinePath)) {
    delete settings.statusLine;
    console.log('  Removed caveman statusLine from settings.json');
  }
}

fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
console.log('  Removed ' + removed + ' caveman hook entries from settings.json');
'@

        node -e $nodeScript

        # Clean up backup file left by installer
        if (Test-Path "$Settings.bak") {
            Remove-Item "$Settings.bak" -Force
            Write-Host "  Removed: $Settings.bak"
        }
    }
}

# 3. Remove flag file
if (Test-Path $FlagFile) {
    Remove-Item $FlagFile -Force
    Write-Host "  Removed: $FlagFile"
}

Write-Host ""
Write-Host "Done! Restart Claude Code to complete the uninstall." -ForegroundColor Green

# Guidance for other agents
Write-Host ""
Write-Host "Other agents:"
Write-Host "  npx skills remove caveman      # Cursor, Windsurf, Cline, Copilot, etc."
Write-Host "  claude plugin disable caveman   # Claude Code plugin"
Write-Host "  gemini extensions uninstall caveman  # Gemini CLI"
