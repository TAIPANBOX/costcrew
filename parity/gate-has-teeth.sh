#!/usr/bin/env bash
# Prove the parity gate can tell "did not fail" from "did not run".
#
# A comparison that only ever prints PARITY is decoration. Each case below
# either plants a fault the gate MUST catch, or presents a non-fault it MUST
# NOT fire on, or takes the subject away entirely and requires the gate to say
# it measured nothing rather than passing.
#
#   ./parity/gate-has-teeth.sh <golden-capture-dir>
#
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOLDEN="${1:?usage: gate-has-teeth.sh <golden-capture-dir>}"
PARITY="$REPO/bin/parity"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/parity-teeth.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

PASS=0; FAIL=0
ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

want_red() {
  local label="$1"; shift
  if "$@" >"$TMP/out" 2>&1; then
    bad "$label (gate stayed green; it should have gone red)"
    sed 's/^/        /' "$TMP/out" | tail -4
  else
    ok "$label"
  fi
}

want_green() {
  local label="$1"; shift
  if "$@" >"$TMP/out" 2>&1; then
    ok "$label"
  else
    bad "$label (gate went red on a non-fault)"
    sed 's/^/        /' "$TMP/out" | tail -6
  fi
}

echo "[non-faults: the gate must NOT fire]"
want_green "golden compared with itself is parity" \
  "$PARITY" compare -a "$GOLDEN" -b "$GOLDEN"

echo
echo "[subject taken away: the gate must say it measured nothing]"
mkdir -p "$TMP/empty/bodies"
printf '{"base":"none","count":0,"entries":[],"digest":""}\n' >"$TMP/empty/manifest.json"
want_red "an empty capture is refused, not passed" \
  "$PARITY" compare -a "$GOLDEN" -b "$TMP/empty"
if grep -q "measured nothing" "$TMP/out"; then
  ok "and it says WHY: measured nothing"
else
  bad "it went red without saying it measured nothing"
fi

echo
echo "[planted faults: each must be caught]"

# 1. Arithmetic. One rounding step in the engine, the kind of change a port
#    makes by accident when a float lands in an int.
COSTCREW_PARITY_PATCH="sed -i '' 's/round(base \* f \/ 10) \* 10/round(base * f \/ 10) * 10 + 1/' ingest.py" \
  "$REPO/parity/capture-python.sh" "$TMP/fault-arith" 8461 >"$TMP/cap1.log" 2>&1
if [[ -f "$TMP/fault-arith/manifest.json" ]]; then
  want_red "a budget off by one is caught" \
    "$PARITY" compare -a "$GOLDEN" -b "$TMP/fault-arith"
else
  bad "fault-arith capture did not run"; tail -5 "$TMP/cap1.log" | sed 's/^/        /'
fi

# 2. Wording. A template string, the kind a port rewrites without noticing.
COSTCREW_PARITY_PATCH="sed -i '' 's/Newest first/Newest first./' templates/audit.html" \
  "$REPO/parity/capture-python.sh" "$TMP/fault-copy" 8462 >"$TMP/cap2.log" 2>&1
if [[ -f "$TMP/fault-copy/manifest.json" ]]; then
  want_red "a one-character copy change is caught" \
    "$PARITY" compare -a "$GOLDEN" -b "$TMP/fault-copy"
else
  bad "fault-copy capture did not run"; tail -5 "$TMP/cap2.log" | sed 's/^/        /'
fi

# 3. A route disappearing. The failure a port makes by forgetting a handler,
#    and the one a status-only check would miss if it only walked what exists.
COSTCREW_PARITY_PATCH="sed -i '' 's|@app.get(\"/teams\", response_class=HTMLResponse)|@app.get(\"/teams-moved\", response_class=HTMLResponse)|' app.py" \
  "$REPO/parity/capture-python.sh" "$TMP/fault-route" 8463 >"$TMP/cap3.log" 2>&1
if [[ -f "$TMP/fault-route/manifest.json" ]]; then
  want_red "a route that moved is caught" \
    "$PARITY" compare -a "$GOLDEN" -b "$TMP/fault-route"
else
  bad "fault-route capture did not run"; tail -5 "$TMP/cap3.log" | sed 's/^/        /'
fi

echo
echo "teeth: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
