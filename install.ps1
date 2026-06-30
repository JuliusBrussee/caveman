# caveman — installer shim (Windows / PowerShell).
#
# Thin wrapper around bin/install.js (the unified Node installer). Every flag
# you'd pass to bin/install.js can be passed here; we just forward them.
#
# One-line install:
#   irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
#
# Local clone:
#   pwsh install.ps1 [flags]
#
# Why a Node installer? install.sh + install.ps1 used to be parallel sources of
# truth and constantly drifted (issue #249 was a `node -e "..."` quoting bug
# that silently dropped the JSON merge step on every Windows install). One
# Node script works everywhere without quoting bugs.

[CmdletBinding()]
param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$InstallerArgs
)

$ErrorActionPreference = "Stop"
$Repo = "JuliusBrussee/caveman"

# Resolve runtime: prefer Node ≥18, fall back to Bun.
$runtime = $null

$node = Get-Command node -ErrorAction SilentlyContinue
if ($node) {
  $nodeMajor = [int](& node -p "process.versions.node.split('.')[0]")
  if ($nodeMajor -ge 18) {
    $runtime = 'node'
  }
  # If Node is too old, don't exit — fall through to the Bun probe below.
}

if (-not $runtime) {
  $bun = Get-Command bun -ErrorAction SilentlyContinue
  if ($bun) {
    $runtime = 'bun'
  }
}

if (-not $runtime) {
  if ($node) {
    Write-Error "caveman: Node $nodeMajor too old (>=18 needed), and Bun not found. Install:"
  } else {
    Write-Error "caveman: Node.js (>=18) or Bun required. Install:"
  }
  Write-Error @"
  - Node.js: winget install OpenJS.NodeJS.LTS
    or download from https://nodejs.org
  - Bun:     powershell -c "irm bun.sh/install.ps1 | iex"
    or download from https://bun.sh
"@
  exit 1
}

# If we're inside the repo clone, run the local installer directly.
# When piped to iex (irm ... | iex), $MyInvocation.MyCommand.Path is null — skip local check.
if ($MyInvocation.MyCommand.Path) {
  $here = Split-Path -Parent $MyInvocation.MyCommand.Path
  $local = Join-Path $here "bin/install.js"
  if (Test-Path $local) {
    & $runtime $local @InstallerArgs
    exit $LASTEXITCODE
  }
}

# Curl-pipe path: delegate to npx (Node) or bunx (Bun).
if ($runtime -eq 'node') {
  $npx = Get-Command npx -ErrorAction SilentlyContinue
  if (-not $npx) {
    Write-Error "caveman: npx required (ships with Node >=18). Reinstall Node.js."
    exit 1
  }

  # Do NOT pass `--` here — npm 7+ npx already forwards trailing args to the
  # package, and a literal `--` was tripping bin/install.js's parseArgs as an
  # unknown flag.
  & npx -y "github:$Repo" @InstallerArgs
} else {
  # Bun runtime: bunx is Bun's equivalent of npx.
  & bunx -y "github:$Repo" @InstallerArgs
}
exit $LASTEXITCODE
