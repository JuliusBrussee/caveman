[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$ClaudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $HOME ".claude" }

# Claude Code invokes statusLine commands with the same JSON-on-stdin
# contract as other hooks (e.g. session_id, cwd). ConvertFrom-Json is a
# first-party PowerShell idiom, not an external dependency the way jq would
# be for Bash. ReadToEnd on a closed/empty stdin returns "" immediately, not
# a hang (matches a manual/test invocation with no input).
$StdinJson = ""
try { $StdinJson = [Console]::In.ReadToEnd() } catch {}

$SessionId = ""
if ($StdinJson) {
    try {
        $Data = $StdinJson | ConvertFrom-Json -ErrorAction Stop
        # PR-review v5 High finding: plain dot-notation property access
        # ($Data.session_id) is CASE-INSENSITIVE in PowerShell -- a payload
        # containing "Session_Id" (or any other casing) would resolve here,
        # while JS's JSON.parse(...).session_id and Bash's structural walker
        # are both case-sensitive and would treat it as absent. Filter
        # PSObject.Properties with the case-SENSITIVE -ceq operator so only
        # an exact-case "session_id" key is ever considered, matching real
        # JSON member semantics (property names are case-sensitive per spec).
        $SessionIdProp = $Data.PSObject.Properties | Where-Object { $_.Name -ceq 'session_id' } | Select-Object -First 1
        # PR-review High finding: a truthy NON-STRING session_id (e.g. a JSON
        # number) would otherwise be cast to a string and matched, computing
        # a scoped path the JS/Bash implementations never would (both reject
        # non-string session_id outright) -- a cross-implementation parity
        # gap that could display another scoped session's mode. Require the
        # actual JSON type to be a string before casting or matching.
        if ($SessionIdProp -and $SessionIdProp.Value -is [string]) {
            $Raw = $SessionIdProp.Value
            # Whole-string anchored match using \z, NOT a trailing $ (Tier-2 v2
            # High finding): .NET's $ matches end-of-string OR immediately
            # before a single trailing `\n` by default, so a value ending in
            # one newline would falsely pass a `$`-anchored check. \z has no
            # such exception. Reject entirely on any non-match — never strip
            # or truncate down to a valid-looking id.
            if ($Raw -match '^[A-Za-z0-9_-]{1,128}\z') {
                $SessionId = $Raw
            }
        }
    } catch {}
}

# Resolve the flag path with the same ENOENT-vs-rejected fallback semantics
# as caveman-config.js's resolveFlag: no session id (or an invalid one) ->
# always the legacy path, unchanged from today. A valid session id with NO
# scoped file at all (true ENOENT) falls back to the legacy path. A valid
# session id whose scoped file EXISTS (even a reparse point, even
# invalid/oversized content) is fail-closed: never fall back to legacy.
$LegacyFlag = Join-Path $ClaudeDir ".caveman-active"
$Flag = $LegacyFlag
$ScopedIdentity = $false
if ($SessionId) {
    $ScopedFlag = Join-Path $ClaudeDir ".caveman-active-$SessionId"
    # PR-review High finding: Test-Path follows a reparse point and can
    # report $false for a DANGLING scoped symlink on some PowerShell
    # versions (target-following resolution), which would wrongly fall
    # through to the legacy path -- exactly the fail-open bypass this
    # design rejects elsewhere. Get-Item -Force detects the directory
    # ENTRY itself (a reparse point exists regardless of target validity),
    # matching resolveFlag's/statusline.sh's lstat-based existence check.
    try {
        Get-Item -LiteralPath $ScopedFlag -Force -ErrorAction Stop | Out-Null
        $Flag = $ScopedFlag
        $ScopedIdentity = $true
    } catch [System.Management.Automation.ItemNotFoundException] {
        # True not-found (ENOENT-equivalent) -> fall back to the legacy path,
        # $Flag is already $LegacyFlag.
    } catch {
        # PR-review High finding: catching every exception here treated an
        # EACCES/I/O failure the same as "doesn't exist" and fell through to
        # the legacy path, potentially rendering another session's mode.
        # Empirically confirmed distinct: Get-Item throws
        # ItemNotFoundException for a missing path but
        # UnauthorizedAccessException (or other exceptions) for a lookup that
        # failed for any other reason. Fail closed for everything but a
        # confirmed not-found.
        exit 0
    }
}

# Refuse reparse points (symlinks / junctions) and oversized files. Without
# this, a local attacker could point the flag at a secret file and have the
# statusline render its bytes (including ANSI escape sequences) to the terminal
# every keystroke.
try {
    $Item = Get-Item -LiteralPath $Flag -Force -ErrorAction Stop
    if ($Item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) { exit 0 }
    if ($Item.Length -gt 64) { exit 0 }
} catch {
    # Scoped identity + missing/rejected content -> fail closed, render
    # nothing, never fall back to the legacy sentinel ($Flag is already the
    # scoped path here when $ScopedIdentity is true).
    exit 0
}

$Mode = ""
try {
    # Reject-not-strip, exact-match validation (PR-review High finding):
    # -TotalCount 1 only reads the FIRST line, so a file like
    # "full`nnot-a-mode" (within the 64-byte cap) would validate on "full"
    # alone while readFlag reads the COMPLETE content and rejects the whole
    # value. Read the full bounded content (-Raw) and trim only
    # leading/trailing whitespace -- mirrors caveman-config.js's readFlag
    # exactly, same as the Bash statusline's fix for the equivalent bug.
    $Raw = Get-Content -LiteralPath $Flag -Raw -ErrorAction Stop
    if ($null -ne $Raw) { $Mode = ([string]$Raw).Trim() }
} catch {
    exit 0
}

$Mode = $Mode.ToLowerInvariant()

$Valid = @('off','lite','full','ultra','wenyan-lite','wenyan','wenyan-full','wenyan-ultra','commit','review','compress')
if (-not ($Valid -contains $Mode)) { exit 0 }

# A resolved mode of 'off' renders nothing at all, matching isActiveMode.
if ($Mode -eq "off") { exit 0 }

$Esc = [char]27
if ($Mode -eq "full") {
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
                $Savings = (Get-Content -LiteralPath $SavingsFile -Encoding UTF8 -Raw -ErrorAction Stop).TrimEnd()
                $Savings = ($Savings -replace '[\x00-\x1F]', '')
                if ($Savings.Length -gt 0) {
                    [Console]::Write(" ${Esc}[38;5;172m$Savings${Esc}[0m")
                }
            }
        } catch {}
    }
}
