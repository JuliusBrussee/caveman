# caveman — one-command hook installer for Claude Code (Windows PowerShell)
# Installs: SessionStart hook (auto-load rules) + UserPromptSubmit hook (mode tracking)
# Usage: powershell -ExecutionPolicy Bypass -File hooks\install.ps1
#   or:  powershell -ExecutionPolicy Bypass -File hooks\install.ps1 -Force
#   or (remote, no -Force support via pipe):
#        irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.6.0/hooks/install.ps1 | iex
#          For added safety, inspect first:
#          Invoke-WebRequest -Uri "..." -OutFile install.ps1; Get-Content install.ps1; Run manually
#   Note: irm ... | iex cannot pass -Force. For force reinstall, save the file and run with -File.
param(
    [switch]$Force
)

$ErrorActionPreference = "Stop"

# Require node
if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: 'node' is required to install the caveman hooks (used to merge" -ForegroundColor Red
    Write-Host "       the hook config into settings.json safely)." -ForegroundColor Red
    Write-Host "       Install Node.js from https://nodejs.org and re-run this script." -ForegroundColor Red
    exit 1
}

$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $env:USERPROFILE ".claude" }
$HooksDir = Join-Path $ClaudeDir "hooks"
$Settings = Join-Path $ClaudeDir "settings.json"
$RepoUrl = "https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.6.0/hooks"

$HookFiles = @("package.json", "caveman-config.js", "caveman-activate.js", "caveman-mode-tracker.js", "caveman-statusline.sh", "caveman-statusline.ps1", "caveman-tool-output.js")

# Resolve source — works from repo clone or remote
$ScriptDir = if ($PSScriptRoot) { $PSScriptRoot } else { $null }

# Check if already installed (unless -Force). Older installs only had two hook
# files, so require the full current set plus the hook registrations before we
# short-circuit.
if (-not $Force) {
    $AllFilesPresent = $true
    foreach ($hook in $HookFiles) {
        if (-not (Test-Path (Join-Path $HooksDir $hook))) {
            $AllFilesPresent = $false
            break
        }
    }

    $HooksWired = $false
    $HasStatusLine = $false
    if ($AllFilesPresent -and (Test-Path $Settings)) {
        try {
            $settingsObj = Get-Content $Settings -Raw | ConvertFrom-Json
            $hasCavemanHook = {
                param([string]$eventName)
                if (-not $settingsObj.hooks) { return $false }
                $entries = $settingsObj.hooks.$eventName
                if (-not $entries) { return $false }
                foreach ($entry in $entries) {
                    if ($entry.hooks) {
                        foreach ($hookDef in $entry.hooks) {
                            if ($hookDef.command -and $hookDef.command.Contains("caveman")) {
                                return $true
                            }
                        }
                    }
                }
                return $false
            }
            $HooksWired = (& $hasCavemanHook "SessionStart") -and (& $hasCavemanHook "UserPromptSubmit")
            $HasStatusLine = $null -ne $settingsObj.statusLine
        } catch {
            $HooksWired = $false
            $HasStatusLine = $false
        }
    }

    if ($AllFilesPresent -and $HooksWired -and $HasStatusLine) {
        Write-Host "Caveman hooks already installed in $HooksDir"
        Write-Host "  Re-run with -Force to overwrite: powershell -File hooks\install.ps1 -Force"
        Write-Host ""
        Write-Host "Nothing to do. Hooks are already in place."
        exit 0
    }
}

if ($Force -and (Test-Path (Join-Path $HooksDir "caveman-activate.js"))) {
    Write-Host "Reinstalling caveman hooks (-Force)..."
} else {
    Write-Host "Installing caveman hooks..."
}

# 1. Ensure hooks dir exists
if (-not (Test-Path $HooksDir)) {
    New-Item -ItemType Directory -Path $HooksDir -Force | Out-Null
}

# 2. Copy or download hook files (with SHA-256 integrity verification)
$TempDir = if ($env:TEMP) { $env:TEMP } else { "/tmp" }
$ChecksumsFile = Join-Path $TempDir "caveman-checksums-$(Get-Date -Format 'yyyyMMddHHmmss').tmp"

$DownloadChecksums = $false
if (-not $ScriptDir) {
    $DownloadChecksums = $true
}

if ($DownloadChecksums) {
    try {
        Invoke-WebRequest -Uri "$RepoUrl/checksums.sha256" -OutFile $ChecksumsFile -UseBasicParsing
    } catch {
        Write-Host "WARNING: Could not download checksums file. Proceeding without integrity verification." -ForegroundColor Yellow
    }
}

