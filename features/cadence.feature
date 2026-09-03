# language: en

Feature: Cadence runs, and a person switches it on

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestDueWithTheSwitchOffExitsTwoAndCreatesNothing
  Scenario: Nothing runs until a person switches it on
    Given the console's cadence switch is off
    When the runner is asked for cadence-due work, live
    Then it refuses before touching anything, and no sprint or task is created

  @test:TestDueRefusesBeforeAnyCallWhenWorstExceedsTheCeiling
  Scenario: A run never spends past the ceiling
    Given the switch is on and today's cadence-due work would cost more at
      worst than the ceiling allows
    When the runner is asked for cadence-due work, live
    Then it refuses before making any call, and no sprint or task is created

  @test:TestDueLiveCreatesRunsAndEmitsCrewRan
  Scenario: A run says what it did and what it cost
    Given the switch is on and the worst case is inside the ceiling
    When the runner is asked for cadence-due work, live
    Then it creates the due work under a sprint labelled for the day, runs
      it, and journals one crew_ran event naming the tasks and the cost
