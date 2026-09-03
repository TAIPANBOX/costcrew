# language: en

Feature: The data-quality analyst can stop the crew

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestASourceWithNoChargeForTStaleDaysIsReportedStale
  Scenario: Stale data is said, with the days
    Given a source with no charge for T.stale days
    When the data-quality analyst measures the estate
    Then it reports that source stale, naming how many days it has gone
      without a charge

  @test:TestApplyDataHaltSuspendsTheDesksAnalystsAndDueSkipsIt
  Scenario: A halt stops the desk's crew and says why
    Given a data-quality finding names a desk and a reason in a data.halt
      option
    When the supervisor applies it
    Then every active analyst on that desk is suspended with the reason,
      and the crew's cadence work on that desk is skipped and says why

  @test:TestAHaltOlderThanTStaleDaysIsCarriedToTheOwnerBySupervise
  Scenario: A halt that lasts goes to the owner
    Given a desk has been halted for longer than T.stale_days
    When the supervisor's pass runs
    Then the halt is carried to the owner in a decision request, naming the
      desk and the reason

  @test:TestLiftHaltReactivatesTheDesksAnalysts
  Scenario: A person lifts it
    Given a desk is halted
    When an operator lifts it on the desk page, with a reason
    Then the desk's suspended analysts return to active and the reason is
      journaled
