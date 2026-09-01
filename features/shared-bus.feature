# language: en

Feature: What this console puts on the shared bus is something the estate can read

  This console is a guest producer of the estate's event bus. The envelope it
  writes is agent-passport's, and that contract's `severity` is a CLOSED enum:
  info, low, medium, high, critical. A value outside it is not a stylistic
  choice, because a consumer that validates refuses the whole line and the
  event this console wrote is then in nobody's record.

  Found on 2026-08-28 while reading what registering this console in the
  estate's own registry would take, which is what that registry's preamble
  asks for: a row there is a claim checked against the producer's code.

  # @test:TestEverySeverityThisConsoleEmitsIsOneTheEnvelopeAllows
  Scenario: A severity the shared envelope does not carry is refused here
    Given the emitter that writes this console's events
    When it is handed a severity outside the envelope's closed enum
    Then it refuses rather than writing a line every validating consumer drops

  # @test:TestEveryGuardBandEmitsASeverityTheEnvelopeAllows
  Scenario: Every band of the monthly guard reports a severity the estate accepts
    Given three analysts past their monthly guard, one in each band
    When the guard check runs
    Then each event carries the severity its band means, and all three are
      values the shared envelope allows

  # @test:TestWireTypesIsExactlyWhatTheCallSitesProduce
  Scenario: The estate can be told what this console emits, and be told the truth
    Given the list of wire types this console declares
    When it is compared with every emit call site, translations applied
    Then the two are exactly equal, and a kind built rather than written that
      nothing can resolve is a failure rather than a silent omission

  # @test:TestEveryBinaryThisRepositoryBuildsIsDeclaredAndTheReverse
  Scenario: What this repository contributes is declared here and cannot go stale
    Given components.json at the root
    When it is compared with every binary the module builds and every flag the
      console defines
    Then the sets are equal in both directions, because the only thing that
      knows what this repository builds is this repository

  # @test:TestTheConsoleAnswersItsDeclaredHealthPathWithNoCredential
  Scenario: The declared health path answers a caller holding nothing
    Given the console started with only the flags the declaration names
    When the declared health path is fetched with no session and redirects are
      not followed
    Then it answers 200, because a launcher polls it and a launcher holds no
      session, and a probe that followed a redirect would read a login page as
      health

  @measured 2026-09-01, a live GCP cluster, `kubectl -n agent-stack logs job/crew-1`
  """
  A crew run made 42 model calls costing USD 0.2485 and the bus carried 26
  lines, every one of them written by the CONSOLE at startup and none by the
  run. The half of this console that spends real money was the half the record
  plane could not see. docs/live-agents.md had named this as step 5 of the
  executor; it was the step that was not built.
  """

  # @test:TestALiveCallReachesTheSharedBus
  Scenario: A call somebody paid for is on the record
    Given a runner with a bus and a trust domain
    When it records what came back from a live call
    Then the bus carries a tool_call for that analyst, with the tokens, the
      engine and what it cost, because what a call cost is the only reason a
      finops plane emits one at all

  # @test:TestEveryCallInOneInvocationSharesTheRunID
  Scenario: One invocation is one run
    Given three calls made by one invocation of the runner
    When their events reach the bus
    Then all three carry the same run id, because the record plane shards and
      indexes by run and a query would otherwise answer with one row where the
      answer is three

  # @test:TestARunWithNoBusStillSavesItsDeliverable
  Scenario: A runner with no bus configured still does its work
    Given a runner nobody pointed at a bus
    When it records a call
    Then the deliverable and the money are written anyway, because the estate
      integration is off by default and making it mandatory is the opposite of
      what this contract is

  # @test:TestTheRunnerOpensARealBusFromItsFlags
  Scenario: The runner builds its own bus, not one a test handed it
    Given the runner's own flags for the events file and the trust domain
    When it opens the bus
    Then it holds a live emitter minting under the domain it was given, because
      every other scenario here proves what happens once a bus arrives and none
      of them can see whether the binary ever makes one

  # @test:TestABusWithNoTrustDomainIsRefusedRatherThanDefaulted
  Scenario: A bus with no trust domain is refused before anything is spent
    Given an events file and no trust domain
    When the runner opens the bus
    Then it refuses, because an event minted under a domain the record plane
      was not given is refused as foreign and counted, and on a live cluster a
      whole namespace of events read as a quiet night that way
