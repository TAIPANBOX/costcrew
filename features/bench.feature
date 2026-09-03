# language: en

Feature: The bench scores a named cause against the truth

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @yurii 2026-09-02
  """
  мені хоча б певну кількість якихось результатів тестування було б непогано
  мати
  """

  @test:TestBenchPacketHidesTheDriverLabelAndItsKind
  @test:TestBenchPacketHidesTheDriverOnTheOtherKnownCase
  Scenario: The bench hides the cause
    Given a known anomaly whose registry entry names a driver label and a
      kind, one-time or recurring
    When the bench builds the packet an analyst would read, in its own
      hiding mode
    Then that packet names neither the driver's label nor the word of its
      kind, though everything else about the anomaly (the service, the day,
      the excess) still appears exactly as production would show it

  @test:TestMockOracleScores100PercentAcrossTheBoard
  @test:TestMockScoresRightServiceAndDayWrongCauseAndKind
  @test:TestScoreJudgesTheNamedCauseNotTheWholeBody
  Scenario: It scores a named cause against the truth
    Given a deliverable that names a cause, either the true one or a fixed
      wrong one
    When the bench extracts what was named and checks it against the known
      driver label
    Then a deliverable naming the true cause scores matched and one naming
      the wrong cause does not, judged on the NAMED cause rather than on
      whether the label's own words appear anywhere in the body

  @test:TestRealEngineWithoutLivePricesAndExits2
  @test:TestPrintWorstCasePriceNamesTheEngineModelAndPrice
  Scenario: It prices a live run before spending
    Given a real engine and no -live flag
    When the bench is asked to score N known cases on that engine
    Then it refuses before calling anything, prints the worst-case price of
      the run at that model's own published rate, and exits 2

  @test:TestBenchWritesNothingToTheEstate
  @test:TestRunningTwiceAgainstTheSameDirIsIdempotent
  Scenario: It writes nothing
    Given a seeded store and a full bench run against it, scoring with the
      mock engine
    Then no task, no artifact, no charge and no journal row exists that did
      not exist before the run, and running the bench again against the
      same store with the same seed prints the exact same report
