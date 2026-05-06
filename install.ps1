# caveman — smart multi-agent installer (Windows / PowerShell).
#
# One line:
#   irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
#
# Detects which AI coding agents are on your machine and installs caveman for
# each one using its native distribution (plugin / extension / skill / rule
# file). Skips agents that aren't installed. Safe to re-run — each underlying
# install command is idempotent.
#
# Run `install.ps1 -Help` for the full reference (flags + agent matrix).
#
# Defaults: -WithHooks ON, -WithMcpShrink ON (when Claude Code is detected),
# -WithInit OFF. Use -Minimal to skip everything except the plugin/extension
# install. Use -All to also drop per-repo rule files into $PWD.

[CmdletBinding()]
param(
  [switch]$DryRun,
  [switch]$Force,
  [switch]$SkipSkills,
  [switch]$WithHooks,
  [switch]$NoHooks,
  [switch]$WithInit,
  [switch]$WithMcpShrink,
  [switch]$NoMcpShrink,
  [switch]$All,
  [switch]$Minimal,
  [switch]$List,
  [switch]$NoColor,
  [switch]$Help,
  [string[]]$Only = @()
)

$ErrorActionPreference = "Stop"
$Repo = "JuliusBrussee/caveman"
$RawBase = "https://raw.githubusercontent.com/$Repo/main"
$HooksInstallUrl = "$RawBase/hooks/install.ps1"
$InitScriptUrl = "$RawBase/tools/caveman-init.js"
$McpShrinkPkg = "caveman-shrink"

