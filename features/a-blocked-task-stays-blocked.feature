# language: en

Feature: A task a person blocks while its call is in flight does not get its deliverable

  @claude 2026-10-07
  Invariant 57 said what the runner does about a blocked task: it drops every
  blocked task before anything is priced or called. It also named what that
  does not cover: a task blocked while its call is already in flight still got
  its deliverable written, so a person's order to stop was undone by an answer
  that happened to arrive after it. The words below paraphrase that named
  limit; no quote of anybody is in them.

  @test:TestATaskBlockedWhileItsCallWasInFlightGetsNoDeliverable
  Scenario: The answer arrives after a person blocked the task
    Given a task whose call is in flight and a person who blocks the task
      before the model's answer comes back
    When the answer arrives
    Then no draft and no options are saved, the person's block and its reason
      are untouched, the charge the gateway settled for the call is booked on
      the task and counted against the run, and a line says the answer was
      discarded because the task was blocked

  @test:TestATaskBlockedDuringAToolRoundBooksBothRoundsAndSavesNothing
  Scenario: The block lands during a tool round of a longer call
    Given a task that asks for a tool before it answers, blocked while the
      first round is in flight
    When the second round answers
    Then both settled rounds are charged, nothing is saved, and the discard is
      said

  @test:TestSaveDraftWritesNothingForABlockedTask
  Scenario: A block that lands between the last look and the write still wins
    Given a deliverable about to be saved for a task that is blocked
    When the draft is written
    Then the write itself refuses it, because the insert and the look at the
      task's state are one statement, and no money is booked for a draft that
      was not saved

  @test:TestARunLeavesAPersonsBlockAloneAndCountsTheDiscardedAnswer
  Scenario: The run does not overwrite a person's block with its own
    Given a run in which one task is blocked by a person while its call is in flight
    When the run finishes
    Then the task is still blocked for the person's reason, it is not counted
      as done, and the summary line counts it as discarded

  @test:TestATaskNobodyBlockedStillGetsItsDeliverable
  Scenario: A task nobody blocked still gets its draft
    Given a task that stays open while its call is in flight
    When the answer arrives
    Then its draft is saved and its charge booked, and no discard line is printed
