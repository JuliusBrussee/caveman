# caveman — uninstaller for the SessionStart + UserPromptSubmit hooks (Windows PowerShell)
# Removes: hook files in ~/.claude/hooks, settings.json entries, and the flag file
# Usage: powershell -ExecutionPolicy Bypass -File src\hooks\uninstall.ps1
#   or:  irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/hooks/uninstall.ps1 | iex
param()

$ErrorActionPreference = "Stop"

$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $env:USERPROFILE ".claude" }
$HooksDir = Join-Path $ClaudeDir "hooks"
$Settings = Join-Path $ClaudeDir "settings.json"
$FlagFile = Join-Path $ClaudeDir ".caveman-active"

$HookFiles = @("package.json", "caveman-config.js", "caveman-parse.js", "caveman-activate.js", "caveman-mode-tracker.js", "caveman-stats.js", "caveman-statusline.sh", "caveman-statusline.ps1", "cavecrew-model-overrides.js")

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

        $nodeScript = @'
const fs = require('fs');
const path = require('path');
const settingsPath = process.env.CAVEMAN_SETTINGS;
const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));

// Own ONLY handlers whose command targets one of our exact script basenames.
// A bare 'caveman' substring also matches user-authored hooks that merely
// mention the word in a path (#593). Mirrors referencesManagedScript() in
// bin/lib/settings.js — keep the two in sync.
const MANAGED = new Set([
  'caveman-activate.js', 'caveman-mode-tracker.js', 'caveman-stats.js',
  'caveman-statusline.sh', 'caveman-statusline.ps1',
]);
const tokenize = (command) => {
  const out = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let m;
  while ((m = re.exec(command)) !== null) out.push(m[1] ?? m[2] ?? m[3]);
  return out;
};
// win32.basename splits on both / and \ so either slash form matches.
const isManaged = (command) => {
  if (typeof command !== 'string') return false;
  try {
    return tokenize(command).some(t => MANAGED.has(path.win32.basename(t)));
  } catch (e) { return false; }
};

let removed = 0;
if (settings.hooks) {
  for (const event of Object.keys(settings.hooks)) {
    if (!Array.isArray(settings.hooks[event])) continue;
    let removedHere = 0;
    // Filter the inner handler list, not the entry: a matcher group holding
    // one of ours AND a foreign hook must keep the foreign one.
    settings.hooks[event] = settings.hooks[event].filter(entry => {
      if (!entry || !Array.isArray(entry.hooks)) return true;
      const before = entry.hooks.length;
      entry.hooks = entry.hooks.filter(h => !(h && isManaged(h.command)));
      const n = before - entry.hooks.length;
      removedHere += n;
      return n === 0 || entry.hooks.length > 0;
    });
    removed += removedHere;
    if (removedHere > 0 && settings.hooks[event].length === 0) {
      delete settings.hooks[event];
    }
  }
  if (Object.keys(settings.hooks).length === 0) {
    delete settings.hooks;
  }
}

// Remove statusLine only when it points at our managed script. Matching by
// basename also survives the slash-form mismatch between a Windows-written
// settings.json and the forward-slashed env var.
if (settings.statusLine) {
  const cmd = typeof settings.statusLine === 'string'
    ? settings.statusLine
    : (settings.statusLine.command || '');
  if (isManaged(cmd)) {
    delete settings.statusLine;
    console.log('  Removed caveman statusLine from settings.json');
  }
}

fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
console.log('  Removed ' + removed + ' caveman hook entries from settings.json');
'@

        # Write the script to a temp file rather than passing it to `node -e`.
        # PowerShell's native-argument quoting mangles embedded double quotes,
        # so `node -e "<script>"` truncated at the first one under cmd quoting
        # (#249); install.ps1 was fixed to this shape in #250 and the
        # uninstaller must not drift back.
        $tmpScript = Join-Path $env:TEMP "caveman-uninstall-$([System.Diagnostics.Process]::GetCurrentProcess().Id).js"
        try {
            [System.IO.File]::WriteAllText($tmpScript, $nodeScript, [System.Text.Encoding]::UTF8)
            node $tmpScript
        } finally {
            if (Test-Path $tmpScript) { Remove-Item $tmpScript -Force }
        }

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
