#!/bin/bash
# Engram — UserPromptSubmit hook for Claude Code (async)
#
# Captures every user prompt to POST /prompts so mem_save can attach the
# originating prompt. Fire-and-forget: prints nothing (no systemMessage, no
# context) and always exits 0 so it can never delay or block the prompt.
# The server derives the prompt's project from the session.


# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)
PROMPT=$(echo "$INPUT" | jq -r '.prompt // empty' 2>/dev/null)

if [ -n "$PROMPT" ] && [ -n "$SESSION_ID" ] && resolve_engram_url; then
  # The body travels through a pipe: one argv string is capped at 128 KiB on
  # Linux, so a long prompt must never be a command-line argument.
  printf '%s' "$INPUT" | jq -c '{session_id:.session_id, content:.prompt}' 2>/dev/null \
    | curl -sf -X POST "${ENGRAM_URL}/prompts" --max-time 2 \
        -H 'Content-Type: application/json' --data-binary @- >/dev/null 2>&1 || true
fi

exit 0
