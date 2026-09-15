[CmdletBinding()]
param(
  [switch]$NoLink
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot

if (Test-Path (Join-Path $RepoRoot "public/cli")) {
  $CliDir = Join-Path $RepoRoot "public/cli"
  $StackRoot = Join-Path $RepoRoot "public"
} elseif (Test-Path (Join-Path $RepoRoot "packages/cli")) {
  $CliDir = Join-Path $RepoRoot "packages/cli"
  $StackRoot = $RepoRoot
} elseif (Test-Path (Join-Path $RepoRoot "cli")) {
  $CliDir = Join-Path $RepoRoot "cli"
  $StackRoot = $RepoRoot
} else {
  throw "Caveman CLI source not found"
}

$Dist = Join-Path $CliDir "dist/index.js"
if (-not (Test-Path $Dist)) {
  if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
    throw "$Dist missing and pnpm not found. Install pnpm and re-run."
  }
  Write-Host "building CLI ($CliDir)"
  Push-Location $CliDir
  try {
    & pnpm install --silent
    if ($LASTEXITCODE -ne 0) { throw "pnpm install failed with exit code $LASTEXITCODE" }
    & pnpm build
    if ($LASTEXITCODE -ne 0) { throw "pnpm build failed with exit code $LASTEXITCODE" }
  } finally {
    Pop-Location
  }
}

if (-not $NoLink) {
  if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
    throw "npm not found. Install Node.js >=18 and re-run."
  }
  Push-Location $CliDir
  try {
    & npm link
    if ($LASTEXITCODE -ne 0) { throw "npm link failed with exit code $LASTEXITCODE" }
  } finally {
    Pop-Location
  }
  Write-Host "linked cave + caveman through npm"
}

$Go = Get-Command go -ErrorAction SilentlyContinue
if (-not $Go) {
  Write-Warning "Go toolchain not found; skipped six runtime companions. CLI remains installed and reports missing capabilities."
  exit 0
}

$CavemanHome = if ($env:CAVEMAN_HOME) {
  $env:CAVEMAN_HOME
} elseif ($env:USERPROFILE) {
  Join-Path $env:USERPROFILE ".caveman"
} else {
  Join-Path ([Environment]::GetFolderPath("UserProfile")) ".caveman"
}
$BinDir = Join-Path $CavemanHome "bin"
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null

$Binaries = [ordered]@{
  "caveman-proxy" = "proxy/cmd/caveman-proxy"
  "caveman-engine" = "engine/cmd/caveman-engine"
  "caveman-mcp" = "mcp/cmd/caveman-mcp"
  "cavemem" = "mem/cmd/cavemem"
  "caveman-browse" = "browse/cmd/caveman-browse"
  "caveman-shrink" = "shrink/cmd/caveman-shrink"
}

Write-Host "building Go binaries into $BinDir"
foreach ($Entry in $Binaries.GetEnumerator()) {
  $Output = Join-Path $BinDir "$($Entry.Key).exe"
  $Package = Join-Path $StackRoot $Entry.Value
  & go build -o $Output $Package
  if ($LASTEXITCODE -ne 0) { throw "go build failed for $($Entry.Key) with exit code $LASTEXITCODE" }
}

Write-Host "done - try: caveman claude"
