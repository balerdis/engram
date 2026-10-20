#!/bin/bash
# Engram — UserPromptSubmit hook for Codex
#
# On the FIRST message of a session: injects a ToolSearch instruction to force
# Codex to load all engram memory tools (which are deferred by default).
#
# On subsequent messages: checks when the last mem_save was for the current
# project. If it's been > 15 minutes AND the session has been active > 5
# minutes, injects a nudge reminding the agent to save.
#
# The nudge is debounced per session: once shown, it stays quiet for
# ENGRAM_NUDGE_COOLDOWN_SECS (default 900s) before it can fire again.
#
# MUST exit 0 always and output valid JSON — otherwise Codex blocks the message.


print_toolsearch_message() {
  printf '%s\n' '{"systemMessage":"CRITICAL FIRST ACTION — Execute this ToolSearch NOW before responding to the user:\nselect:mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge\n\nAfter loading tools, call mem_context to check for prior session history before responding."}'
}

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')

# Memory disabled (port held by another user): no request, no injection.
if ! resolve_engram_url; then
  echo "{}"
  exit 0
fi

# ──────────────────────────────────────────────────────────────────────────────
# PROMPT PERSIST
#
# Every user message is captured to POST /prompts so mem_save can attach the
# originating prompt via SessionActivity. Detached subshell: never blocks and
# never fails the hook.
# ──────────────────────────────────────────────────────────────────────────────
PROMPT=$(echo "$INPUT" | jq -r '.prompt // empty')
if [ -n "$PROMPT" ] && [ -n "$SESSION_ID" ]; then
  (
    # Only a name the server gave is sent; otherwise the server derives it.
    resolve_project "$CWD"
    _PROMPT_PROJECT=""
    [ "$PROJECT_FROM_SERVER" = "1" ] && _PROMPT_PROJECT="$PROJECT"
    curl -sf -X POST "${ENGRAM_URL}/prompts" --max-time 2 \
      -H 'Content-Type: application/json' \
      -d "$(jq -n --arg s "$SESSION_ID" --arg p "${_PROMPT_PROJECT:-}" --arg c "$PROMPT" \
            '{session_id:$s, project:$p, content:$c}')" >/dev/null 2>&1 || true
  ) &
fi

parse_epoch() {
  TS="$1"
  if [ -z "$TS" ]; then
    return 1
  fi

  # Drop fractional seconds without dropping timezone information.
  if [[ "$TS" == *.* ]]; then
    TS_PREFIX="${TS%%.*}"
    TS_SUFFIX="${TS#*.}"
    case "$TS_SUFFIX" in
      *Z) TS="${TS_PREFIX}Z" ;;
      *+*) TS="${TS_PREFIX}+${TS_SUFFIX#*+}" ;;
      *-*) TS="${TS_PREFIX}-${TS_SUFFIX#*-}" ;;
      *) TS="$TS_PREFIX" ;;
    esac
  fi

  # BSD date accepts numeric RFC3339 offsets with %z, but requires +HHMM.
  if [[ "$TS" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})([+-][0-9]{2}):([0-9]{2})$ ]]; then
    TZ_TS="${BASH_REMATCH[1]}${BASH_REMATCH[2]}${BASH_REMATCH[3]}"
    date -j -f "%Y-%m-%dT%H:%M:%S%z" "$TZ_TS" "+%s" 2>/dev/null && return 0
  fi
  if [[ "$TS" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[+-][0-9]{4}$ ]]; then
    date -j -f "%Y-%m-%dT%H:%M:%S%z" "$TS" "+%s" 2>/dev/null && return 0
  fi

  if [[ "$TS" == *Z ]]; then
    Z_TS="${TS%Z}"
    date -j -u -f "%Y-%m-%dT%H:%M:%S" "$Z_TS" "+%s" 2>/dev/null && return 0
  fi

  date -j -f "%Y-%m-%dT%H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -j -f "%Y-%m-%d %H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -d "$TS" "+%s" 2>/dev/null
}

# Default: no injection
OUTPUT="{}"

# ──────────────────────────────────────────────────────────────────────────────
# FIRST-MESSAGE DETECTION
#
# Use a state file per session to determine if this is the first user message.
# State file lives in a per-user 0700 directory and is keyed by session_id (falls back to project+pid).
# ──────────────────────────────────────────────────────────────────────────────

# Build a stable session key. The session id is free text (it comes from the
# agent CLI), so it never becomes part of a path as it is: only [A-Za-z0-9._-]
# survive, capped at 64 characters, plus a checksum of the raw id so two ids that
# sanitize to the same text keep separate state. The "s-" prefix means a name can
# never start with "." or "-".
engram_state_key() {
  _raw=$1
  _clean=$(printf '%s' "$_raw" | LC_ALL=C tr -c 'A-Za-z0-9._-' '_' | LC_ALL=C cut -c1-64)
  _sum=$(printf '%s' "$_raw" | cksum | cut -d ' ' -f 1)
  printf 's-%s-%s' "$_clean" "$_sum"
}

if [ -n "$SESSION_ID" ]; then
  SESSION_KEY="$(engram_state_key "$SESSION_ID")"
else
  # No session ID available — only then detect project for the fallback state key.
  SAFE_PROJECT=$(printf '%s' "$(canonical_project "$(basename "$CWD")")" | tr -cs 'a-zA-Z0-9_-' '_')
  SESSION_KEY="p-${SAFE_PROJECT}-$$"
fi

