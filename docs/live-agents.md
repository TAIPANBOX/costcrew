# Making the analysts actually write

A plan, written before any of it exists and before anything is spent, because
the first version of this spends real money on somebody's account.

`@claude` 2026-08-24 throughout, unless a line says otherwise. Nothing here has
been built or measured; the numbers that ARE measured say so.

## What is there now, and what is not

The console has a board, a roster, guards, a review flow and a journal. What it
does not have is anything that calls a model.

Measured 2026-08-24 by reading the tree: `internal/engines` checks whether an
environment variable is set or a binary is on PATH and never dials the
`BaseURL` it stores; there is no `exec.Command`, `http.Post` or
`http.NewRequest` anywhere outside `internal/enforce`, which is a separate
binary the console does not call; and every artifact in the database was
written by one INSERT in `internal/crew/seed.go`.

The code says so plainly in `engines.Dry`:

> With nothing configured the console still works: every figure the engine
> computes is real, and the analysts simply do not write.

So adding an API key today changes exactly one thing: the Engines page reads
`ready` instead of `not set up`. No call, no token, no charge.

## The two questions to answer before the first line

### 1. What stops it

The console has guards and does not enforce them. `CheckGuards` runs once at
startup, reports who went past, and returns. That is the right design for a
console that watches somebody else's spend, and the wrong one for a process
that spends on its own.

An executor without a real stop is a bill that grows until a person looks.

What the guards would bound, measured from the fixture on 2026-08-24:

| | |
|---|---|
| per-task guard | 310 tasks, avg **21.92**, min 15.00, max 29.91 |
| monthly guard | 39 analysts, **92.18** each, **3595.00** in total |
| open right now | 22 tasks carrying **430.98** of guard between them |

So one sprint, every open task run once, each using its whole guard, is
bounded at **430.98 USD**. That is the number to react to, not the average.

The stop has to be in the call path, and it has to be arithmetic rather than a
report:

- **Per call**: refuse to start a call whose worst-case cost would take the
  task past its remaining guard. Worst case, not expected: a model's output
  length is not known in advance, so the bound is `max_tokens` at the output
  price.
- **Per task**: the guard on the row, already there and already the number the
  board shows.
- **Per run**: a ceiling for the whole sprint, passed on the command line, so
  a mistake anywhere below is bounded by one number a person typed.
- **Per day**: the same across runs, because "I ran it twice" is the shape most
  overspends have.

None of these needs TokenFuse. TokenFuse is what makes a stop hold across
processes and across machines; a single executor on one box can hold its own.

### 2. What happens to the estate

Today every figure is generated and internally consistent, and all of today's
checking rests on that: `3871.35` on the crew page is the same number on the
KPI page because both count the same generated rows.

Real calls put real charges beside generated ones in the same column. Two kinds
of number under one heading is the exact fault this console spends its time
catching in other people's data.

So: **a live run does not write into the seeded estate.** It writes its own
rows, marked, and every page that sums them says which kind it is summing.
Concretely, a `source` on the charge: `fixture` or `live`. The alternative,
keeping a separate database, sounds cleaner and is worse: the point is to see
the crew's real cost NEXT to the estate it is analysing, and two databases
cannot be added up at all.

## The shape

One binary, `tools/run`, not the console. The console stays a console: it
reads, shows and records, and it does not hold credentials or make outbound
calls. That separation already exists for enforcement and it should not be
broken for this.

```
tools/run -data ./local -sprint 2026-W34 -ceiling 25.00 -dry
```

`-dry` prints what it would do and what the worst case costs, and calls
nothing. It is the default; spending needs the flag removed.

What one task becomes:

1. **Take** a queued task. Its `Goal`, its `Assignee`'s mission, rights and
   engine, and the anomaly it came from if it did, are the prompt's material.
   Nothing else: an analyst with `figures-read` may be given figures, and one
   without it may not.
2. **Price** the call before making it. `max_tokens` times the engine's output
   price, plus the prompt at the input price, against what is left of the
   task's guard and the run's ceiling. Refuse here, loudly, or proceed.
3. **Call** the engine the analyst was hired with. `claude-cli` runs the local
   binary; the keyed engines are HTTP.
4. **Record** the artifact as a `draft`, exactly as the seed does, and the
   charge as `live`. A draft, never a post: **only a person's stamp publishes**,
   and that is an invariant this console already holds.
5. **Journal** it. The chain already takes `agent_*` events and the stack
   already reads them.

Failure is a `blocked` task with the reason, which the board already renders
and the agent card already shows under "Where it stopped".

## What is missing that is not obvious

**Prices are prose.** `internal/engines` carries "USD 0.27 per million input
tokens, USD 1.10 per million output" as a sentence for a human. Step 2 needs
those as numbers, per engine, per model, and they change. That is a small piece
of data and a real maintenance surface: a stale price makes the stop wrong in
whichever direction the price moved.

**Models are not chosen anywhere.** The hire form picks an engine, which is a
route and a bill, not a model. Either the executor picks a default per engine
and says which, or the roster grows a model field. The first is less to get
wrong.

**The estate's own AI desk already has model prices**, in `world.unitRate`, per
model, used to generate the fixture. Those are fixture economics and must not
be reused as real prices; that would be the same class of mistake as counting
generated and live charges together.

## What I would do first, and what it would cost

The smallest thing that answers "does this work": **one task, one analyst, one
engine, dry then live.**

- `-dry` on the whole open sprint first: 22 tasks, prints a worst case of
  **430.98** and calls nothing. Free, and it proves the pricing arithmetic
  before any of it matters.
- Then one real task under a `-ceiling 1.00`. On `claude-cli` that comes out of
  a subscription and costs nothing extra. On OpenRouter or Anthropic it is
  cents, and the ceiling is the guarantee rather than the estimate.

I would not run a sprint until a `-dry` and a single live task have both been
seen, and I would not raise the ceiling above a dollar until the per-call
refusal has been watched refusing something.

## What this is not

It is not needed for the pilot. The build in `~/Desktop/CostCrew-for-Tania` is
a finished console whose figures are checkable; its analysts not writing live
is not a gap a tester will find, because nothing on any page claims they do.

It is not TokenFuse. TokenFuse is the stop that holds when many processes spend
against one budget. This is one process holding its own, and building it does
not make the other unnecessary; it makes the seam where the other plugs in.
