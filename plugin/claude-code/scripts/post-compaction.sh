#!/bin/bash
# Engram — Post-compaction hook for Claude Code
#
# When compaction happens, inject Memory Protocol + context and instruct
# the agent to persist the compacted summary via mem_session_summary.

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')

# Memory disabled (port held by another user): send and print nothing.
resolve_engram_url || exit 0

# Ambiguous project: PROJECT stays empty and project-scoped work is skipped.
resolve_project "$CWD"

# Ensure session exists
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  curl -sf "${ENGRAM_URL}/sessions" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg id "$SESSION_ID" --arg project "$PROJECT" --arg dir "$CWD" \
      '{id: $id, project: $project, directory: $dir}')" \
    > /dev/null 2>&1
fi

# Fetch context from previous sessions
CONTEXT=""
if [ -n "$PROJECT" ]; then
  ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
  CONTEXT=$(curl -sf "${ENGRAM_URL}/context?project=${ENCODED_PROJECT}" --max-time 3 2>/dev/null | jq -r '.context // empty')
fi

# Resolve protocol verbosity mode for this slug. All slim/full branching
# (including the engram-version floor check) lives in Go — see `engram
# protocol-mode`. A missing/old engram binary or an unrecognized subcommand
# never yields "slim" here, so this always defaults safely to full. $mode is
# NEVER echoed/logged to this hook's own stdout.
mode=$(engram protocol-mode claude-code 2>/dev/null)
if [ "$mode" != "slim" ]; then
  mode="full"
fi

# Inject Memory Protocol + compaction instruction + context. Only the static
# protocol prose is gated on $mode — the "CRITICAL INSTRUCTION" header below
# and the numbered recovery steps that follow it stay unconditional (they are
# the compaction-recovery contract itself, not the duplicated protocol text).
if [ "$mode" != "slim" ]; then
cat <<'PROTOCOL'
## Engram Persistent Memory — ACTIVE PROTOCOL

You have engram memory tools. This protocol is MANDATORY and ALWAYS ACTIVE.

WHO SAVES: only the agent talking with the person saves memories. If another agent launched you
(you received a task brief, not a conversation with the user), do not call mem_save, mem_update,
mem_session_summary, mem_save_prompt, mem_session_start/end or mem_judge unless your brief
explicitly asks for it. You may use mem_search, mem_context and mem_get_observation. Put durable
findings in your final reply; whoever launched you decides what to save.

### TOOLS
Under the Claude Code plugin the tools are named `mcp__plugin_engram_engram__<tool>`
(for example `mcp__plugin_engram_engram__mem_save`). Claude Code may defer them: if a tool
is not callable yet, load it with ToolSearch first (`select:mcp__plugin_engram_engram__mem_save`).

Core: mem_save, mem_search, mem_context, mem_session_summary, mem_get_observation, mem_save_prompt
Also available: mem_update, mem_suggest_topic_key, mem_session_start, mem_session_end, mem_capture_passive

### PROACTIVE SAVE (only the agent talking with the person) — do NOT wait for user to ask
Call `mem_save` IMMEDIATELY after ANY of these:
- Decision made (architecture, convention, workflow, tool choice)
- Bug fixed (include root cause)
- Convention or workflow documented/updated
- Notion/Jira/GitHub artifact created or updated with significant content
- Non-obvious discovery, gotcha, or edge case found
- Pattern established (naming, structure, approach)
- User preference or constraint learned
- Feature implemented with non-obvious approach

**Self-check after EVERY task**: "Did I just make a decision, fix a bug, learn something, or establish a convention? If yes → mem_save NOW."

### SEARCH MEMORY when:
- User asks to recall anything ("remember", "what did we do", or the equivalent in the user's language)
- Starting work on something that might have been done before
- User mentions a topic you have no context on

### SESSION CLOSE (only the agent talking with the person) — before saying "done":
Call `mem_session_summary` with: Goal, Discoveries, Accomplished, Next Steps, Relevant Files.

---

PROTOCOL
fi

# Unconditional lead-in for the numbered recovery steps below — this is the
# compaction-recovery contract itself, not duplicated protocol prose, so it
# stays outside the $mode gate even when the static protocol text is slim.
echo "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN ORDER:"

if [ -n "$PROJECT" ]; then
  PROJECT_CLAUSE=" Use project: '${PROJECT}'."
  PROJECT_CLAUSE_CTX=" with project: '${PROJECT}'"
else
  PROJECT_CLAUSE=""
  PROJECT_CLAUSE_CTX=""
fi
printf "\n1. FIRST: Call mem_session_summary with the content of the compacted summary above.%s\n" "$PROJECT_CLAUSE"
printf "   This preserves what was accomplished before compaction.\n\n"
printf "2. THEN: Call mem_context%s to recover recent session history and observations.\n" "$PROJECT_CLAUSE_CTX"
printf "   Read the returned context carefully — it tells you what was being worked on.\n\n"
cat <<'PROTOCOL'
3. If you need more detail on a specific topic, call mem_search with relevant keywords.

4. Only THEN continue working on what the user asked.

All 4 steps are MANDATORY. Without them, you lose context and start blind.
PROTOCOL

# Inject memory context if available
if [ -n "$CONTEXT" ]; then
  printf "\n%s\n" "$CONTEXT"
fi

exit 0
