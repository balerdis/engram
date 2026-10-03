#!/bin/bash
# Engram — SessionEnd hook for Claude Code (synchronous)
#
# Marks the session as ended via the HTTP API. Claude Code gives SessionEnd
# hooks a ~1.5s budget that plugin timeouts do not raise, so the request is
# capped at 1s and failures are ignored. The body is empty on purpose: the
# server keeps any summary already stored for the session.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')

if [ -z "$SESSION_ID" ]; then
  exit 0
fi

curl -sf --max-time 1 "${ENGRAM_URL}/sessions/${SESSION_ID}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  > /dev/null 2>&1

exit 0
