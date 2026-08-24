# CLAUDE.md, working instructions for costcrew-go

## Read before you change anything

`parity/README.md` says what the parity gate measures now and what it no
longer does. `docs/stack-connection.md` says what this console writes into the
estate's shared records and what it deliberately does not.

## What this is

A FinOps analyst console in which a crew of agents does the work, and a person
reviews it. Rewritten from the Python version, which is frozen at
`~/Development/FinOps analyst service` and is the parity reference and nothing
else.

Single static binary, pure-Go SQLite (`modernc.org/sqlite`), no build step, no
runtime to install, no network. Money is integer cents everywhere.

## Gates

```sh
go test ./...                        # 251 tests, 15 packages
./scripts/gates-have-teeth.sh        # 44 cases; needs a clean tree; ~60s
./scripts/features-are-bound.sh      # 43 scenarios, both directions
./parity/gate-has-teeth.sh parity/captures/golden
gofmt -l . && go vet ./...
```

The gates in this repo are Go tests rather than shell scripts, so
`gates-have-teeth.sh` mutates the PRODUCT and requires the test to go red.
Read its header before adding a case: two of its properties exist because Go
makes a silent pass easy, and one because a mutation that does not compile
fails `go test` with the same exit code as a caught fault.

## Hard invariants

Each one carries how it is held today. `(gate: ...)`, `(test: ...)`,
`(partly gated: ...)` or `(not enforced)`, and use the weakest one that is
true. An invariant with no check, written as though it had one, is worse than
an absent invariant.

1. **No route answers a stranger.** Every route turns an unauthenticated
   caller away, with seven exceptions listed in `publicRoutes`, each carrying
   its reason in the source. They are not one kind of thing: `/login`,
   `/logout`, `/healthz` and `/static/` genuinely answer anybody; `/signup` is
   open only while nobody can administer the installation (invariant 10); and
   `/calendar` and `/stats` are aliases that redirect to a guarded page.
   *(gate: `TestEveryRouteRequiresASession`, which walks all 48 GET routes
   registered in `server.go`. Its regexp is anchored to the registration and
   not to the string `HandleFunc`, because it once fired on a route named in a
   COMMENT, and a gate that fires on prose gets deleted the first week.)*

2. **A viewer may read and export, and may write nothing.**
   *(gate: `TestAViewerCannotWrite`, all 30 write routes against a real viewer
   session with a real CSRF token. It requires THE role refusal by its wording,
   not any refusal: accepting any message made it pass on 26 of 27 routes whose
   handlers happened to complain about something else first.)*

3. **Managing accounts is an admin's job, and it is a higher bar than acting.**
   An operator may spend the budget and may not hand anybody else the ability
   to.
   *(gate: `TestAnOperatorCannotEscalateThroughAccounts`, which checks the
   DATABASE rather than the redirect, because a handler that writes and then
   complains would pass a redirect check.)*

4. **Only the agent's owner, or an admin, manages an agent.** Remove,
   transfer, re-brief and state, plus the re-brief page itself. An admin is
   included because somebody has to clean up after a person who has left, and
   that is exactly when the owner cannot act.
   *(gate: nine tests in `internal/web/ownership_test.go`. One of them,
   `TestTheOwnerAndAnAdminCanStillManage`, exists because every other test here
   proves somebody is REFUSED and a fix that refused everybody would pass all
   of them.)*

5. **Every write route refuses a request without a valid CSRF token.** The
   check is in five places, not one: `s.checked` and four handlers with their
   own copy.
   *(gate: `TestEveryWriteRouteChecksCSRF`, 30 routes with an empty token and
   with a wrong one, requiring the CSRF wording specifically. Until
   2026-08-23 this was one route of thirty-three.)*

6. **Spend is attributed by the owner recorded ON the charge**, never by who
   owns the agent today. Otherwise an agent changing hands rewrites history for
   two people: the new owner's total jumps by an amount they never authorised
   and the previous owner's drops by the same.
   *(gate: `TestSpendByOwnerReadsTheChargeNotTheRoster` and
   `TestATransferSplitsTheSpendAtTheHandover`. A transfer moves the charges on
   OPEN work and leaves closed work with whoever authorised it; moving
   everything would mean there is no history at all.)*

7. **The same estate renders the same way every time.** Nondeterminism here
   does not look like a bug, it looks like a table whose rows moved, which a
   reader takes for new data.
   *(gate: `TestAIUnitsAreOrderedTheSameEveryCall` and
   `TestPagesRenderTheSameTwice`. The second scales its repeat count to the row
   count, because comparing two renders of a four-row table is close to a coin
   toss: ranging a Go map is a rotation from a random start, not a uniform
   shuffle, and three repeats caught a real fault only 17 times in 20.)*

