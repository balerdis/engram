#!/usr/bin/env pwsh
# Engram — Windows-native UserPromptSubmit hook for Claude Code
#
# Optional fallback for Windows endpoints where Git Bash/MSYS2 is slowed or
# blocked. Captures the prompt to POST /prompts, prints nothing and never
# blocks prompt submission.

$ErrorActionPreference = 'SilentlyContinue'
[Console]::InputEncoding = [System.Text.Encoding]::UTF8

try {
  # Resolve the port with `engram port` (exit 1 = memory disabled: send nothing).
  # An engram without that command falls back to ENGRAM_PORT / 7437.
  $engramPort = if ($env:ENGRAM_PORT) { $env:ENGRAM_PORT } else { '7437' }
  $portOut = (& engram port 2>&1 | Out-String).Trim()
  if ($LASTEXITCODE -eq 0 -and $portOut -match '^[0-9]{1,5}$') {
    $engramPort = $portOut
  } elseif ($LASTEXITCODE -eq 1 -and $portOut -notmatch '^unknown command') {
    exit 0
  }
  $payload   = [Console]::In.ReadToEnd() | ConvertFrom-Json
  $sessionID = [string]($payload.session_id)
  $prompt    = [string]($payload.prompt)

  if (-not [string]::IsNullOrWhiteSpace($prompt) -and -not [string]::IsNullOrWhiteSpace($sessionID)) {
    $body = [PSCustomObject]@{ session_id = $sessionID; content = $prompt } | ConvertTo-Json -Compress
    $null = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$engramPort/prompts" `
      -ContentType 'application/json; charset=utf-8' `
      -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 1
  }
} catch { }

exit 0
