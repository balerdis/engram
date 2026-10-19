#!/bin/bash
# Engram — SessionEnd hook for Claude Code (synchronous)
#
# Marks the session as ended via the HTTP API. Claude Code gives SessionEnd
# hooks a ~1.5s budget that plugin timeouts do not raise, so the request is
# capped at 1s and failures are ignored. The body is empty on purpose: the
# server keeps any summary already stored for the session.


# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')

if [ -z "$SESSION_ID" ]; then
  exit 0
fi

# Memory disabled (port held by another user): send nothing.
resolve_engram_url || exit 0

ENCODED_SESSION=$(printf '%s' "$SESSION_ID" | jq -sRr @uri)
curl -sf --max-time 1 "${ENGRAM_URL}/sessions/${ENCODED_SESSION}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  > /dev/null 2>&1

exit 0