8. **A right this console can grant has an explanation, and an explanation
   has a right.** Both directions, because the second rots quietly.
   *(gate: `TestEveryGrantableRightIsExplained`,
   `TestNoExplanationOutlivesItsRight`, `TestNoSkillGrantsARetiredRight`. The
   third guards the way a retired right comes back, which is not the roster but
   `RightsFor` deriving rights from skills on the next hire.)*

9. **An attestation is never invented.** A method with no evidence is a word:
   it can be neither checked nor disproved, and an identity graph that believes
   it stops asking about an agent bound to nothing. This console once derived
   attestations for 12 of 39 agents from their permission lists, which was
   worse than `none`, because idryx then stopped flagging them.
   *(gate: `ValidAttestation` plus `TestAnAttestationHasToCarryItsEvidence`,
   `TestHiringRefusesAnAttestationWithNoEvidence`,
   `TestSeedingClaimsNoAttestation`.)*

10. **Registration is open while nobody can administer this installation**,
    not while the users table is empty. The estate seeds five owner accounts
    with unusable passwords, and counting ACCOUNTS made a brand-new
    installation refuse the first person who opened it.
    *(gate: `TestSignupIsOpenUntilThereIsAnAdmin`.)*

11. **Starting the console twice changes nothing the second time.** Every
    startup step is a migration or a backfill and every one runs on every
    start.
    *(gate: `TestBackfillMandateConverges`, `TestSeedOwnersIsIdempotent`,
    `TestSeedOwnersDoesNotUndoAPlacementAPersonMade`,
    `TestEnsureOwnershipHistoryIsSafeToRunAgain`. The first exists because
    every start reported filling in a mandate for two analysts and never had:
    SQLite counts the rows an UPDATE MATCHED, not the rows whose values
    differ.)*

12. **The journal is a chain, and the audit page says where it broke rather
    than that it is broken.**
    *(gate: `TestAnEditedEntryStillBreaksTheChain`,
    `TestAWholeNumberInAnEntryDoesNotBreakTheChain`,
    `TestTheAuditPageVerifiesTheChain`. The second is there because JSON
    round-tripping turns an int64 into a float64 and the writer and the
    verifier then disagree about the same bytes.)*

13. **Figures reconcile across pages.** One number cut two ways must give one
    answer. Four separate disagreements have been found this way, and each
    looked entirely plausible from one side.
    *(partly gated: `TestOwnersAndCrewAgreeOnUnboundAgents` and the
    reconciliation tests in `internal/world`. There is no gate that walks every
    pair of pages showing the same figure, and there could not easily be one:
    the pairs are known to people and not to the code.)*

14. **Every scenario in `features/` names a test, and every name is real.**
    The scenarios are Yurii's own words, quoted above each one, and are not
    derived from the code: a scenario written by reading what was built only
    proves it can be described.
    *(gate: `scripts/features-are-bound.sh`, reachable from the suite as
    `TestFeatureBindingsHold`. It checks the POINTER in both directions and
    deliberately not whether the test asserts what the scenario says, which
    nothing mechanical can. There is no BDD runner: three runners with three
    step-definition styles across the estate would cost more surface than the
    readability they buy, and that is my call rather than his.)*

15. **Every gate can go red.** A new gate is not finished until
    `scripts/gates-have-teeth.sh` has a case that plants its fault.
    *(gate: `scripts/gates-have-teeth.sh` itself, checked by hand on
    2026-08-23: gutting an assertion reports TOOTHLESS, a dirty tree is
    refused, an already-red gate is UNJUDGEABLE rather than judged, and a kill
    restores the tree.)*

16. **A deliverable says whether a person's money bought it.** The estate
    ships 279 generated drafts; a live run adds real ones to the same table,
    with the same author, the same state and the same shape. After the first
    full run 63 real ones sat among 342 and nothing could tell them apart.
    `docs/live-agents.md` named this before the runner existed and the runner
    was built without it anyway.
    *(gate: `TestARunnerDeliverableIsMarkedLive` and
    `TestTheTaskPageShowsWhichDeliverableWasWrittenLive`, one for the writer and
    one for the reader, because a marker no page displays is not a marker. The
    column defaults to `fixture`, which is the safe direction: a row whose
    provenance is unknown must not read as evidence of a real call. The SPEND is
    named too, since 2026-08-24, on all three pages that show a mixed cost:
    `TestTheCrewPageSaysWhatOfItsFigureIsReal`,
    `TestTheCrewCostKPISaysWhatIsRealMoney`,
    `TestTheAgentCardSaysWhatOfItsCostIsReal`. One function, `crew.RealMoney`,
    writes the sentence for all three, because three copies of one fact is how
    two pages come to disagree about it. The card reads `LiveSpendBy`, per
    analyst, and its test gives a second agent 0.99 of live spend and requires
    that it not appear on the first agent's card.
    `TestTheKPISaysNothingAboutMoneyNobodySpent` holds the other direction: a
    console where no agent has run says nothing about real money.)*

