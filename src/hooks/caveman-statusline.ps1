[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $HOME ".claude" }

# Claude Code sends session JSON on stdin, including session_id. We use it to
# read THIS window's mode instead of a machine-wide flag — otherwise every
# window renders whichever mode was set last.
#
# Two guards keep this from wedging the status bar, mirroring the bash port:
#   1. IsInputRedirected — an interactive console is never read from.
#   2. ReadToEndAsync + Wait(1000) — a hard 1s ceiling if stdin is an open
#      pipe that never closes. Normally EOF arrives immediately.
$SessionId = ""
if ([Console]::IsInputRedirected) {
    try {
        $task = [Console]::In.ReadToEndAsync()
        if ($task.Wait(1000)) {
            $payload = $task.Result
            if ($payload) {
                $SessionId = [string](ConvertFrom-Json $payload).session_id
            }
        }
    } catch {
        $SessionId = ""
    }
}

# The id becomes part of a path, so whitelist its alphabet. Anything else —
# including empty — falls back to the legacy machine-wide flag.
if ($SessionId -notmatch '^[A-Za-z0-9_-]{1,128}$') { $SessionId = "" }

$Flag = Join-Path $ClaudeDir ".caveman-active"
if ($SessionId) {
    $SessionFlag = Join-Path (Join-Path $ClaudeDir ".caveman-sessions") "$SessionId.mode"
    if (Test-Path -LiteralPath $SessionFlag) { $Flag = $SessionFlag }
}

if (-not (Test-Path -LiteralPath $Flag)) { exit 0 }

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

# Durable off: caveman is deactivated for this session. Render nothing at all,
# matching what an absent flag does — never "[CAVEMAN:OFF]", which would read
# as a caveman mode rather than the absence of one.
if ($Mode -eq "off") { exit 0 }

$Esc = [char]27
if ([string]::IsNullOrEmpty($Mode) -or $Mode -eq "full") {
    [Console]::Write("${Esc}[38;5;172m[CAVEMAN]${Esc}[0m")
} else {
    $Suffix = $Mode.ToUpperInvariant()
    [Console]::Write("${Esc}[38;5;172m[CAVEMAN:$Suffix]${Esc}[0m")
}

# Historical numeric savings suffixes are not measurements. Ignore them,
# including files written by older stats scripts before this update.
exit 0
