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
  [[ -n "${APP_PID:-}" ]] && kill "$APP_PID" 2>/dev/null || true
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
( cd "$WORK/costcrew" && "$PY" -m uvicorn app:app --port "$PORT" --host 127.0.0.1 \
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