17. **A run cannot walk past its ceiling by running things at once.** Each call
    reserves its worst case BEFORE it starts and settles the difference after,
    so four calls in flight cannot each pass a check against the same unspent
    balance.
    *(gate: `TestTheCeilingHoldsUnderConcurrency`, `TestAFailedCallReturnsItsReservation`.
    The first passed against a budget that ignored reservations entirely until
    it was rewritten to HOLD its reservations open: it settled each call
    immediately, so no two ever overlapped, and the one fault it existed to
    catch could not reach it.)*

18. **What the console says a run cost is what the run cost.** Cents cannot
    hold a fifth of a cent and a model call costs about that, so the rounding
    happens once, over the whole run, and the cents are handed out by largest
    remainder. Rounding per CALL recorded 0.56 for a run that billed 0.2337;
    rounding per TASK recorded the same, because the runner makes one call per
    task.
    *(gate: `TestTheLedgerDoesNotOverstateManySmallCalls`, with both faults in
    `gates-have-teeth.sh`, and `TestSettlingTheSameRunTwiceChangesNothing`
    because settling runs on every run. The test that let the second fault
    through put all 44 calls on ONE task, where per-task rounding happens to be
    right: a test can only prove what it describes.
    @measured 2026-08-24, a full run: router 0.2342, board 0.24, crew page
    3871.35 -> 3871.59.)*

19. **Nothing may move the sidebar under the cursor.** Three attempts, and the
    middle one made it worse, so all three are written down in the CSS itself.
    Sticky gave the panel its own scrollbar, invisible and reset by every page
    load. Static put it in the flow, where a trackpad's momentum slid the whole
    list after the finger had left it: Yurii clicked Accounts four times and got
    Desks, Crew and Budgets, 574px, 682px and 466px away. Fixed is the only one
    the page cannot touch.
    *(gate: `TestThePageCannotMoveTheSidebar` and `TestTheSidebarFitsAWindow`,
    which are budgets on the CSS because Go cannot lay out a page. @measured,
    Chrome at 1440x1020 with the page forced to 3000px and both nav.scrollTop
    and main.scrollTop forced to 400: nothing moved it, all 26 links visible,
    zero mismatches; 92 page-and-size combinations clean afterwards. Below
    936px of viewport the panel keeps an internal scroll, which is what any
    list too long for its box does; the structural answer is fewer than 26
    destinations and that is Yurii's call.)*

20. **A figure that mixes generated and real money says so.** Both themes are
    measured, not eyeballed: the marker's first border was 1.2:1 against its
    surface in light mode, which is the console's container vocabulary, not its
    state vocabulary.
    *(gate: `TestTheCrewPageSaysWhatOfItsFigureIsReal`,
    `TestTheLiveMarkerIsDrawnLikeAMarker`. @measured 2026-08-24: marker text
    6.53:1 light and 7.17:1 dark, border 4.8:1 and 5.2:1; the ten text pairs on
    the overview pass in both themes, thinnest slack 0.29 on the state chip.)*

## Decisions that have no gate yet

Written here so that "it holds" and "something holds it" stay different
sentences.

- **The console never reaches the network.** True today and checked by reading:
  the only outbound HTTP client in the repo is `internal/enforce`, which is a
  separate binary the console never calls, and the stack integrations are all
  behind flags that default to off. *(not enforced: nothing would catch a
  handler that grew a client.)*

- **Money is integer cents.** `money.Cents` is an `int64` and `Float()` exists
  only for presentation. *(not enforced: nothing stops a new float from being
  summed, and `TestFloatSumIsNotOrderIndependent` demonstrates why that
  matters rather than preventing it.)*

- **The agent-event stream is never fabricated.** Nothing derived is written
  into it: the card's history panels read the BOARD instead, because that
  stream is read by trailryx, heraldyx and idryx, and several hundred
  reconstructed lines with months-old dates would arrive at the end of an
  append-only record as though they were new. *(not enforced.)*

- **A viewer reads the account list and is served no controls.**
  `@yurii 2026-08-23`, asked directly: "лишай список акаунтів". The list is not
  a secret in a console where everyone with an account is a colleague. The
  CONTROLS are gated (`TestWhatAViewerCanRead`); the reasoning for showing the
  list is a decision, not an invariant.

## Standing rule

Anything written here that is not marked `@yurii` or `@measured` is `@claude`:
my reading, and re-checkable. Before writing that an invariant is held, open
the check and confirm it asserts what the sentence says. Invariant 5 said
"every write route" for a long time while one route of thirty-three was
covered, and the sentence was written before anybody counted.

## Conventions

- Comments say WHY, and name the failure that made the line necessary. Most of
  this codebase's comments are a record of something that went wrong once.
- A page states its own limits: what a figure does not include, what a gate
  does not cover, what a number is not comparable to.
- Report the slack, not the verdict. "It fits" once hid 18px.
