# Caveman Pipe: Summarize anything in technical caveman style
# Usage: git diff | .\commands\caveman-pipe.ps1

$inputData = $input | Out-String

if ([string]::IsNullOrWhiteSpace($inputData)) {
    Write-Host "Usage: <command> | .\commands\caveman-pipe.ps1"
    exit 1
}

$inputData | claude --print -p "Responde en estilo cavernícola técnico. Sustancia queda. Paja muere."
