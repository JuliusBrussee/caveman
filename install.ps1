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
  [string[]]$Args
)

$ErrorActionPreference = "Stop"
$Repo = "JuliusBrussee/caveman"

# Require Node ≥18.
$node = Get-Command node -ErrorAction SilentlyContinue
if (-not $node) {
  Write-Error @"
caveman: Node.js (>=18) required. Install:
  - winget install OpenJS.NodeJS.LTS
  - or download from https://nodejs.org
"@
  exit 1
}

$nodeMajor = [int](& node -p "process.versions.node.split('.')[0]")
if ($nodeMajor -lt 18) {
  Write-Error "caveman: Node $nodeMajor too old. Need Node >=18. Upgrade: https://nodejs.org"
  exit 1
}

# If we're inside the repo clone, run the local installer directly.
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$local = Join-Path $here "bin/install.js"
if (Test-Path $local) {
  & node $local @Args
  exit $LASTEXITCODE
}

# Curl-pipe path: delegate to npx.
$npx = Get-Command npx -ErrorAction SilentlyContinue
if (-not $npx) {
  Write-Error "caveman: npx required (ships with Node >=18). Reinstall Node.js."
  exit 1
}

# Do NOT pass `--` here — npm 7+ npx already forwards trailing args to the
# package, and a literal `--` was tripping bin/install.js's parseArgs as an
# unknown flag.
& npx -y "github:$Repo" @Args
exit $LASTEXITCODE
