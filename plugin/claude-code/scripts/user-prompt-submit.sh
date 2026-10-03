#!/bin/bash
# Engram — UserPromptSubmit hook for Claude Code (async)
#
# Captures every user prompt to POST /prompts so mem_save can attach the
# originating prompt. Fire-and-forget: prints nothing (no systemMessage, no
# context) and always exits 0 so it can never delay or block the prompt.
# The server derives the prompt's project from the session.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)
PROMPT=$(echo "$INPUT" | jq -r '.prompt // empty' 2>/dev/null)

if [ -n "$PROMPT" ] && [ -n "$SESSION_ID" ]; then
  curl -sf -X POST "${ENGRAM_URL}/prompts" --max-time 2 \
    -H 'Content-Type: application/json' \
    -d "$(jq -n --arg s "$SESSION_ID" --arg c "$PROMPT" \
          '{session_id:$s, content:$c}')" >/dev/null 2>&1 || true
fi

exit 0
