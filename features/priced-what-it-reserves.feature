# language: en

Feature: The number a person reads before setting a ceiling is the number the run will actually reserve

  @claude 2026-09-03
  No quote of Yurii's exists for this scenario: found by this session running
  the first real live crew task on a real Anthropic account
  (PRICE-DISPLAY-SPEC.md), not by a conversation with him. The words below are
  this session's own reading of tools/run/main.go, live.go, due.go and
  internal/deliver/estimate.go, not his.

  A task priced for anthropic or openrouter can make up to six model calls in
  one execute() (the tool-calling loop, tools/run/loop.go), each one reserved
  before the first call is made. report()'s own dry-run print, price()'s own
  per-task guard verdict, spend()'s own whole-run preflight and -due's own
  preflight all compared a ceiling against ONE call's own bound instead of
  what execute()'s reserve() would actually require. The gap went unnoticed
  on every engine that never loops (bedrock), because there the two figures
  are the same number.

  @test:TestReportsWorstCaseIsWhatTheLiveRunWouldActuallyReserve
  Scenario: The dry-run report shows what a live run would reserve
    Given a task priced for the anthropic engine, which can make several
      model calls in one run through the tool-calling loop
    When the dry-run report prices it, with no call ever made
    Then the worst case it prints, for that task and for the run's own
      total, is the same figure a live run's own reserve would require
      before letting the first call through

  @test:TestPriceRefusesATaskTheMultipliedWorstCaseCannotAffordEvenWhenTheSingleCallCanAffordIt
  @test:TestReportAndPriceVerdictNeverDisagreeOnWhichFigureIsMultiplied
  Scenario: A task's guard verdict agrees with what the live run would require
    Given a task whose per-task guard covers one call's own worst case but
      not what six rounds on the tool loop could actually cost
    When it is priced
    Then it is refused, past its guard, rather than shown as fitting inside
      it only to be refused for real once a live run tries to reserve it

  @test:TestSpendRefusesTheWholeRunBeforeTheFirstCallWhenTheMultipliedWorstExceedsTheCeiling
  Scenario: A live run refuses the whole run before the first call, not partway through it
    Given a run whose ceiling covers one call's own worst case for its one
      task but not what the tool loop could actually reserve for it
    When the run is started live
    Then it refuses before any call is attempted, the same way it already
      does when the ceiling is obviously too small, rather than starting,
      reserving the real figure per task, and only then refusing silently

  @test:TestReservedWorstCaseMultipliesOnlyTheLoopingEngines
  @test:TestEstimateWorstCaseIsUnchangedForASingleCallEngine
  Scenario: An engine outside the tool loop is priced once, never multiplied
    Given a task priced for bedrock, or any other engine that makes exactly
      one call per run
    When its worst case is computed, for a dry run or for the console's
      /cadence preview
    Then it is exactly one call's own bound, with no loop multiplier applied
      on top of it

  @test:TestEstimateWorstCaseReturnsTheReservedFigureNotOneCallsOwnBound
  Scenario: The /cadence preview shows what a live due run would reserve
    Given a cadence-due item for an analyst on the anthropic engine, which
      -due -live would run through the same tool-calling loop an ordinary
      sprint task uses
    When the console's /cadence page prices it for preview
    Then the worst case it would show is the reserved figure, loops
      included, not one call's own bound
