$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $HOME ".claude" }
$Flag = Join-Path $ClaudeDir ".caveman-active"
if (-not (Test-Path $Flag)) { exit 0 }

# Refuse reparse points (symlinks / junctions) and oversized files. Without
# this, a local attacker could point the flag at a secret file and have the
# statusline render its bytes (including ANSI escape sequences) to the terminal
# every keystroke.
try {
    $Item = Get-Item -LiteralPath $Flag -Force -ErrorAction Stop
    if ($Item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) { exit 0 }
    if ($Item.Length -gt 64) { exit 0 }
} catch {
    exit 0
}

$Mode = ""
try {
    $Raw = Get-Content -LiteralPath $Flag -TotalCount 1 -ErrorAction Stop
    if ($null -ne $Raw) { $Mode = ([string]$Raw).Trim() }
} catch {
    exit 0
}

# Strip anything outside [a-z0-9-] — blocks terminal-escape and OSC hyperlink
# injection via the flag contents. Then whitelist-validate.
$Mode = $Mode.ToLowerInvariant()
$Mode = ($Mode -replace '[^a-z0-9-]', '')

$Valid = @('off','lite','full','ultra','wenyan-lite','wenyan','wenyan-full','wenyan-ultra','commit','review','compress')
if (-not ($Valid -contains $Mode)) { exit 0 }

$Esc = [char]27

# Opt-in compact badge: CAVEMAN_BADGE_COMPACT=1 → [C] / [C:L] / [C:U] / etc.
# Useful for ccstatusline-style dense statuslines where [CAVEMAN] is too long.
# Default (env unset or any other value) → existing verbose output unchanged.
# Compact mode also skips the savings suffix to keep the badge tight.
if ($env:CAVEMAN_BADGE_COMPACT -eq "1") {
    # Whitelist above rejects empty $Mode, so no empty arm needed here.
    switch ($Mode) {
        "full"         { [Console]::Write("${Esc}[38;5;172m[C]${Esc}[0m") }
        "lite"         { [Console]::Write("${Esc}[38;5;172m[C:L]${Esc}[0m") }
        "ultra"        { [Console]::Write("${Esc}[38;5;172m[C:U]${Esc}[0m") }
        "wenyan-lite"  { [Console]::Write("${Esc}[38;5;172m[C:WL]${Esc}[0m") }
        "wenyan"       { [Console]::Write("${Esc}[38;5;172m[C:W]${Esc}[0m") }
        "wenyan-full"  { [Console]::Write("${Esc}[38;5;172m[C:W]${Esc}[0m") }
        "wenyan-ultra" { [Console]::Write("${Esc}[38;5;172m[C:WU]${Esc}[0m") }
        "commit"       { [Console]::Write("${Esc}[38;5;172m[C:CM]${Esc}[0m") }
        "review"       { [Console]::Write("${Esc}[38;5;172m[C:RV]${Esc}[0m") }
        "compress"     { [Console]::Write("${Esc}[38;5;172m[C:CP]${Esc}[0m") }
    }
    exit 0
}

if ([string]::IsNullOrEmpty($Mode) -or $Mode -eq "full") {
    [Console]::Write("${Esc}[38;5;172m[CAVEMAN]${Esc}[0m")
} else {
    $Suffix = $Mode.ToUpperInvariant()
    [Console]::Write("${Esc}[38;5;172m[CAVEMAN:$Suffix]${Esc}[0m")
}

# Savings suffix: on by default. Opt out via CAVEMAN_STATUSLINE_SAVINGS=0.
# Reads a pre-rendered string written by caveman-stats.js. Refuses reparse
# points and strips control bytes (matches statusline.sh hardening). Until
# /caveman-stats has run at least once, the suffix file is absent and nothing
# is rendered — safe default for fresh installs.
if ($env:CAVEMAN_STATUSLINE_SAVINGS -ne "0") {
    $SavingsFile = Join-Path $ClaudeDir ".caveman-statusline-suffix"
    if (Test-Path $SavingsFile) {
        try {
            $SavingsItem = Get-Item -LiteralPath $SavingsFile -Force -ErrorAction Stop
            if (-not ($SavingsItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -and
                $SavingsItem.Length -le 64) {
                $Savings = (Get-Content -LiteralPath $SavingsFile -Raw -ErrorAction Stop).TrimEnd()
                $Savings = ($Savings -replace '[\x00-\x1F]', '')
                if ($Savings.Length -gt 0) {
                    [Console]::Write(" ${Esc}[38;5;172m$Savings${Esc}[0m")
                }
            }
        } catch {}
    }
}
