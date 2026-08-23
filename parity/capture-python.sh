#!/usr/bin/env bash
# Capture the Python CostCrew's observable surface from a FRESH store.
#
# Fresh matters. The capture signs in, signing in appends a journal event, and
# a second capture of an already-captured instance therefore disagrees with the
# first about how many events exist. Starting from an empty store each time is
# what makes the run reproducible instead of merely repeatable.
#
# The source tree is copied, never used in place: this must not touch the
# working installation or its data.
#
#   ./parity/capture-python.sh <out-dir> [port]
#
set -euo pipefail

SRC="${COSTCREW_PY_SRC:-/Users/yukos/Development/FinOps analyst service}"
OUT="${1:?usage: capture-python.sh <out-dir> [port]}"
PORT="${2:-8422}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/costcrew-parity.XXXXXX")"

cleanup() {
  # The whole process group, not just the pid we launched.
  #
  # A plain `kill $APP_PID` left three uvicorn servers running after a green
  # run of gate-has-teeth.sh, still holding 8461-8463 and serving a workspace
  # this trap had already deleted. The gate then could not run a second time:
  # every planted-fault capture failed to bind, and the gate reported three
  # failures that were its own leftovers rather than anything about the code.
  #
  # A gate that only works the first time is not a gate.
  if [[ -n "${APP_PID:-}" ]]; then
    kill -TERM -- "-$APP_PID" 2>/dev/null || kill -TERM "$APP_PID" 2>/dev/null || true
    # Give it a moment to go, then make sure. Deleting the workspace out from
    # under a server that is still running is what produced the orphans.
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$APP_PID" 2>/dev/null || break
      sleep 0.5
    done
    kill -KILL -- "-$APP_PID" 2>/dev/null || kill -KILL "$APP_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "workspace $WORK"
rsync -a --exclude='.venv' --exclude='__pycache__' --exclude='*.duckdb' \
      --exclude='app.db*' --exclude='events.ndjson*' \
      "$SRC/costcrew/" "$WORK/costcrew/"
rsync -a --exclude='.venv' --exclude='__pycache__' "$SRC/phase0/" "$WORK/phase0/"

PY="$SRC/costcrew/.venv/bin/python"
[[ -x "$PY" ]] || { echo "no interpreter at $PY" >&2; exit 1; }

# Fault injection for gate-has-teeth.sh. The command runs inside the throwaway
# copy, never against the source tree, which is why planting a fault is safe.
if [[ -n "${COSTCREW_PARITY_PATCH:-}" ]]; then
  echo "planting fault: $COSTCREW_PARITY_PATCH"
  ( cd "$WORK/costcrew" && eval "$COSTCREW_PARITY_PATCH" )
fi

# The app seeds itself on first start when it sees no store, which is the path
# a real operator takes, so the capture exercises it rather than a prepared one.
# Say it here rather than after two minutes of waiting.
#
# A busy port made this script wait out its full 60 retries and then print the
# last 20 lines of the app log, in which "address already in use" sat at line
# 12 under a friendly banner about registering an account. The cause was
# legible only to somebody who already suspected it.
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "port $PORT is already in use; nothing was captured." >&2
  echo "  a previous run may have left a server behind: lsof -nP -iTCP:$PORT -sTCP:LISTEN" >&2
  exit 1
fi

# set -m puts the server in its own process group, so cleanup can take the
# whole group and not just the process we happen to know the pid of.
( set -m; cd "$WORK/costcrew" && "$PY" -m uvicorn app:app --port "$PORT" --host 127.0.0.1 \
    >"$WORK/app.log" 2>&1 & echo $! >"$WORK/app.pid" )
APP_PID="$(cat "$WORK/app.pid")"

for _ in $(seq 1 60); do
  sleep 2
  code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 "http://127.0.0.1:$PORT/" || true)"
  [[ "$code" == "303" || "$code" == "200" ]] && break
done
[[ "${code:-}" == "303" || "${code:-}" == "200" ]] || {
  echo "app never came up; last 20 lines of its log:" >&2
  tail -20 "$WORK/app.log" >&2
  exit 1
}

"$REPO/bin/parity" capture -base "http://127.0.0.1:$PORT" -out "$OUT"
