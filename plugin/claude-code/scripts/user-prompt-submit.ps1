#!/usr/bin/env pwsh
# Engram — Windows-native UserPromptSubmit hook for Claude Code
#
# Optional fallback for Windows endpoints where Git Bash/MSYS2 is slowed or
# blocked. Captures the prompt to POST /prompts, prints nothing and never
# blocks prompt submission.

$ErrorActionPreference = 'SilentlyContinue'
[Console]::InputEncoding = [System.Text.Encoding]::UTF8

try {
  # Resolve the port with `engram port` (update check off, stdout and stderr
  # apart). Disabled only when exit 1 + empty stdout + a stderr that says memory
  # is disabled; exit 0 with a non-numeric stdout is disabled too (fail closed).
  # Anything else (an engram without that command, a crash) falls back to
  # ENGRAM_PORT / 7437.
  $engramPort = if ($env:ENGRAM_PORT -match '^[0-9]{1,5}$') { $env:ENGRAM_PORT } else { '7437' }
  $env:ENGRAM_NO_UPDATE_CHECK = '1'
  $errFile = [System.IO.Path]::GetTempFileName()
  $portOut = ((& engram port 2>$errFile) | Out-String).Trim()
  $portRc = $LASTEXITCODE
  $portErr = (Get-Content -Raw -ErrorAction SilentlyContinue $errFile)
  Remove-Item -Force -ErrorAction SilentlyContinue $errFile
  if ($portRc -eq 0) {
    if ($portOut -match '^[0-9]{1,5}$' -and [int]$portOut -ge 1 -and [int]$portOut -le 65535) {
      $engramPort = $portOut
    } else {
      exit 0
    }
  } elseif ($portRc -eq 1 -and $portOut -eq '' -and $portErr -match 'memory is disabled|memory disabled|held by a process of another OS user') {
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
