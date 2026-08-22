# The parity gate

CostCrew is being rewritten from Python to Go. This directory holds the thing
that makes that safe: a way to hold the two implementations against each other
and see, byte for byte, where they disagree.

It exists before any Go code on purpose. A rewrite without an oracle is a
rewrite you cannot finish, because "it looks right" is not a finishing
condition and 10 479 lines of financial arithmetic is not something to eyeball.

## What it compares

The **observable HTTP surface**, not any function API. That is the only
contract both implementations must honour, and one crawl of it exercises
routing, templates and the engine's arithmetic together.

A capture walks the read surface from a signed-in admin session, normalises the
handful of genuinely volatile bytes, and writes one file per surface plus a
manifest of hashes. 196 surfaces at the current sampling.

## Run it

```sh
go build -o bin/parity ./tools/parity

./parity/capture-python.sh parity/captures/golden        # the reference
./parity/capture-python.sh parity/captures/actual
./bin/parity compare -a parity/captures/golden -b parity/captures/actual
```

`compare` exits non-zero on any difference and names the first differing line
of each surface, so a failure is actionable without reaching for `diff`.

## Two rules that keep it honest

**Fresh store every time.** The capture signs in, and signing in appends a
journal event, so a second capture of an already-captured instance disagrees
with the first about how many events exist. `capture-python.sh` copies the
source to a throwaway workspace and lets the app seed itself from empty. It
never touches the working installation.

**The gate must be able to go red.** `gate-has-teeth.sh` plants three faults a
port would plausibly make (a budget off by one, a one-character copy change, a
route that moved) and requires each to be caught; presents one non-fault and
requires the gate not to fire; and hands it an empty capture and requires it to
say it measured nothing rather than passing. 6 cases, all green.

## What it does not cover

Stated because a gate whose limits are unwritten gets trusted past them.

- **The write surface.** Only `href` links are followed. POST routes answer
  405 to a GET, and an earlier version that crawled form `action` targets
  filled 123 of 400 surfaces with that noise. The POST contract needs its own
  gate.
- **The journal's event count and the feed's clock**, both normalised away,
  because the act of capturing moves them.
- **Chain-hash agreement between implementations.** Normalised away for the
  same reason, and this one costs real coverage: two implementations could
  compute different hashes over the same event and this gate would not notice.
  That is a cross-language byte contract and belongs in a pinned-vector test,
  the way the rest of the estate pins theirs. Until that test exists, it is an
  uncovered path.
- **Anything a crawl cannot reach**, including pages behind a state the seeded
  estate does not produce.

## What it found before any Go existed

One real defect in the Python version, which is the reason to build the oracle
first rather than after.

`build_intake()` in `ingest.py` drew each team's budget factor from a seeded
RNG, consuming it in the order a `GROUP BY` returned rows. That query had no
`ORDER BY`, and DuckDB does not promise one: **six runs of the same query over
the same store returned six different row orders**, so every rebuild of the
estate produced different budgets. The function's own docstring said
"Deterministic (seeded)" and `DATA.md` promised the estate regenerates
identically. Both were false for budgets.

Fixed by adding `ORDER BY 1,2,3`. The same defect is present in the handover
copy that shipped.
