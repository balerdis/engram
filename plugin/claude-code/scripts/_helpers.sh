#!/bin/bash
# Engram — Shared helpers for Claude Code hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.

# Canonical project name: same rules as the engram server (lowercase, trim,
# collapse "--" and "__", "unknown" when empty).
canonical_project() {
  local n
  n=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' \
    | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e ':a' -e 's/--/-/' -e 'ta' -e ':b' -e 's/__/_/' -e 'tb')
  [ -z "$n" ] && n="unknown"
  printf '%s' "$n"
}

# Detect project name from git remote, with fallbacks (fallback only: the
# server answers GET /project/current first, see resolve_project).
# The result is canonical.
# Priority: git remote origin repo name > git root basename > cwd basename
_detect_project_raw() {
  local dir="$1"

  # Try git remote origin URL
  local url
  url=$(git -C "$dir" remote get-url origin 2>/dev/null)
  if [ -n "$url" ]; then
    # Handles both SSH (git@github.com:user/repo.git) and HTTPS (https://github.com/user/repo.git)
    local name
    name=$(echo "$url" | sed 's/\.git$//' | sed 's|.*[/:]||' | tr '[:upper:]' '[:lower:]')
    if [ -n "$name" ]; then
      echo "$name"
      return
    fi
  fi

  # Fallback: git root directory name (works in worktrees)
  local root
  root=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)
  if [ -n "$root" ]; then
    basename "$root" | tr '[:upper:]' '[:lower:]'
    return
  fi

  # Final fallback: cwd basename (current behavior)
  basename "$dir" | tr '[:upper:]' '[:lower:]'
}

detect_project() {
  canonical_project "$(_detect_project_raw "$1")"
}

# ── Port resolution ─────────────────────────────────────────────────────────
# The engram port is shared by every OS user of the machine, so it is resolved
# before every use and never used when only another uid listens on it.
# Rule (same as `engram port`): valid ENGRAM_PORT -> used if free or ours, else
# disabled; otherwise 7437 if free/ours, else 17437 + uid % 10000 if free/ours,
# else disabled. Test seams: ENGRAM_PORT_PROC_TCP4, ENGRAM_PORT_PROC_TCP6.

# Prints the uids that LISTEN on loopback/wildcard port $1 (one per line).
_engram_listener_uids() {
  local hex tcp4 tcp6
  hex=$(printf '%04X' "$1")
  tcp4="${ENGRAM_PORT_PROC_TCP4:-/proc/net/tcp}"
  tcp6="${ENGRAM_PORT_PROC_TCP6:-/proc/net/tcp6}"
  _engram_awk_listeners "$hex" "0100007F 00000000" "$tcp4"
  _engram_awk_listeners "$hex" "00000000000000000000000001000000 0000000000000000FFFF00000100007F 00000000000000000000000000000000 0000000000000000FFFF000000000000" "$tcp6"
}

_engram_awk_listeners() {
  [ -r "$3" ] || return 0
  awk -v p="$1" -v lb="$2" '
    BEGIN { n = split(lb, a, " "); for (i = 1; i <= n; i++) ok[a[i]] = 1 }
    NR > 1 && $4 == "0A" {
      split($2, x, ":")
      if ((toupper(x[1]) in ok) && toupper(x[2]) == p && $8 ~ /^[0-9]+$/) print $8
    }' "$3" 2>/dev/null
}

# Success when port $1 is free or held by our uid (or when we cannot tell).
_engram_port_usable() {
  local tcp4 uids me
  tcp4="${ENGRAM_PORT_PROC_TCP4:-/proc/net/tcp}"
  [ -r "$tcp4" ] || return 0
  uids=$(_engram_listener_uids "$1")
  [ -z "$uids" ] && return 0
  me=$(id -u)
  printf '%s\n' "$uids" | grep -qx "$me"
}

_engram_valid_port() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "${#1}" -le 5 ] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

# Sets ENGRAM_RESOLVED_PORT and ENGRAM_URL; returns 1 when memory is disabled.
resolve_engram_url() {
  local out rc port
  ENGRAM_RESOLVED_PORT=""
  ENGRAM_URL=""
  out=$(engram port 2>&1)
  rc=$?
  if [ "$rc" -eq 0 ] && _engram_valid_port "$out"; then
    ENGRAM_RESOLVED_PORT="$out"
  else
    # Only an engram without `engram port` (missing binary, unknown command) may
    # fall back; any other failure means the server says memory is disabled.
    case "$rc:$out" in
      0:*|127:*|*:"unknown command"*) ;;
      *) return 1 ;;
    esac
    port="${ENGRAM_PORT:-}"
    _engram_valid_port "$port" || port=7437
    _engram_port_usable "$port" || return 1
    ENGRAM_RESOLVED_PORT="$port"
  fi
  ENGRAM_URL="http://127.0.0.1:${ENGRAM_RESOLVED_PORT}"
  return 0
}

# ── Project resolution ──────────────────────────────────────────────────────
# Sets PROJECT (canonical) and PROJECT_AMBIGUOUS (1 when the server could not
# pick a project: then no name is invented and project-scoped work is skipped).
resolve_project() {
  local resp
  PROJECT=""
  PROJECT_AMBIGUOUS=0
  resp=$(curl -sf --max-time 1 -G "${ENGRAM_URL}/project/current" --data-urlencode "cwd=$1" 2>/dev/null)
  if [ -n "$resp" ] && printf '%s' "$resp" | jq -e 'type == "object" and has("project")' >/dev/null 2>&1; then
    PROJECT=$(printf '%s' "$resp" | jq -r '.project // ""')
    if [ -z "$PROJECT" ]; then
      PROJECT_AMBIGUOUS=1
    fi
    return 0
  fi
  PROJECT=$(detect_project "$1")
}
