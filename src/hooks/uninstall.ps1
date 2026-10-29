# caveman — uninstaller for the SessionStart + SubagentStart + UserPromptSubmit + SessionEnd hooks (Windows PowerShell)
# Removes: hook files in ~/.claude/hooks, settings.json entries, and the mode state
# Usage: powershell -ExecutionPolicy Bypass -File src\hooks\uninstall.ps1
#   or:  irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/hooks/uninstall.ps1 | iex
param()

$ErrorActionPreference = "Stop"

$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $env:USERPROFILE ".claude" }
$HooksDir = Join-Path $ClaudeDir "hooks"
$Settings = Join-Path $ClaudeDir "settings.json"
$SettingsHelper = ""
if ($PSScriptRoot) {
    $candidate = Join-Path $PSScriptRoot "../../installer/lib/settings.js"
    if (Test-Path -LiteralPath $candidate) { $SettingsHelper = $candidate }
}
$env:CAVEMAN_SETTINGS_HELPER = $SettingsHelper

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

# 1. Remove caveman entries from settings.json (idempotent)
#
# This runs BEFORE the hook files are deleted, on purpose. If the settings edit
# cannot complete, the install is still intact and the user can re-run or edit
# by hand. The other order left Claude Code pointing at deleted scripts, which
# is `Cannot find module ...caveman-activate.js` on every session start (#471).
if (Test-Path $Settings) {
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        # Abort - do NOT fall through to deleting the hook files. settings.json
        # still points at them, and removing them is exactly #471 on every
        # session start. Same reason the node exit code below aborts.
        Write-Host "ERROR: 'node' not found - cannot safely edit settings.json." -ForegroundColor Yellow
        Write-Host "       Nothing was removed. Install node and re-run, or remove the"
        Write-Host "       caveman SessionStart, SubagentStart, UserPromptSubmit, SessionEnd and statusLine"
        Write-Host "       entries from $Settings by hand first."
        exit 1
    } else {
        # Back up before editing, same policy as install.ps1: never overwrite an
        # existing .bak, which is the only pre-caveman copy the user has.
        $BackupMade = $false
        if (-not (Test-Path "$Settings.bak")) {
            Copy-Item $Settings "$Settings.bak"
            $BackupMade = $true
        }

        # Pass path via env var — avoids injection if username contains a single quote.
        # Use a single-quote here-string so PowerShell does NOT expand $variables inside.
        $env:CAVEMAN_SETTINGS = $Settings -replace '\\', '/'

        $nodeScript = @'
const fs = require('fs');
const path = require('path');
const settingsPath = process.env.CAVEMAN_SETTINGS;
// A settings.json with // comments is valid for Claude Code but not for
// JSON.parse. Bail out before touching anything rather than half-uninstalling:
// installer/install.js --uninstall handles JSONC properly.
let settings;
const shared = process.env.CAVEMAN_SETTINGS_HELPER ? require(process.env.CAVEMAN_SETTINGS_HELPER) : null;
try {
  settings = shared ? shared.readSettings(settingsPath) : JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
  if (!settings || typeof settings !== 'object' || Array.isArray(settings)) throw new Error('settings.json must be a readable object');
} catch (e) {
  console.error('  Cannot parse ' + settingsPath + ': ' + e.message);
  console.error('  Nothing was changed. If the file has // comments, run:');
  console.error('    npx -y github:JuliusBrussee/caveman -- --uninstall');
  process.exit(3);
}

// Own ONLY handlers whose command targets one of our exact script basenames.
// A bare 'caveman' substring also matches user-authored hooks that merely
// mention the word in a path (#593). Mirrors referencesManagedScript() in
// installer/lib/settings.js — keep the two in sync.
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

if (shared) shared.writeSettings(settingsPath, settings);
else fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
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

        # $ErrorActionPreference does not trip on a native exit code, so check
        # it by hand: without this, a failed edit still fell through and deleted
        # the user's only pre-caveman backup.
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Aborting: settings.json was not modified and nothing was removed." -ForegroundColor Yellow
            exit 1
        }

        # Clean up the backup this run made. A .bak that was already there is the
        # user's (or install's pre-caveman copy) and stays. So does ours when it
        # is the only copy of comments the rewrite dropped, i.e. not plain JSON.
        $env:CAVEMAN_BAK = ($Settings -replace '\\', '/') + '.bak'
        $BackupIsPlainJson = $false
        if ($BackupMade -and (Test-Path "$Settings.bak")) {
            # Windows PowerShell 5.1 turns a native command's stderr into error
            # records, and this script runs under $ErrorActionPreference = "Stop",
            # which makes them TERMINATING. The probe's failure is its answer, not
            # an error: a JSONC backup makes JSON.parse throw, node prints the
            # stack trace to stderr, and the script died there — before the
            # explicit `exit 0` below could set a status, which is why neither
            # that nor clearing $LASTEXITCODE moved the powershell.exe lane. pwsh
            # 7 does not raise on native stderr, so only 5.1 ever saw it. Scope
            # the preference around the probe; the rest of the script keeps Stop.
            $PreviousEAP = $ErrorActionPreference
            $ErrorActionPreference = "Continue"
            try {
                # $LASTEXITCODE is 0 from the rewrite above, and a node that cannot be
                # launched at all leaves it untouched — which would read as "plain JSON"
                # and delete the backup. Fail safe: only an exit code node itself wrote
                # may clear it.
                $global:LASTEXITCODE = 1
                node -e "JSON.parse(require('fs').readFileSync(process.env.CAVEMAN_BAK, 'utf8'))" 2>$null
                $BackupIsPlainJson = ($LASTEXITCODE -eq 0)
            } catch {
                $BackupIsPlainJson = $false  # unreadable probe: keep the backup
            } finally {
                $ErrorActionPreference = $PreviousEAP
            }
            # The probe is a predicate, not an operation: a JSONC backup makes it
            # exit 1 on a perfectly successful uninstall. Its result is consumed
            # above, so keep the variable honest for anything that reads it later.
            # This alone does NOT fix the script's exit status: assigning
            # $LASTEXITCODE updates the variable, not the engine's record of the
            # last native command, which is what `powershell.exe -File` returns.
            # The explicit `exit 0` at the end of this script is what does that.
            $global:LASTEXITCODE = 0
        }
        if ($BackupIsPlainJson) {
            Remove-Item "$Settings.bak" -Force
            Write-Host "  Removed: $Settings.bak"
        }
    }
}