# ── Help ────────────────────────────────────────────────────────────────────
if ($Help) {
@"
caveman installer (Windows) — detects your agents and installs caveman for each.

USAGE
  install.ps1 [-DryRun] [-Force] [-Only <agent>[,<agent>]] [-All] [-Minimal]
              [-WithHooks] [-NoHooks] [-WithInit] [-WithMcpShrink] [-NoMcpShrink]
              [-SkipSkills] [-List] [-NoColor]

  irm $RawBase/install.ps1 | iex

FLAGS
  -DryRun          Print what would run, do nothing.
  -Force           Re-run even if a target reports "already installed".
  -Only <list>     Comma-separated agent ids. Repeatable / array.
  -All             Turn on -WithHooks, -WithInit, -WithMcpShrink together.
  -Minimal         Skip hooks, MCP shrink, per-repo init. Plugin/extension only.
  -WithHooks       Claude Code: also wire SessionStart/UserPromptSubmit hooks
                   + statusline + stats badge. ON by default.
  -NoHooks         Opt out of the default-on hooks install.
  -WithMcpShrink   Claude Code: register caveman-shrink MCP proxy. ON by default.
  -NoMcpShrink     Opt out of the default-on MCP shrink registration.
  -WithInit        Drop per-repo rule files into `$PWD for Cursor / Windsurf /
                   Cline / Copilot / AGENTS.md. OFF by default.
  -SkipSkills      Don't run the npx-skills auto-detect fallback.
  -List            Print the full provider matrix and exit.
  -NoColor         Disable ANSI color codes.

EXAMPLES
  install.ps1                          # default: plugin + hooks + MCP shrink
  install.ps1 -All                     # also drop per-repo rule files
  install.ps1 -Minimal                 # plugin/extension only
  install.ps1 -DryRun -All
  install.ps1 -Only claude -WithMcpShrink
  install.ps1 -Only cursor,windsurf -WithInit
  install.ps1 -List

URLS THE INSTALLER MAY FETCH FROM
  $RawBase/install.ps1
  $RawBase/hooks/install.ps1
  $RawBase/tools/caveman-init.js
"@ | Write-Host
  exit 0
}

# ── Resolve -All / -Minimal / default-auto switches ────────────────────────
if ($All -and $Minimal) {
  Write-Error "-All and -Minimal are mutually exclusive."
  exit 2
}
if ($All) {
  $WithHooks = $true
  $WithInit = $true
  $WithMcpShrink = $true
}
# Default-auto: turn ON unless caller passed -Minimal or the explicit -No*
# opt-out switch.
if (-not $WithHooks -and -not $NoHooks -and -not $Minimal) {
  $WithHooks = $true
}
if (-not $WithMcpShrink -and -not $NoMcpShrink -and -not $Minimal) {
  $WithMcpShrink = $true
}
if ($Minimal) {
  $WithHooks = $false
  $WithMcpShrink = $false
  $WithInit = $false
}

# ── Color helpers ──────────────────────────────────────────────────────────
$Esc = [char]27
function Say($msg) {
  if ($NoColor) { Write-Host $msg }
  else { Write-Host "$Esc[38;5;172m$msg$Esc[0m" }
}
function Note($msg) {
  if ($NoColor) { Write-Host $msg }
  else { Write-Host "$Esc[2m$msg$Esc[0m" }
}
function Warn($msg) {
  if ($NoColor) { Write-Host $msg }
  else { Write-Host "$Esc[31m$msg$Esc[0m" }
}
function Ok($msg) {
  if ($NoColor) { Write-Host $msg }
  else { Write-Host "$Esc[32m$msg$Esc[0m" }
}

# ── State ───────────────────────────────────────────────────────────────────
$OnlyList = @()
foreach ($o in $Only) {
  foreach ($x in ($o -split ',')) {
    $t = $x.Trim()
    if ($t) {
      # Backward-compat alias (matches install.sh).
      if ($t -eq "aider") { $t = "aider-desk" }
      $OnlyList += $t
    }
  }
}

$InstalledIds = @()
$SkippedIds = @()
$SkippedWhy = @()
$FailedIds = @()
$FailedWhy = @()
$DetectedCount = 0

function Want([string]$id) {
  if ($OnlyList.Count -eq 0) { return $true }
  return $OnlyList -contains $id
}

function Has-Cmd([string]$c) {
  return [bool](Get-Command $c -ErrorAction SilentlyContinue)
}

# Detect repo root if running from a clone (vs irm | iex from raw.github).
function Get-RepoRoot {
  $src = $PSCommandPath
  if ($src -and (Test-Path $src)) {
    $d = Split-Path -Parent $src
    if ((Test-Path (Join-Path $d "install.ps1")) -and
        (Test-Path (Join-Path $d "hooks")) -and
        (Test-Path (Join-Path $d "tools"))) {
      return $d
    }
  }
  return $null
}
$RepoRoot = Get-RepoRoot

# ── Run helpers ─────────────────────────────────────────────────────────────
# Run a process, return $true if exit 0. Honors -DryRun. Errors do not throw.
# `$Args` is an automatic in PowerShell — name the param `$Argv` to avoid the
# implicit-collision warning under strict analysis.
function Try-Run {
  param([string]$Exe, [string[]]$Argv)
  if ($DryRun) {
    Note "  would run: $Exe $($Argv -join ' ')"
    return $true
  }
  Write-Host "  $ $Exe $($Argv -join ' ')"
  try {
    & $Exe @Argv
    return ($LASTEXITCODE -eq 0)
  } catch {
    Write-Host "  $($_.Exception.Message)" -ForegroundColor Red
    return $false
  }
}

function Record-Installed([string]$id) { $script:InstalledIds += $id }
function Record-Skipped([string]$id, [string]$why) {
  $script:SkippedIds += $id
  $script:SkippedWhy += $why
}
function Record-Failed([string]$id, [string]$why) {
  $script:FailedIds += $id
  $script:FailedWhy += $why
}

function Ensure-Node {
  if ((Has-Cmd "node") -and (Has-Cmd "npx")) { return $true }
  Warn "  node + npx required for this target — install Node.js (https://nodejs.org) and re-run."
  return $false
}

# ── Detection helpers ───────────────────────────────────────────────────────
$VsCodeExtRoots = @(
  (Join-Path $HOME ".vscode\extensions"),
  (Join-Path $HOME ".vscode-server\extensions"),
  (Join-Path $HOME ".cursor\extensions"),
  (Join-Path $HOME ".windsurf\extensions")
)

function Test-VscodeExt([string]$needle) {
  foreach ($r in $VsCodeExtRoots) {
    if (Test-Path $r) {
      $found = Get-ChildItem -Path $r -ErrorAction SilentlyContinue |
               Where-Object { $_.Name -match [regex]::Escape($needle) }
      if ($found) { return $true }
    }
  }
  return $false
}

function Test-CursorExt([string]$needle) {
  $r = Join-Path $HOME ".cursor\extensions"
  if (Test-Path $r) {
    $found = Get-ChildItem -Path $r -ErrorAction SilentlyContinue |
             Where-Object { $_.Name -match [regex]::Escape($needle) }
    if ($found) { return $true }
  }
  return $false
}

# JetBrains config roots: Windows uses %APPDATA%\JetBrains, but cover the WSL
# bridge (~/.config/JetBrains) and macOS-on-PowerShell-Core path too so users
# running pwsh on different OSes get the same matrix.
$JetbrainsRoots = @(
  (Join-Path $env:APPDATA "JetBrains"),
  (Join-Path $HOME ".config\JetBrains"),
  (Join-Path $HOME "Library/Application Support/JetBrains")
)

function Test-JetbrainsConfig {
  foreach ($r in $JetbrainsRoots) {
    if ($r -and (Test-Path $r)) { return $true }
  }
  return $false
}

function Test-JetbrainsPlugin([string]$needle) {
  foreach ($r in $JetbrainsRoots) {
    if ($r -and (Test-Path $r)) {
      $found = Get-ChildItem -Path $r -Recurse -Directory -Depth 4 -ErrorAction SilentlyContinue |
               Where-Object { $_.Name -match [regex]::Escape($needle) }
      if ($found) { return $true }
    }
  }
  return $false
}

# Resolve a detect spec like "command:foo||dir:~/.bar||vscode-ext:baz".
# Spec strings use $HOME / $env:HOME tokens that we expanded at build time —
# they're already absolute by the time they reach this function.
function Resolve-DetectSpec([string]$spec) {
  if ([string]::IsNullOrWhiteSpace($spec)) { return $false }
  foreach ($clause in ($spec -split '\|\|')) {
    $c = $clause.Trim()
    if (-not $c) { continue }
    if ($c -match '^command:(.+)$')          { if (Has-Cmd $matches[1]) { return $true } }
    elseif ($c -match '^dir:(.+)$')          { if (Test-Path $matches[1] -PathType Container) { return $true } }
    elseif ($c -match '^file:(.+)$')         { if (Test-Path $matches[1] -PathType Leaf) { return $true } }
    elseif ($c -match '^vscode-ext:(.+)$')   { if (Test-VscodeExt $matches[1]) { return $true } }
    elseif ($c -match '^cursor-ext:(.+)$')   { if (Test-CursorExt $matches[1]) { return $true } }
    elseif ($c -eq 'jetbrains-config')       { if (Test-JetbrainsConfig)        { return $true } }
    elseif ($c -match '^jetbrains-plugin:(.+)$') { if (Test-JetbrainsPlugin $matches[1]) { return $true } }
  }
  return $false
}

# ── Provider matrix (mirror of install.sh PROVIDER_*) ──────────────────────
# Keep this aligned with install.sh row-for-row. Columns:
#   id, label, profile (npx-skills slug or empty for non-skills), detect,
#   soft (1 = config-dir-only probe, no CLI on PATH).
$Providers = @(
  @{ id='claude';      label='Claude Code';        profile='';             detect='command:claude'; soft=0 },
  @{ id='gemini';      label='Gemini CLI';         profile='';             detect='command:gemini'; soft=0 },
  @{ id='codex';       label='Codex CLI';          profile='codex';        detect='command:codex'; soft=0 },
  @{ id='cursor';      label='Cursor';             profile='cursor';       detect="command:cursor||dir:$HOME\.cursor"; soft=0 },
  @{ id='windsurf';    label='Windsurf';           profile='windsurf';     detect="command:windsurf||dir:$HOME\.codeium\windsurf||dir:$HOME\.windsurf"; soft=0 },
  @{ id='cline';       label='Cline';              profile='cline';        detect='vscode-ext:cline'; soft=0 },
  @{ id='copilot';     label='GitHub Copilot';     profile='github-copilot'; detect='command:gh'; soft=0 },
  @{ id='continue';    label='Continue';           profile='continue';     detect='vscode-ext:continue.continue||vscode-ext:continue'; soft=0 },
  @{ id='kilo';        label='Kilo Code';          profile='kilo';         detect="vscode-ext:kilocode||dir:$HOME\.kilocode"; soft=0 },
  @{ id='roo';         label='Roo Code';           profile='roo';          detect='vscode-ext:roo||vscode-ext:rooveterinaryinc.roo-cline||cursor-ext:roo'; soft=0 },
  @{ id='augment';     label='Augment Code';       profile='augment';      detect='vscode-ext:augment||jetbrains-plugin:augment'; soft=0 },
  @{ id='aider-desk';  label='Aider Desk';         profile='aider-desk';   detect="command:aider||dir:$HOME\.aider-desk"; soft=0 },
  @{ id='amp';         label='Sourcegraph Amp';    profile='amp';          detect='command:amp'; soft=0 },
  @{ id='bob';         label='IBM Bob';            profile='bob';          detect="command:bob||dir:$HOME\.bob"; soft=0 },
  @{ id='crush';       label='Crush';              profile='crush';        detect="command:crush||dir:$HOME\.config\crush"; soft=0 },
  @{ id='devin';       label='Devin (terminal)';   profile='devin';        detect="command:devin||dir:$HOME\.config\devin"; soft=0 },
  @{ id='droid';       label='Droid (Factory)';    profile='droid';        detect="command:droid||dir:$HOME\.factory"; soft=0 },
  @{ id='forgecode';   label='ForgeCode';          profile='forgecode';    detect="command:forge||dir:$HOME\.forge"; soft=0 },
  @{ id='goose';       label='Block Goose';        profile='goose';        detect="command:goose||dir:$HOME\.config\goose"; soft=0 },
  @{ id='iflow';       label='iFlow CLI';          profile='iflow-cli';    detect="command:iflow||dir:$HOME\.iflow"; soft=0 },
  @{ id='junie';       label='JetBrains Junie';    profile='junie';        detect="dir:$HOME\.junie||jetbrains-plugin:junie"; soft=1 },
  @{ id='kiro';        label='Kiro CLI';           profile='kiro-cli';     detect="command:kiro||dir:$HOME\.kiro"; soft=0 },
  @{ id='mistral';     label='Mistral Vibe';       profile='mistral-vibe'; detect="command:mistral||dir:$HOME\.vibe"; soft=0 },
  @{ id='openhands';   label='OpenHands';          profile='openhands';    detect="command:openhands||dir:$HOME\.openhands"; soft=0 },
  @{ id='opencode';    label='opencode';           profile='opencode';     detect="command:opencode||file:$HOME\.config\opencode\AGENTS.md"; soft=0 },
  @{ id='qwen';        label='Qwen Code';          profile='qwen-code';    detect="command:qwen||dir:$HOME\.qwen"; soft=0 },
  @{ id='qoder';       label='Qoder';              profile='qoder';        detect="dir:$HOME\.qoder"; soft=1 },
  @{ id='rovodev';     label='Atlassian Rovo Dev'; profile='rovodev';      detect="command:rovodev||dir:$HOME\.rovodev"; soft=0 },
  @{ id='tabnine';     label='Tabnine CLI';        profile='tabnine-cli';  detect="command:tabnine||dir:$HOME\.tabnine"; soft=0 },
  @{ id='trae';        label='Trae';               profile='trae';         detect="command:trae||dir:$HOME\.trae"; soft=0 },
  @{ id='warp';        label='Warp';               profile='warp';         detect="command:warp||dir:$HOME\.warp"; soft=0 },
  @{ id='replit';      label='Replit Agent';       profile='replit';       detect="command:replit||dir:$HOME\.replit"; soft=0 },
  @{ id='antigravity'; label='Google Antigravity'; profile='antigravity';  detect="dir:$HOME\.gemini\antigravity"; soft=1 }
)

# ── -List output ────────────────────────────────────────────────────────────
if ($List) {
  Say "🪨 caveman provider matrix"
  Write-Host ""
  Write-Host ("  {0,-13} {1,-22} {2}" -f "ID", "AGENT", "INSTALL MECHANISM")
  Write-Host ("  {0,-13} {1,-22} {2}" -f "----", "-----", "-----------------")
  foreach ($p in $Providers) {
    if ([string]::IsNullOrEmpty($p.profile)) {
      $mech = if ($p.id -eq 'claude') { 'claude plugin install' }
              elseif ($p.id -eq 'gemini') { 'gemini extensions install' }
              else { '' }
    } else {
      $mech = "npx skills add ($($p.profile))"
    }
    if ($p.soft -eq 1) { $mech += ' (soft)' }
    Write-Host ("  {0,-13} {1,-22} {2}" -f $p.id, $p.label, $mech)
  }
  Write-Host ""
  Note "  Detection probes per agent live in install.ps1 \$Providers."
  Note "  Soft entries detect via config-dir presence only (no CLI on PATH)."
  Write-Host ""
  Note "  Defaults: -WithHooks ON, -WithMcpShrink ON, -WithInit OFF."
  Note "  -All turns all three on, -Minimal turns all three off."
  exit 0
}

# ── Banner ──────────────────────────────────────────────────────────────────
Say "🪨 caveman installer"
Note "  $Repo"
if ($DryRun) { Note "  (dry run — nothing will be written)" }
Write-Host ""

# ── Per-agent install functions ─────────────────────────────────────────────
function Install-Claude {
  $script:DetectedCount++
  Say "→ Claude Code detected"
  $pluginDone = $false

  $alreadyInstalled = $false
  if (-not $Force) {
    try {
      $list = & claude plugin list 2>$null
      if ($list -match "(?i)caveman") { $alreadyInstalled = $true }
    } catch {}
  }
  if ($alreadyInstalled) {
    Note "  caveman plugin already installed (use -Force to reinstall)"
    Record-Skipped "claude" "plugin already installed"
    $pluginDone = $true
  } else {
    if ((Try-Run "claude" @("plugin", "marketplace", "add", $Repo)) -and
        (Try-Run "claude" @("plugin", "install", "caveman@caveman"))) {
      Record-Installed "claude"
      $pluginDone = $true
    } else {
      Record-Failed "claude" "claude plugin install failed"
    }
  }

  # -WithHooks: also run the standalone hooks installer (PowerShell variant).
  if ($WithHooks) {
    Say "  → installing standalone hooks (-WithHooks)"
    $hooksArgs = @()
    if ($Force) { $hooksArgs += "-Force" }

    $localPs1 = $null
    if ($RepoRoot) {
      $candidate = Join-Path $RepoRoot "hooks\install.ps1"
      if (Test-Path $candidate) { $localPs1 = $candidate }
    }

    if ($DryRun) {
      if ($localPs1) {
        Note "    would run: powershell -ExecutionPolicy Bypass -File $localPs1 $($hooksArgs -join ' ')"
      } else {
        Note "    would run: irm $HooksInstallUrl | iex (with -Force=$Force)"
      }
    } else {
      try {
        if ($localPs1) {
          & powershell -ExecutionPolicy Bypass -File $localPs1 @hooksArgs
          if ($LASTEXITCODE -eq 0) { Record-Installed "claude-hooks" }
          else { Record-Failed "claude-hooks" "hooks/install.ps1 exit $LASTEXITCODE" }
        } else {
          # Save to temp + run with -File so -Force works (irm | iex can't pass args).
          $tmp = Join-Path $env:TEMP "caveman-hooks-install-$([Guid]::NewGuid()).ps1"
          Invoke-WebRequest -Uri $HooksInstallUrl -OutFile $tmp -UseBasicParsing
          try {
            & powershell -ExecutionPolicy Bypass -File $tmp @hooksArgs
            if ($LASTEXITCODE -eq 0) { Record-Installed "claude-hooks" }
            else { Record-Failed "claude-hooks" "remote hooks installer exit $LASTEXITCODE" }
          } finally {
            Remove-Item $tmp -Force -ErrorAction SilentlyContinue
          }
        }
      } catch {
        Record-Failed "claude-hooks" $_.Exception.Message
      }
    }
  }

  # -WithMcpShrink: register the proxy. Probe npm first so a transient
  # registry outage downgrades to a clean manual-config skip instead of
  # registering an `npx -y caveman-shrink` entry that 404s on every spawn.
  if ($WithMcpShrink) {
    Say "  → wiring caveman-shrink MCP proxy (-WithMcpShrink)"
    if (Has-Cmd "npm") {
      $packageOnNpm = $false
      try { $null = & npm view $McpShrinkPkg 2>$null; $packageOnNpm = ($LASTEXITCODE -eq 0) } catch {}
      if (-not $packageOnNpm) {
        Warn "    'npm view $McpShrinkPkg' returned no metadata — registry unreachable or package missing."
        Note "    Skipping registration. Re-run -WithMcpShrink when the registry is reachable,"
        Note "    or copy the snippet below into your MCP config and point it at a local clone."
        Record-Skipped "caveman-shrink" "npm registry probe failed"
        Write-Host ""
        return
      }
    }
    $hasMcpAdd = $false
    if (Has-Cmd "claude") {
      try { $null = & claude mcp --help 2>$null; $hasMcpAdd = ($LASTEXITCODE -eq 0) } catch {}
    }
    if ($hasMcpAdd) {
      if ($DryRun) {
        Note "    would run: claude mcp add caveman-shrink -- npx -y $McpShrinkPkg"
      } else {
        if (Try-Run "claude" @("mcp", "add", "caveman-shrink", "--", "npx", "-y", $McpShrinkPkg)) {
          Record-Installed "caveman-shrink"
          Note "    registered. wrap an upstream by editing the mcpServers entry — see:"
          Note "    https://github.com/$Repo/tree/main/mcp-servers/caveman-shrink"
        } else {
          Record-Failed "caveman-shrink" "claude mcp add failed"
        }
      }
    } else {
      Note "    'claude mcp add' not available on this CLI. Add this snippet to your"
      Note "    Claude Code MCP config (settings.json or .mcp.json) manually:"
      Write-Host ""
      Write-Host '    {'
      Write-Host '      "mcpServers": {'
      Write-Host '        "fs-shrunk": {'
      Write-Host '          "command": "npx",'
      Write-Host '          "args": ['
      Write-Host '            "caveman-shrink",'
      Write-Host '            "npx", "@modelcontextprotocol/server-filesystem", "C:\\path\\to\\dir"'
      Write-Host '          ]'
      Write-Host '        }'
      Write-Host '      }'
      Write-Host '    }'
      Write-Host ""
      Record-Skipped "caveman-shrink" "manual config required (snippet printed)"
    }
  }
  Write-Host ""
}

function Install-Gemini {
  $script:DetectedCount++
  Say "→ Gemini CLI detected"
  $alreadyInstalled = $false
  if (-not $Force) {
    try {
      $list = & gemini extensions list 2>$null
      if ($list -match "(?i)caveman") { $alreadyInstalled = $true }
    } catch {}
  }
  if ($alreadyInstalled) {
    Note "  caveman extension already installed (use -Force to reinstall)"
    Record-Skipped "gemini" "extension already installed"
  } else {
    if (Try-Run "gemini" @("extensions", "install", "https://github.com/$Repo")) {
      Record-Installed "gemini"
    } else {
      Record-Failed "gemini" "gemini extensions install failed"
    }
  }
  Write-Host ""
}

function Install-ViaSkills {
  param([string]$id, [string]$label, [string]$profile)
  $script:DetectedCount++
  Say "→ $label detected"
  if (-not (Ensure-Node)) {
    Record-Failed $id "node/npx missing"
    Write-Host ""
    return
  }
  $skillsArgs = @("-y", "skills", "add", $Repo)
  if ($profile) { $skillsArgs += @("-a", $profile) }
  if (Try-Run "npx" $skillsArgs) {
    Record-Installed $id
  } else {
    Record-Failed $id "npx skills add failed (profile: $(if ($profile) { $profile } else { 'auto' }))"
  }
  Write-Host ""
}

# ── Run the install loop ────────────────────────────────────────────────────
foreach ($p in $Providers) {
  if (-not (Want $p.id)) { continue }
  if (-not (Resolve-DetectSpec $p.detect)) { continue }
  switch ($p.id) {
    'claude' { Install-Claude }
    'gemini' { Install-Gemini }
    default  { Install-ViaSkills $p.id $p.label $p.profile }
  }
}

# ── Generic fallback: npx skills add (auto-detect) ─────────────────────────
if (-not $SkipSkills -and $OnlyList.Count -eq 0 -and $DetectedCount -eq 0) {
  Say "→ no known agents detected — running npx-skills auto-detect fallback"
  if (Ensure-Node) {
    if (Try-Run "npx" @("-y", "skills", "add", $Repo)) {
      Record-Installed "skills-auto"
    } else {
      Record-Failed "skills-auto" "npx skills add (auto) failed"
    }
  }
  Write-Host ""
}

# ── -WithInit: drop per-repo rule files into $PWD ──────────────────────────
# Avoid the variable name `$args` here — it shadows PowerShell's automatic
# unbound-args array. Use `$initArgs` instead.
function Run-Init {
  $initArgs = @($PWD.Path)
  if ($DryRun) { $initArgs += "--dry-run" }
  if ($Force)  { $initArgs += "--force" }

  if ($RepoRoot -and (Test-Path (Join-Path $RepoRoot "tools\caveman-init.js")) -and (Has-Cmd "node")) {
    if (Try-Run "node" (@((Join-Path $RepoRoot "tools\caveman-init.js")) + $initArgs)) { return $true }
    return $false
  }

  if (-not (Has-Cmd "node")) {
    Warn "  node required to run caveman-init (install Node.js: https://nodejs.org)"
    return $false
  }

  if ($DryRun) {
    Note "  would run: irm $InitScriptUrl | node - $($initArgs -join ' ')"
    return $true
  }

  $tmp = Join-Path $env:TEMP "caveman-init-$([Guid]::NewGuid()).js"
  try {
    Invoke-WebRequest -Uri $InitScriptUrl -OutFile $tmp -UseBasicParsing
    & node $tmp @initArgs
    return ($LASTEXITCODE -eq 0)
  } catch {
    Warn "  $($_.Exception.Message)"
    return $false
  } finally {
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
  }
}

if ($WithInit) {
  Say "→ writing per-repo IDE rule files into $PWD (-WithInit)"
  if (Run-Init) {
    Record-Installed "caveman-init ($PWD)"
  } else {
    Record-Failed "caveman-init" "tools/caveman-init.js failed"
  }
  Write-Host ""
} elseif ($InstalledIds.Count -gt 0 -or $SkippedIds.Count -gt 0) {
  Note "  tip: re-run inside a repo with -All (or -WithInit) to also write per-repo"
  Note "       Cursor/Windsurf/Cline/Copilot/AGENTS.md rule files."
}

# ── Summary ────────────────────────────────────────────────────────────────
Write-Host ""
Say "🪨 done"

if ($InstalledIds.Count -gt 0) {
  Ok "  installed:"
  foreach ($a in $InstalledIds) { Write-Host "    - $a" }
}

if ($SkippedIds.Count -gt 0) {
  Write-Host "  skipped:"
  for ($i = 0; $i -lt $SkippedIds.Count; $i++) {
    Write-Host ("    - {0} - {1}" -f $SkippedIds[$i], $SkippedWhy[$i])
  }
}

if ($FailedIds.Count -gt 0) {
  Warn "  failed:"
  for ($i = 0; $i -lt $FailedIds.Count; $i++) {
    Warn ("    - {0} - {1}" -f $FailedIds[$i], $FailedWhy[$i])
  }
}

if ($InstalledIds.Count -eq 0 -and $SkippedIds.Count -eq 0 -and $FailedIds.Count -eq 0) {
  Write-Host "  nothing detected. install one of: claude, gemini, codex, cursor, windsurf, cline, copilot, opencode, roo, amp, goose, kiro, augment, aider-desk, continue, junie, trae, warp, ..."
  Write-Host "  or pass -Only <agent> to force a specific target (see -List for the full matrix)"
}

Write-Host ""
Note "  start any session and say 'caveman mode', or run /caveman in Claude Code"
Note "  uninstall: see https://github.com/$Repo#install"

# Exit non-zero only when EVERY detected agent failed (and at least one was
# detected). Skips don't count as failure.
if ($DetectedCount -gt 0 -and $InstalledIds.Count -eq 0 -and $SkippedIds.Count -eq 0) {
  exit 1
}
exit 0
