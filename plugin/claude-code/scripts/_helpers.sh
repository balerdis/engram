#!/bin/bash
# Engram — Shared helpers for Claude Code hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.

# Lowercase, including non-ASCII letters when the tools allow it (perl, or
# bash >= 4 in a UTF-8 locale); plain ASCII `tr` is the last resort.
_engram_lower() {
  if command -v perl >/dev/null 2>&1; then
    printf '%s' "$1" | perl -CIO -pe '$_ = lc'
  elif [ "${BASH_VERSINFO[0]:-0}" -ge 4 ]; then
    printf '%s' "${1,,}"
  else
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
  fi
}

# Canonical project name: same rules as the engram server (lowercase, trim,
# collapse "--" and "__", "unknown" when empty).
canonical_project() {
  local n
  n=$(_engram_lower "$1" \
    | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e ':a' -e 's/--/-/' -e 'ta' -e ':b' -e 's/__/_/' -e 'tb')
  [ -z "$n" ] && n="unknown"
  printf '%s' "$n"
}

# Project name as it is interpolated into text the model reads: printed
# VERBATIM only when it is a SINGLE token of 1-64 ASCII letters, digits and
# . _ - (no spaces: a sentence must never pass as a name); otherwise nothing is
# printed and the function returns 1, so the caller omits the whole project
# clause instead of showing an altered name that does not exist. bash cannot
# match Unicode letters reliably: a name with any non-ASCII character omits the
# clause in these hooks. Residual risk: a single token can still contain
# readable words, and the name comes from the repository or directory the user
# chose to open. Stored names are never changed by this.
safe_project_text() {
  local n="$1"
  local LC_ALL=C
  [[ "$n" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || return 1
  printf '%s' "$n"
}

# Repo name from a remote URL, same rules as the server: scheme://host without
# a path has no name; trailing slashes and a ".git" suffix (any case) go away.
_repo_name_from_url() {
  local u="$1"
  case "$u" in
    *://*)
      u="${u#*://}"
      case "$u" in
        */*) u="/${u#*/}" ;;
        *) u="" ;;
      esac
      ;;
  esac
  printf '%s' "$u" | sed -e 's|/*$||' -e 's/\.[gG][iI][tT]$//' -e 's|/*$||' -e 's|.*[/:]||'
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
    # Handles SSH (git@github.com:user/repo.git) and HTTPS URLs, with or without
    # ".git" (any case) and trailing slashes.
    local name
    name=$(_repo_name_from_url "$url")
    if [ -n "$name" ]; then
      _engram_lower "$name"
      return
    fi
  fi

  # Fallback: git root directory name (works in worktrees)
  local root
  root=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)
  if [ -n "$root" ]; then
    _engram_lower "$(basename "$root")"
    return
  fi

  # Final fallback: cwd basename (current behavior)
  _engram_lower "$(basename "$dir")"
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
# `engram port` runs with the update check off (it could add seconds to a hook)
# and stdout/stderr are read separately:
#   rc 0 + numeric stdout            -> that port;
#   rc 0 + anything else             -> disabled (fail closed);
#   rc 1 + empty stdout + stderr that says memory is disabled -> disabled;
#   any other result (127, "unknown command" from an engram that predates
#   `engram port`, crashes)          -> ENGRAM_PORT or 7437, only when free/ours.
resolve_engram_url() {
  local out err rc port errf
  ENGRAM_RESOLVED_PORT=""
  ENGRAM_URL=""
  errf=$(mktemp 2>/dev/null) || errf=/dev/null
  out=$(ENGRAM_NO_UPDATE_CHECK=1 engram port 2>"$errf")
  rc=$?
  err=""
  [ "$errf" != /dev/null ] && { err=$(cat "$errf" 2>/dev/null); rm -f "$errf"; }
  if [ "$rc" -eq 0 ]; then
    _engram_valid_port "$out" || return 1
    ENGRAM_RESOLVED_PORT="$out"
  else
    if [ "$rc" -eq 1 ] && [ -z "$out" ]; then
      case "$err" in
        *"memory is disabled"*|*"memory disabled"*|*"held by a process of another OS user"*) return 1 ;;
      esac
    fi
    port="${ENGRAM_PORT:-}"
    _engram_valid_port "$port" || port=7437
    _engram_port_usable "$port" || return 1
    ENGRAM_RESOLVED_PORT="$port"
  fi
  ENGRAM_URL="http://127.0.0.1:${ENGRAM_RESOLVED_PORT}"
  return 0
}