foreach ($hook in $HookFiles) {
    $dest = Join-Path $HooksDir $hook
    $localSource = if ($ScriptDir) { Join-Path $ScriptDir $hook } else { $null }

    if ($localSource -and (Test-Path $localSource)) {
        Copy-Item $localSource $dest -Force
    } else {
        Invoke-WebRequest -Uri "$RepoUrl/$hook" -OutFile $dest -UseBasicParsing

        if ($DownloadChecksums -and (Test-Path $ChecksumsFile)) {
            $expectedHash = $null
            try {
                $checksumContent = Get-Content $ChecksumsFile -Raw
                $matchedLine = $checksumContent -split "`n" | Where-Object { $_ -match $hook.Replace('.', '\.') } | Select-Object -First 1
                if ($matchedLine -match '^([a-fA-F0-9]{64})\s+\S+' ) {
                    $expectedHash = $matches[1]
                }
            } catch {}

            if ($expectedHash) {
                $actualHash = (Get-FileHash -Path $dest -Algorithm SHA256).Hash.ToLower()
                if ($actualHash -ne $expectedHash.ToLower()) {
                    Remove-Item $dest -Force -ErrorAction SilentlyContinue
                    Write-Host "ERROR: Integrity check failed for $hook" -ForegroundColor Red
                    Write-Host "  Expected: $expectedHash" -ForegroundColor Red
                    Write-Host "  Actual:   $actualHash" -ForegroundColor Red
                    exit 1
                }
            }
        }
    }
    Write-Host "  Installed: $dest"
}

if (Test-Path $ChecksumsFile) {
    Remove-Item $ChecksumsFile -Force -ErrorAction SilentlyContinue
}

# 3. Wire hooks + statusline into settings.json (idempotent)
if (-not (Test-Path $Settings)) {
    Set-Content -Path $Settings -Value "{}"
}

# Back up existing settings.json before touching it
Copy-Item $Settings "$Settings.bak" -Force

# Use node for safe JSON merging — pass paths via env vars to avoid injection
# if the username contains a single quote (e.g., O'Brien).
# Use a single-quote here-string so PowerShell does NOT expand $variables inside.
$env:CAVEMAN_SETTINGS = $Settings -replace '\\', '/'
$env:CAVEMAN_HOOKS_DIR = $HooksDir -replace '\\', '/'

$nodeScript = @'
const fs = require('fs');
const settingsPath = process.env.CAVEMAN_SETTINGS;
const hooksDir = process.env.CAVEMAN_HOOKS_DIR;
const managedStatusLinePath = hooksDir + '/caveman-statusline.ps1';
const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
if (!settings.hooks) settings.hooks = {};

// SessionStart
if (!settings.hooks.SessionStart) settings.hooks.SessionStart = [];
const hasStart = settings.hooks.SessionStart.some(e =>
  e.hooks && e.hooks.some(h => h.command && h.command.includes('caveman'))
);
if (!hasStart) {
  settings.hooks.SessionStart.push({
    hooks: [{
      type: 'command',
      command: 'node "' + hooksDir + '/caveman-activate.js"',
      timeout: 5,
      statusMessage: 'Loading caveman mode...'
    }]
  });
}

// UserPromptSubmit
if (!settings.hooks.UserPromptSubmit) settings.hooks.UserPromptSubmit = [];
const hasPrompt = settings.hooks.UserPromptSubmit.some(e =>
  e.hooks && e.hooks.some(h => h.command && h.command.includes('caveman'))
);
if (!hasPrompt) {
  settings.hooks.UserPromptSubmit.push({
    hooks: [{
      type: 'command',
      command: 'node "' + hooksDir + '/caveman-mode-tracker.js"',
      timeout: 5,
      statusMessage: 'Tracking caveman mode...'
    }]
  });
}

// ToolResult - post-process tool outputs to condense and redact
if (!settings.hooks.ToolResult) settings.hooks.ToolResult = [];
const hasToolResult = settings.hooks.ToolResult.some(e =>
  e.hooks && e.hooks.some(h => h.command && h.command.includes('caveman-tool-output'))
);
if (!hasToolResult) {
  settings.hooks.ToolResult.push({
    hooks: [{
      type: 'command',
      command: 'node "' + hooksDir + '/caveman-tool-output.js"',
      timeout: 10,
      statusMessage: 'Compressing tool output...'
    }]
  });
}

// Statusline
if (!settings.statusLine) {
  settings.statusLine = {
    type: 'command',
    command: 'powershell -ExecutionPolicy Bypass -File "' + managedStatusLinePath + '"'
  };
  console.log('  Statusline badge configured.');
} else {
  const cmd = typeof settings.statusLine === 'string'
    ? settings.statusLine
    : (settings.statusLine.command || '');
  if (cmd.includes(managedStatusLinePath)) {
    console.log('  Statusline badge already configured.');
  } else {
    console.log('  NOTE: Existing statusline detected - caveman badge NOT added.');
    console.log('        See hooks/README.md to add the badge to your existing statusline.');
  }
}

fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
console.log('  Hooks wired in settings.json');
'@

node -e $nodeScript

Write-Host ""
Write-Host "Done! Restart Claude Code to activate." -ForegroundColor Green
Write-Host ""
Write-Host "What's installed:"
Write-Host "  - SessionStart hook: auto-loads caveman rules every session"
Write-Host "  - Mode tracker hook: updates statusline badge when you switch modes"
Write-Host "    (/caveman lite, /caveman ultra, /caveman-commit, etc.)"
Write-Host "  - Statusline badge: shows [CAVEMAN] or [CAVEMAN:ULTRA] etc."
