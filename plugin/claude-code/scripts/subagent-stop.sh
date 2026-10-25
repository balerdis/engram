#!/bin/bash
# Engram — SubagentStop hook for Claude Code (async)
#
# Thin hook: reads the subagent's last assistant message from the hook input, POSTs it to
# the passive capture endpoint. All extraction logic lives in the
# Go server — this script is intentionally minimal.


# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
OUTPUT=$(echo "$INPUT" | jq -r '.last_assistant_message // empty')

# Nothing to capture if no output
[ -z "$OUTPUT" ] && exit 0

# Memory disabled (port held by another user): send nothing.
resolve_engram_url || exit 0

# Ambiguous project: do not invent a name, skip the capture.
resolve_project "$CWD"
[ "$PROJECT_AMBIGUOUS" = "1" ] && exit 0

# Fire and forget — server handles extraction, dedup, and storage
curl -sf "${ENGRAM_URL}/observations/passive" \
  -X POST \
  -H "Content-Type: application/json" \
  --data-binary @- \
  > /dev/null 2>&1 < <(printf '%s' "$INPUT" | jq -c \
    --arg project "$PROJECT" \
    --arg source "subagent-stop" \
    '{session_id: .session_id, content: .last_assistant_message, project: $project, source: $source}')

exit 0
