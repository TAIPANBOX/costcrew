# The parity gate

This directory holds a way to capture CostCrew's observable HTTP surface and
hold two captures against each other.

It was built before any Go existed, to make the rewrite from Python safe. That
job is finished, and the gate's use has changed with it. What follows separates
what it did then from what it is for now, because a gate whose purpose is stale
gets read as a promise it no longer keeps.

## The rewrite is over, and byte-for-byte parity with it

The Go console has features the Python one never had: an agent card, owner and
service views, budget intake, agent transfer and deletion, and the connections
to trailryx, genaryx, TokenFuse and idryx. Some Python routes are gone.

Measured, so this is not an impression:

```
parity compare -a parity/captures/golden -b parity/captures/go-1
  196 surfaces both sides
  0 gone, 0 extra, 191 differing
  /workbench?source=gcp: golden 200, actual 404
```

@measured 2026-08-23, the command above.

So do not run golden against a current build and read the result as a failure.
191 of 196 surfaces differ on purpose. `captures/golden` is now a frozen record
of what the Python version did, kept because the arithmetic behind those
figures is still the reference for anything that claims to reproduce it.

The Python source was at `~/Development/FinOps analyst service`. It was
deleted 2026-08-25, with nothing else keeping it: this is not a git repository
and there is no restore point. `captures/golden` above is what survived, and
what it is should not be overstated: a frozen capture of the OUTPUT the
Python app served on one crawl, not the source, not its arithmetic, and not
anything outside what that crawl reached. Everything under "What it does not
cover" below was already outside golden's reach before the source was gone;
deleting the source did not shrink that list, it just removed the only place
that could ever have answered it.

## What the gate is for now

Holding Go against Go: two builds, two commits, two installs. It answers one
question the test suite does not, which is whether the whole rendered surface
still says the same thing after a change nobody thought was visible.

```sh
go build -o bin/parity ./tools/parity

# each capture wants its own fresh install; the capture claims the
# installation by signing up, so it cannot run against a working one
./bin/costcrew -addr 127.0.0.1:8444 -data "$WORK/a" &
./bin/parity capture -base http://127.0.0.1:8444 -out "$WORK/before"
# ... make the change, rebuild, repeat into "$WORK/after"
./bin/parity compare -a "$WORK/before" -b "$WORK/after"
```

`compare` exits non-zero on any difference and names the first differing line
of each surface, so a failure is actionable without reaching for `diff`.

### Seven surfaces differ between any two installs, and should

Two fresh installs of the same binary differ on exactly seven surfaces, all of
them `/audit`, all of them the journal's chain hashes. Each installation has
its own chain, so its hashes are its own. That is correct, and it is the
baseline: a comparison of two installs that reports seven differing `/audit`
surfaces has found nothing.

```
0 gone, 0 extra, 7 differing
```

@measured 2026-08-23, two fresh installs of the same binary compared.

Comparing two captures of the same install after a change avoids this entirely.

### The gate can still go red

```sh
./parity/gate-has-teeth.sh parity/captures/golden
```

It plants three faults a port would plausibly make (a budget off by one, a
one-character copy change, a route that moved) and requires each to be
caught; requires one of those three to name the specific surface it changed
rather than merely exit non-zero; presents one non-fault and requires the
gate not to fire; and hands it an empty capture and requires it to say it
measured nothing rather than passing.

7 cases, 7 passed, and passed again on an immediate second run.
@measured 2026-08-25, the command above run twice.

Until 2026-08-25 the three faults were planted by patching the Python SOURCE
and re-capturing it through a booted uvicorn server (`capture-python.sh`),
and the second run above mattered because that step was what failed once: a
plain `kill` left three uvicorn servers orphaned on 8461-8463 with the
workspace deleted out from under them, and the next run reported their
leftovers as failures of its own, "address already in use" buried at line 12
of a 20-line log tail under a friendly banner about registering an account.

That whole class of failure left with the server. Since 2026-08-25 there is
no Python left to boot, so the fault is planted by copying `captures/golden`
to a temp directory and mutating the copy in place (`parity mutate`,
`parity drop`), rewriting whatever sha256, byte count, entry count and
digest the mutation touched so the copy stays a structurally valid capture
and compare() goes red on a real difference rather than a corrupt directory.
Nothing is launched, so there is no pid or port for a second run to collide
with; it is re-run anyway because a temp-directory leak is still a way for
one run to poison the next, and this gate does not get to assume it is
exempt from that just because the SPECIFIC leak it once had cannot recur.

## What it does not cover

Stated because a gate whose limits are unwritten gets trusted past them.

- **The write surface.** Only `href` links are followed. POST routes answer 405
  to a GET, and an earlier version that crawled form `action` targets filled
  123 of 400 surfaces with that noise. The POST contract needs its own gate;
  today it is held by the CSRF and two-step apply tests instead.
- **Authorisation.** The capture crawls as one signed-in admin, so it sees what
  an admin sees. Roles are held by tests instead, not by this gate:
  `TestEveryRouteRequiresASession` for the stranger,
  `TestAViewerCannotWrite` for all thirty write routes against a real viewer
  session with a real CSRF token, and
  `TestAnOperatorCannotEscalateThroughAccounts` for the rung that matters.
  All three were run against a planted fault first: removing the operator check
  from `checked` turns twenty-four routes red, and removing the admin check
  lets an operator promote themselves, which the test catches in the database
  rather than in the redirect.
- **The journal's event count and the feed's clock**, both normalised away,
  because the act of capturing moves them.
- **Chain-hash agreement between implementations.** Two implementations could
  compute different hashes over the same event and this gate would not notice,
  which is what the seven `/audit` surfaces above are hiding behind. That is a
  cross-language byte contract and belongs in a pinned-vector test. Until that
  test exists, it is an uncovered path.
- **Anything a crawl cannot reach**, including pages behind a state the seeded
  estate does not produce.

## What it has found

Two defects of the same shape, one in each language. Both were invisible to
every test that existed, and both produced output that looked entirely
reasonable.

**Python, before any Go existed.** `build_intake()` in `ingest.py` drew each
team's budget factor from a seeded RNG, consuming it in the order a `GROUP BY`
returned rows. That query had no `ORDER BY`, and DuckDB does not promise one:
six runs of the same query over the same store returned six different row
orders, so every rebuild of the estate produced different budgets. The
function's docstring said "Deterministic (seeded)" and `DATA.md` promised the
estate regenerates identically. Both were false for budgets. Fixed with
`ORDER BY 1,2,3`. The same defect is present in the handover copy that shipped.

**Go, found comparing two installs of the same binary.** `world.AIUnits()`
grouped rows in a map and returned them in map order, which Go randomises by
design. The AI desk therefore listed in a different order on every call, and a
sort with ties broke differently per request. Caught as one differing surface
among the seven expected `/audit` ones, at `/ai?sort=model&dir=desc` line 98.
Fixed by sorting the keys, which are `month|team|model` and therefore a total
order. @measured 2026-08-23.

Both are now held by tests rather than by this gate:
`TestAIUnitsAreOrderedTheSameEveryCall` in `internal/world` and
`TestPagesRenderTheSameTwice` in `internal/web`, which renders sixteen grouping
pages four times each and requires every render to be identical. The second was
run against the unfixed code and named the exact line this gate did.
