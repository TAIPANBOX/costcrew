#!/usr/bin/env bash
# Checks that the two required checks of .github/workflows/ci.yml, `gate` and
# `teeth`, wait for every job that does the work, and are red when one of them
# is not green.
#
# WHY
#
# `gate` and `teeth` do no work themselves. Branch protection requires those two
# names, and each is a job that `needs` the jobs doing the work. That keeps the
# required names while the work is split over jobs that run side by side, and it
# has two silent failures, neither of which prints a red line:
#
#   - a job added to the workflow that neither aggregator lists in `needs` is
#     not required by anything. It runs, it may fail, and the pull request merges;
#   - an aggregator without `if: always()` is SKIPPED when a job it needs fails,
#     and a skipped job reports no failure. GitHub counts a skipped required check
#     as passed, so the failure the aggregator exists to carry would let the merge
#     through.
#
# It also checks that each aggregator reads the result of every job it waits
# for, by name, because a `needs` list the step never looks at proves nothing.
#
# It reads the workflow as text and runs nothing.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

workflow=.github/workflows/ci.yml
if [ ! -f "$workflow" ]; then
	printf 'MEASURED NOTHING  %s is not there\n' "$workflow"
	exit 1
fi

python3 -I - "$workflow" <<'PYEOF'
import re, sys

lines = open(sys.argv[1], encoding="utf-8").read().split("\n")
jobs = {}
current = None
in_jobs = False
for line in lines:
    if re.match(r"^jobs:\s*$", line):
        in_jobs = True
        continue
    if in_jobs and re.match(r"^\S", line) and not line.startswith("#"):
        in_jobs = False
    if not in_jobs:
        continue
    m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
    if m:
        current = m.group(1)
        jobs[current] = []
        continue
    if current is not None:
        jobs[current].append(line)

if not jobs:
    print("MEASURED NOTHING  no job found under jobs: in the workflow")
    sys.exit(1)

# What each aggregator must wait for, and the jobs that are not work.
aggregators = {"gate": None, "teeth": None}
matrix_of = {"teeth": "teeth-shard"}
work = [j for j in jobs if j not in aggregators]
failed = False

def needs_of(body):
    for line in body:
        m = re.match(r"^    needs:\s*\[(.*)\]\s*$", line)
        if m:
            return [x.strip() for x in m.group(1).split(",") if x.strip()]
    return None

waited = {}
for name in aggregators:
    if name not in jobs:
        print("MEASURED NOTHING  the workflow has no job called %s, which branch protection requires" % name)
        sys.exit(1)
    body = jobs[name]
    needs = needs_of(body)
    if needs is None:
        print("NOT WAITING  %s has no needs: [...] list, so it does no waiting" % name)
        failed = True
        needs = []
    waited[name] = needs
    if not any(re.match(r"^    if:\s*always\(\)\s*$", l) for l in body):
        print("SKIPPED NOT RED  %s has no `if: always()`, so when a job it needs fails it is skipped, and a skipped required check counts as passed" % name)
        failed = True
    text = "\n".join(body)
    for need in needs:
        if "needs.%s.result" % need not in text:
            print("RESULT NEVER READ  %s waits for %s and never reads needs.%s.result" % (name, need, need))
            failed = True

# gate waits for every job that is neither aggregator nor the teeth matrix; teeth
# waits for the matrix. A job that is in neither list is required by nothing.
gate_should = sorted(j for j in work if j != "teeth-shard")
teeth_should = ["teeth-shard"]
for name, should in (("gate", gate_should), ("teeth", teeth_should)):
    missing = [j for j in should if j not in waited[name]]
    extra = [j for j in waited[name] if j not in jobs]
    if missing:
        print("NOT WAITED FOR  %s does not wait for %s, so nothing requires it" % (name, ", ".join(missing)))
        failed = True
    if extra:
        print("NO SUCH JOB  %s waits for %s, which the workflow does not have" % (name, ", ".join(extra)))
        failed = True

if failed:
    sys.exit(1)
print("ci: gate waits for %s; teeth waits for %s; both are red unless every one passed" % (", ".join(gate_should), ", ".join(teeth_should)))
PYEOF
