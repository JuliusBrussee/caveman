$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $HOME ".claude" }
$GlobalFlag = Join-Path $ClaudeDir ".caveman-active"

# Claude Code pipes session JSON to statusline scripts on stdin. Pull the
# session_id and prefer the per-session flag so concurrent sessions in
# different caveman levels each see their own badge. Sanitize aggressively
# — anything we splice into a path needs to be safe.
$SessionId = ""
try {
    if (-not [Console]::IsInputRedirected) {
        # No stdin — manual invocation. Skip session lookup.
    } else {
        $StdinRaw = [Console]::In.ReadToEnd()
        if ($StdinRaw) {
            $Parsed = $StdinRaw | ConvertFrom-Json -ErrorAction Stop
            if ($Parsed.session_id) {
                $Cleaned = ($Parsed.session_id -replace '[^a-zA-Z0-9-]', '')
                if ($Cleaned.Length -gt 0 -and $Cleaned.Length -le 128) {
                    $SessionId = $Cleaned
                }
            }
        }
    }
} catch { }

$Flag = $null
if ($SessionId) {
    $Candidate = Join-Path $ClaudeDir ".caveman-active-$SessionId"
    if (Test-Path $Candidate) { $Flag = $Candidate }
}
if (-not $Flag -and (Test-Path $GlobalFlag)) { $Flag = $GlobalFlag }
if (-not $Flag) { exit 0 }

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
if ([string]::IsNullOrEmpty($Mode) -or $Mode -eq "full") {
    [Console]::Write("${Esc}[38;5;172m[CAVEMAN]${Esc}[0m")
} else {
    $Suffix = $Mode.ToUpperInvariant()
    [Console]::Write("${Esc}[38;5;172m[CAVEMAN:$Suffix]${Esc}[0m")
}
