#!/bin/sh
# SessionStart hook for the codex-lang-ensure plugin.
#
# Codex talks only to the Front Door. This hook starts that door and the
# Local Gateway (via the Watchdog) so a session never hits a refused
# connection. If the Local Gateway is still down when the hook exits,
# turns fail closed with HTTP 403 instead of leaking untranslated Han.
#
# Always tries to start both. A Control Page "front off" does not survive
# the next session: the 403 contract is the point of the plugin.
#
# Fast, silent on success, idempotent under concurrent sessions: a lost
# bind race just means the other session already started the process.
# Never prints prompts, bodies, headers, or keys. Exit code is always 0 so
# a broken local service never blocks session start; the 403s speak then.
set -eu

FRONT_PORT="${CODEX_FRONT_PORT:-18787}"
UI_PORT="${CODEX_UI_PORT:-18786}"
BACKEND="http://127.0.0.1:18788"
LOG_DIR="${HOME}/.codex/logs"
LOG_FILE="$LOG_DIR/ensure.log"

log() {
  mkdir -p "$LOG_DIR" 2>/dev/null || true
  printf '%s %s\n' "$(date '+%F %T')" "$1" >>"$LOG_FILE" 2>/dev/null || true
}

find_bin() {
  if command -v "$1" >/dev/null 2>&1; then
    command -v "$1"
  elif [ -x "$HOME/.local/bin/$1" ]; then
    printf '%s' "$HOME/.local/bin/$1"
  else
    printf ''
  fi
}

front_up() {
  curl -fsS --max-time 2 "http://127.0.0.1:$FRONT_PORT/healthz" >/dev/null 2>&1
}

control_up() {
  curl -fsS --max-time 2 "http://127.0.0.1:$UI_PORT/api/state" >/dev/null 2>&1
}

read_backend_from_state() {
  STATE=$(curl -fsS --max-time 2 "http://127.0.0.1:$UI_PORT/api/state" 2>/dev/null) || return 0
  PARSED=$(printf '%s' "$STATE" | python3 -c 'import json,sys
try:
  s=json.load(sys.stdin)
  print(s.get("gatewayPort") or "")
except Exception:
  print("")
' 2>/dev/null || printf '')
  if [ -n "$PARSED" ]; then
    BACKEND="http://127.0.0.1:$PARSED"
  fi
  fp=$(printf '%s' "$STATE" | python3 -c 'import json,sys
try:
  s=json.load(sys.stdin)
  print(s.get("frontPort") or "")
except Exception:
  print("")
' 2>/dev/null || printf '')
  if [ -n "$fp" ]; then
    FRONT_PORT=$fp
  fi
}

if ! control_up; then
  WATCHDOG_BIN=$(find_bin codex-watchdog)
  if [ -n "$WATCHDOG_BIN" ]; then
    log "watchdog $UI_PORT down, starting"
    setsid "$WATCHDOG_BIN" --no-tray >>"$LOG_FILE" 2>&1 < /dev/null &
  else
    log "watchdog $UI_PORT down, codex-watchdog not on PATH nor ~/.local/bin"
  fi
fi

i=0
while [ "$i" -lt 20 ]; do
  if control_up; then
    break
  fi
  sleep 0.2
  i=$((i + 1))
done

if control_up; then
  read_backend_from_state
  curl -fsS --max-time 3 -X POST "http://127.0.0.1:$UI_PORT/api/front" \
    -H 'Content-Type: application/json' \
    -d '{"enabled":true}' >/dev/null 2>&1 || true
  read_backend_from_state
fi

if ! front_up; then
  FRONT_BIN=$(find_bin codex-fronthost)
  if [ -n "$FRONT_BIN" ]; then
    log "front $FRONT_PORT down, starting (backend $BACKEND)"
    setsid "$FRONT_BIN" -port "$FRONT_PORT" -backend "$BACKEND" >>"$LOG_FILE" 2>&1 < /dev/null &
  else
    log "front $FRONT_PORT down, codex-fronthost not on PATH nor ~/.local/bin"
  fi
fi

i=0
while [ "$i" -lt 20 ]; do
  if front_up; then
    exit 0
  fi
  sleep 0.2
  i=$((i + 1))
done
log "front $FRONT_PORT still down after ~8s; turns will 403 until it is up"
exit 0
