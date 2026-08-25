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
#
# Each fault is planted on a COPY of $GOLDEN, mutating captured bytes
# directly instead of Python source re-captured by a running server: the
# Python original (~/Development/FinOps analyst service) was deleted
# 2026-08-25 and there is nothing left to boot. `parity mutate` and
# `parity drop` keep the copy structurally valid (sha256, bytes, count and
# digest all rewritten to match), so a red result below is compare() naming
# a real difference, not tripping over a corrupt directory.

# 1. Arithmetic. A dollar figure changed by one, the kind of change a port
#    makes by accident when a float lands in an int.
cp -R "$GOLDEN" "$TMP/fault-arith"
if "$PARITY" mutate -dir "$TMP/fault-arith" -path /kpis \
     -old '$1,650.00' -new '$1,651.00' >"$TMP/mut1.log" 2>&1; then
  want_red "a budget off by one is caught" \
    "$PARITY" compare -a "$GOLDEN" -b "$TMP/fault-arith"
  # Not vacuous: the failure must name /kpis specifically, not just exit
  # non-zero. A gate that only proves "something, somewhere differs" would
  # pass just as happily if compare() stopped looking after the first path.
  if grep -qF 'CONTENT' "$TMP/out" && grep -qF '/kpis' "$TMP/out"; then
    ok "and it names the surface: CONTENT /kpis"
  else
    bad "it went red without naming /kpis"
  fi
else
  bad "planting fault-arith failed"; tail -5 "$TMP/mut1.log" | sed 's/^/        /'
fi

# 2. Wording. One character in rendered copy, the kind a port rewrites
#    without noticing.
cp -R "$GOLDEN" "$TMP/fault-copy"
if "$PARITY" mutate -dir "$TMP/fault-copy" -path /audit \
     -old 'Newest first.' -new 'Newest first!' >"$TMP/mut2.log" 2>&1; then
  want_red "a one-character copy change is caught" \
    "$PARITY" compare -a "$GOLDEN" -b "$TMP/fault-copy"
else
  bad "planting fault-copy failed"; tail -5 "$TMP/mut2.log" | sed 's/^/        /'
fi

# 3. A route disappearing. The failure a port makes by forgetting a handler,
#    and the one a status-only check would miss if it only walked what exists.
cp -R "$GOLDEN" "$TMP/fault-route"
if "$PARITY" drop -dir "$TMP/fault-route" -path /teams >"$TMP/mut3.log" 2>&1; then
  want_red "a route that moved is caught" \
    "$PARITY" compare -a "$GOLDEN" -b "$TMP/fault-route"
else
  bad "planting fault-route failed"; tail -5 "$TMP/mut3.log" | sed 's/^/        /'
fi

echo
echo "teeth: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
