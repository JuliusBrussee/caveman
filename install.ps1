# caveman — installer shim (Windows / PowerShell).
#
# Thin wrapper around installer/install.js (the unified Node installer). Every flag
# you'd pass to installer/install.js can be passed here; we just forward them.
#
# One-line install:
#   irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.ps1 | iex
#
# Local clone:
#   pwsh install.ps1 [flags]
#
# After the installer it hands over to the CLI's first run (`caveman setup`:
# modules, agents, one Continue) in an interactive console, and prints that
# one command otherwise.
#
# Why a Node installer? install.sh + install.ps1 used to be parallel sources of
# truth and constantly drifted (issue #249 was a `node -e "..."` quoting bug
# that silently dropped the JSON merge step on every Windows install). One
# Node script works everywhere without quoting bugs.
#
# Why no top-level param() and everything inside a function? `irm | iex`
# executes this file as a string: script-path variables ($PSCommandPath,
# $MyInvocation.MyCommand.Path) are $null and a top-level param block cannot
# receive arguments through a pipe anyway (issue #565). Wrapping the logic in
# a function and forwarding $args keeps one script working for both the pipe
# path (no args, no script path) and the local-clone path.

function Install-Caveman {
  param(
    [string[]]$InstallerArgs = @()
  )

  $ErrorActionPreference = "Stop"
  $Repo = "JuliusBrussee/caveman"
  $PinnedRef = if ($env:CAVEMAN_REF) { $env:CAVEMAN_REF } else { "v3.2.0" }
  # The CLI release the first run comes from when caveman is not installed;
  # kept equal to packages/cli/package.json (tests/installer/shim-security).
  $CliVersion = "2.1.0"

  # Require Node ≥18.
  $node = Get-Command node -ErrorAction SilentlyContinue
  if (-not $node) {
    Write-Error @"
caveman: Node.js (>=18) required. Install:
  - winget install OpenJS.NodeJS.LTS
  - or download from https://nodejs.org
"@
    $global:LASTEXITCODE = 1; return
  }

  $nodeMajor = [int](& node -p "process.versions.node.split('.')[0]")
  if ($nodeMajor -lt 18) {
    Write-Error "caveman: Node $nodeMajor too old. Need Node >=18. Upgrade: https://nodejs.org"
    $global:LASTEXITCODE = 1; return
  }

  # If we're inside the repo clone, run the local installer directly.
  # $PSCommandPath is $null when piped to iex (#565) — the old unguarded
  # Split-Path on it was the "Cannot bind argument to parameter 'Path'
  # because it is null" crash.
  $local = $null
  if ($PSCommandPath) {
    $here = Split-Path -Parent $PSCommandPath
    $local = Join-Path $here "installer/install.js"
    if (-not (Test-Path $local)) { $local = $null }
  }

  if ($local) {
    & node $local @InstallerArgs
  } else {
    # Curl-pipe path: delegate to npx.
    $npx = Get-Command npx -ErrorAction SilentlyContinue
    if (-not $npx) {
      Write-Error "caveman: npx required (ships with Node >=18). Reinstall Node.js."
      $global:LASTEXITCODE = 1; return
    }

    # Do NOT pass `--` here — npm 7+ npx already forwards trailing args to the
    # package, and a literal `--` was tripping installer/install.js's parseArgs as an
    # unknown flag.
    # npm 12 disables git package fetches by default (EALLOWGIT). Allow only the
    # root package requested here; older npm versions do not understand this
    # config flag.
    # 2>$null mirrors install.sh's `2>/dev/null`: npm writes its "does not support
    # Node.js" notice to stderr, and a contaminated value would floor the major to 0.
    $npxVersion = [string](& npx --version 2>$null)
    $npxMajor = 0
    if ($npxVersion -match '^(\d+)') {
      $npxMajor = [int]$Matches[1]
    }

    if ($npxMajor -ge 12) {
      & npx --allow-git=root -y "github:$Repo#$PinnedRef" @InstallerArgs
    } else {
      & npx -y "github:$Repo#$PinnedRef" @InstallerArgs
    }
  }
  if ($LASTEXITCODE -ne 0) { return }

  # End in the CLI's first run: modules, agents, one Continue.
  $skip = @($InstallerArgs | Where-Object { $_ -in @("-h", "--help", "--list", "-u", "--uninstall", "--dry-run") })
  if ($skip.Count -gt 0) { return }
  # The skills above run on Node 18; the CLI (runtime, routing) needs 22.13.
  # A prerelease Node ("25.0.0-nightly…") is not a [version] until its suffix goes.
  $nodeVersion = [version](([string](& node -p "process.versions.node")) -replace '[-+].*$', '')
  if ($nodeVersion -lt [version]"22.13.0") {
    Write-Host ""
    Write-Host "caveman: skills installed. The runtime (smaller inputs, Auto routing) needs Node 22.13+; this is v$nodeVersion."
    Write-Host "  Upgrade Node (winget install OpenJS.NodeJS.LTS), then run: npx -y @caveman-ai/cli@$CliVersion"
    return
  }
  # The caveman on PATH when it is this release or newer: an older CLI has an
  # older setup, so npx runs this one and its setup installs it for good, but
  # over a newer CLI that would be a downgrade. A prerelease ranks below its
  # own release; a version that cannot be read counts as older. The probe
  # gives up after 10s. Same probe as install.sh, with no double quotes in it:
  # Windows PowerShell drops those from a native command's arguments (#249).
  $setup = @("npx", "-y", "@caveman-ai/cli@$CliVersion", "setup")
  if (Get-Command caveman -ErrorAction SilentlyContinue) {
    try {
      & node -e 'const r=require(`child_process`).spawnSync(`caveman --version`,{shell:true,encoding:`utf8`,timeout:1e4,killSignal:`SIGKILL`,stdio:[`ignore`,`pipe`,`ignore`],windowsHide:true});const v=s=>(s=/^(\d+)\.(\d+)\.(\d+)(-?)/.exec(s))&&[+s[1],+s[2],+s[3],+!s[4]];let h;try{h=v(JSON.parse(r.stdout).version)}catch{}const w=v(process.argv[1]),d=h&&w&&h.map((x,i)=>x-w[i]).find(x=>x);process.exitCode=h&&w&&!(d<0)?0:1' $CliVersion 2>$null
      if ($LASTEXITCODE -eq 0) { $setup = @("caveman", "setup") }
    } catch { }
  }
  # --non-interactive never prompts: name the first run instead of starting it.
  if ($InstallerArgs -notcontains "--non-interactive" -and [Environment]::UserInteractive -and -not [Console]::IsInputRedirected -and -not [Console]::IsOutputRedirected) {
    & $setup[0] $setup[1..($setup.Length - 1)]
    return
  }
  Write-Host "Next: $($setup -join ' ')"
  $global:LASTEXITCODE = 0
}

# $args is the automatic variable: populated when run as a file
# (`pwsh install.ps1 --force`), empty under `irm | iex`.
Install-Caveman -InstallerArgs $args
# Under `irm | iex` this runs in the user's own session, where `exit` would
# close their window over the output above; only a script file exits.
if ($PSCommandPath) { exit $LASTEXITCODE }