# Per-user state directory (never a shared /tmp path another user could
# pre-create or point at a symlink): $XDG_RUNTIME_DIR, else ~/.engram, plus
# "codex-state", created 0700. Nothing is injected if it cannot be trusted.
STATE_BASE="${XDG_RUNTIME_DIR:-$HOME/.engram}"
STATE_DIR="${STATE_BASE}/codex-state"
( umask 077; mkdir -p "$STATE_DIR" ) 2>/dev/null
if [ -L "$STATE_DIR" ] || [ ! -d "$STATE_DIR" ] || [ ! -O "$STATE_DIR" ]; then
  echo "$OUTPUT"
  exit 0
fi
chmod 700 "$STATE_DIR" 2>/dev/null

STATE_FILE="${STATE_DIR}/${SESSION_KEY}-tools-loaded"

# A state path that is a symlink or anything but a regular file is never
# followed or reused.
if [ -L "$STATE_FILE" ] || { [ -e "$STATE_FILE" ] && [ ! -f "$STATE_FILE" ]; }; then
  echo "$OUTPUT"
  exit 0
fi

if [ ! -e "$STATE_FILE" ]; then
  # ── FIRST MESSAGE ────────────────────────────────────────────────────────────
  # Create the state file immediately to prevent repeat injections. noclobber
  # makes the shell open it with O_EXCL: it fails instead of following a link.
  ( set -C; umask 077; : > "$STATE_FILE" ) 2>/dev/null || true

  # Inject ToolSearch + mem_context instruction.
  print_toolsearch_message
  exit 0
fi

# ──────────────────────────────────────────────────────────────────────────────
# SUBSEQUENT MESSAGES — save-nudge logic
# ──────────────────────────────────────────────────────────────────────────────

# Detect project only after the first-message path has had a chance to return.
resolve_project "$CWD"

# Bail early if we can't determine the project (ambiguous: none is invented)
if [ -z "$PROJECT" ] || [ "$PROJECT_AMBIGUOUS" = "1" ]; then
  echo "$OUTPUT"
  exit 0
fi

# Get session start time to check if session is > 5 minutes old
SESSION_START=""
if [ -n "$SESSION_ID" ]; then
  ENCODED_SESSION=$(printf '%s' "$SESSION_ID" | jq -sRr @uri)
  SESSION_START=$(curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION}" --max-time 0.2 2>/dev/null \
    | jq -r '.started_at // empty' 2>/dev/null)
fi

# Check session age — skip nudge if session is new (< 5 minutes)
if [ -n "$SESSION_START" ]; then
  SESSION_START_EPOCH=$(parse_epoch "$SESSION_START")
  if [ -z "$SESSION_START_EPOCH" ]; then
    echo "$OUTPUT"
    exit 0
  fi
  NOW_EPOCH=$(date "+%s")
  SESSION_AGE_SECS=$(( NOW_EPOCH - SESSION_START_EPOCH ))

  if [ "$SESSION_AGE_SECS" -lt 300 ]; then
    # Session < 5 minutes old — no nudge yet
    echo "$OUTPUT"
    exit 0
  fi
fi

# Fetch the most recent observation for this project (any type)
ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
LAST_SAVE_JSON=$(curl -sf \
  "${ENGRAM_URL}/observations?project=${ENCODED_PROJECT}&limit=1&sort=created_at:desc" \
  --max-time 0.2 2>/dev/null)

if [ -z "$LAST_SAVE_JSON" ]; then
  # Server not responding or slow — fail silently, no nudge
  echo "$OUTPUT"
  exit 0
fi

LAST_SAVE_AT=$(echo "$LAST_SAVE_JSON" | jq -r '.[0].created_at // empty' 2>/dev/null)

if [ -z "$LAST_SAVE_AT" ]; then
  # No observations yet — no nudge (session might just be starting)
  echo "$OUTPUT"
  exit 0
fi

# Parse last save timestamp and compare to now
LAST_EPOCH=$(parse_epoch "$LAST_SAVE_AT")
if [ -z "$LAST_EPOCH" ]; then
  echo "$OUTPUT"
  exit 0
fi
NOW_EPOCH=$(date "+%s")
ELAPSED=$(( NOW_EPOCH - LAST_EPOCH ))

# Nudge if last save was > 15 minutes ago (900 seconds), but debounce so we do
# not repeat the reminder on every message while the agent has nothing to save.
if [ "$ELAPSED" -gt 900 ]; then
  NUDGE_COOLDOWN="${ENGRAM_NUDGE_COOLDOWN_SECS:-900}"
  NUDGE_STATE_FILE="${STATE_FILE%-tools-loaded}-last-nudge"

  LAST_NUDGE_EPOCH=""
  if [ -f "$NUDGE_STATE_FILE" ]; then
    read -r LAST_NUDGE_EPOCH < "$NUDGE_STATE_FILE" 2>/dev/null || LAST_NUDGE_EPOCH=""
  fi
  # Ignore a corrupt/non-numeric state file — treat as "never nudged".
  case "$LAST_NUDGE_EPOCH" in
    ''|*[!0-9]*) LAST_NUDGE_EPOCH="" ;;
  esac

  if [ -z "$LAST_NUDGE_EPOCH" ] || [ "$(( NOW_EPOCH - LAST_NUDGE_EPOCH ))" -ge "$NUDGE_COOLDOWN" ]; then
    if [ ! -L "$NUDGE_STATE_FILE" ]; then
      ( umask 077; printf '%s' "$NOW_EPOCH" > "$NUDGE_STATE_FILE" ) 2>/dev/null || true
    fi
    OUTPUT=$(jq -n \
      '{"systemMessage": "MEMORY REMINDER: It'\''s been over 15 minutes since your last save. If you'\''ve made decisions, discoveries, or completed significant work, call mem_save now."}')
  fi
fi

echo "$OUTPUT"
exit 0
