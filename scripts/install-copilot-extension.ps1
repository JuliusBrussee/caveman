[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$RemainingArgs
)

$scriptPath = Join-Path $PSScriptRoot 'install-copilot-extension.mjs'
& node $scriptPath @RemainingArgs
exit $LASTEXITCODE