# 2. Remove hook files (only now that settings.json no longer points at them)
$RemovedFiles = 0
foreach ($hook in $HookFiles) {
    $path = Join-Path $HooksDir $hook
    if (Test-Path $path) {
        if ($hook -eq "package.json") {
            $OwnedManifest = $false
            # ConvertFrom-Json unwraps one-element arrays and accepts comments
            # on newer PowerShell. Use the same strict ownership check as Node.
            if (Get-Command node -ErrorAction SilentlyContinue) {
                $env:CAVEMAN_MANIFEST_PATH = $path
                try {
                    @'
try {
  const value = JSON.parse(require('fs').readFileSync(process.env.CAVEMAN_MANIFEST_PATH, 'utf8'));
  process.exit(value && value.type === 'commonjs' && Object.keys(value).length === 1 ? 0 : 1);
} catch (_) { process.exit(1); }
'@ | node --input-type=commonjs
                    $OwnedManifest = $LASTEXITCODE -eq 0
                } catch {}
            }
            if (-not $OwnedManifest) {
                Write-Host "  Preserved foreign manifest: $path"
                continue
            }
        }
        Remove-Item $path -Force
        Write-Host "  Removed: $path"
        $RemovedFiles++
    }
}

if ($RemovedFiles -eq 0) {
    Write-Host "  No hook files found in $HooksDir"
}

# 3. Remove mode state
#
# .caveman-history.jsonl is deliberately NOT removed: it is the user's
# accumulated lifetime savings record, not caveman plumbing. Keep this list in
# sync with the uninstall block in installer/install.js.
foreach ($state in @(".caveman-active", ".caveman-active.prev", ".caveman-mode-log.jsonl", ".caveman-statusline-suffix", ".caveman-nudge-shown", ".caveman-statusline-stale")) {
    $StatePath = Join-Path $ClaudeDir $state
    if (Test-Path $StatePath) {
        Remove-Item $StatePath -Force
        Write-Host "  Removed: $StatePath"
    }
}

# Per-session mode files (one <session_id>.mode / .prev per window)
$SessionsDir = Join-Path $ClaudeDir ".caveman-sessions"
if (Test-Path $SessionsDir) {
    Remove-Item $SessionsDir -Recurse -Force
    Write-Host "  Removed: $SessionsDir"
}

Write-Host ""
Write-Host "Done! Restart Claude Code to complete the uninstall." -ForegroundColor Green

# Guidance for other agents
Write-Host ""
Write-Host "Other agents:"
Write-Host "  npx skills remove JuliusBrussee/caveman -g  # Cursor, Windsurf, Cline, Copilot, etc."
Write-Host "  claude plugin disable caveman   # Claude Code plugin"
Write-Host "  gemini extensions uninstall caveman  # Gemini CLI"

# Pin the success status. Every failure path above exits explicitly, so reaching
# here means success -- but the .bak probe deliberately runs a node that exits 1
# whenever the backup holds the comments the rewrite dropped, and Windows
# PowerShell 5.1 (`powershell.exe -File`, still the default shell on Windows)
# ends a script with the last native command's exit code. A fully successful
# uninstall of a JSONC settings.json therefore reported failure. pwsh 7 exits 0
# in the same position, which is why only the powershell.exe lane catches it.
exit 0
