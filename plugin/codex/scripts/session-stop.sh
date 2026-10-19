#!/bin/bash
# Engram — Stop hook for Codex (synchronous)
#
# Marks the session as ended via the HTTP API.
# Runs synchronously (Codex does not support async: true).
# The HTTP call is fast enough to complete well within the 5s timeout.


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
curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  --max-time 4 \
  > /dev/null 2>&1

exit 0
