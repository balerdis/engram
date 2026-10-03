#!/usr/bin/env pwsh
# Engram — Windows-native UserPromptSubmit hook for Claude Code
#
# Optional fallback for Windows endpoints where Git Bash/MSYS2 is slowed or
# blocked. Captures the prompt to POST /prompts, prints nothing and never
# blocks prompt submission.

$ErrorActionPreference = 'SilentlyContinue'
[Console]::InputEncoding = [System.Text.Encoding]::UTF8

try {
  $engramPort = if ($env:ENGRAM_PORT) { $env:ENGRAM_PORT } else { '7437' }
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