# ── Old server ──────────────────────────────────────────────────────────────
# A server started by an older binary keeps running after the binary is
# replaced, and nothing says so. engram_server_version_warning asks the server
# on ENGRAM_URL for /health and prints ONE warning line when it is an engram
# server OLDER than this binary, with the fix; it prints nothing otherwise (a
# newer, equal, dev or unknown version, no jq, an old binary without `engram
# version`). /health is answered by whatever holds the port, so its text is
# never shown as is: the body is capped, `.service` must be "engram" and the
# version must look like a version, or nothing is printed. Callers use it only
# for a server that was already running.

# Success when dotted version $1 is strictly older than $2; fails (also) when
# either is not a plain numeric dotted version (an optional "v" and any
# -pre/+build suffix are ignored).
_engram_version_lt() {
  local a="${1#v}" b="${2#v}" i x y
  a="${a%%[-+]*}"
  b="${b%%[-+]*}"
  [[ "$a" =~ ^[0-9]+(\.[0-9]+)*$ && "$b" =~ ^[0-9]+(\.[0-9]+)*$ ]] || return 1
  local -a pa pb
  IFS=. read -r -a pa <<< "$a"
  IFS=. read -r -a pb <<< "$b"
  for ((i = 0; i < ${#pa[@]} || i < ${#pb[@]}; i++)); do
    x=$((10#${pa[i]:-0}))
    y=$((10#${pb[i]:-0}))
    [ "$x" -lt "$y" ] && return 0
    [ "$x" -gt "$y" ] && return 1
  done
  return 1
}

engram_server_version_warning() {
  local body server_ver bin_ver
  [ -n "${ENGRAM_URL:-}" ] || return 0
  body=$(LC_ALL=C curl -sf --max-time 1 --max-filesize 4096 "${ENGRAM_URL}/health" 2>/dev/null | head -c 4096)
  [ -n "$body" ] || return 0
  server_ver=$(printf '%s' "$body" | jq -r 'select(type == "object" and .service == "engram" and (.version | type) == "string") | .version' 2>/dev/null) || return 0
  [[ "$server_ver" =~ ^[0-9A-Za-z.+_-]{1,32}$ ]] || return 0
  bin_ver=$(ENGRAM_NO_UPDATE_CHECK=1 engram version 2>/dev/null | awk 'NR == 1 && $1 == "engram" { print $2 }')
  [[ "$bin_ver" =~ ^[0-9A-Za-z.+_-]{1,32}$ ]] || return 0
  _engram_version_lt "$server_ver" "$bin_ver" || return 0
  printf 'WARNING (engram): the engram server on port %s runs version %s but the installed binary is %s. An old `engram serve` kept running after the upgrade. Stop it (find it with `pgrep -af "engram serve"`, then `kill <pid>`); the next session starts the new one.' \
    "${ENGRAM_RESOLVED_PORT:-?}" "$server_ver" "$bin_ver"
}

# ── Project resolution ──────────────────────────────────────────────────────
# resolve_project <cwd> [retries]
# Sets PROJECT (canonical), PROJECT_AMBIGUOUS (1 when the server could not pick
# a project: then no name is invented and project-scoped work is skipped) and
# PROJECT_FROM_SERVER (1 only when the name came from GET /project/current; the
# local detection is a fallback for reading, never for migrating or registering).
# With retries > 0, an unanswered request is repeated (a cold-started server
# needs a moment).
resolve_project() {
  local resp tries="${2:-0}" i=0
  PROJECT=""
  PROJECT_AMBIGUOUS=0
  PROJECT_FROM_SERVER=0
  while :; do
    resp=$(curl -sf --max-time 1 -G "${ENGRAM_URL}/project/current" --data-urlencode "cwd=$1" 2>/dev/null)
    if [ -n "$resp" ] && printf '%s' "$resp" | jq -e 'type == "object" and has("project")' >/dev/null 2>&1; then
      PROJECT=$(printf '%s' "$resp" | jq -r '.project // ""')
      PROJECT_FROM_SERVER=1
      if [ -z "$PROJECT" ]; then
        PROJECT_AMBIGUOUS=1
      fi
      return 0
    fi
    [ "$i" -ge "$tries" ] && break
    i=$((i + 1))
    sleep 0.3
  done
  PROJECT=$(detect_project "$1")
}
