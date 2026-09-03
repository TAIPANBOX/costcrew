# language: en

Feature: An analyst remembers what it posted, and the drivers reach back six months

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestOwnHistoryShowsTheAnalystsOwnLastPostedDeliverable
  @test:TestOwnHistoryShowsTheFateOfEveryOptionState
  Scenario: An analyst sees what it posted before, and what became of it
    Given an analyst who has posted deliverables before on this desk, each
      ending in options that were later applied, refused, not chosen or
      carried to an owner
    When it is handed a new task on the same desk
    Then its packet carries the last three of those deliverables, newest
      first, each with the task it answered, the start of its body, and the
      fate of every option it ended in

  @test:TestDriversSectionReachesOneHundredTwentyDays
  @test:TestDriversSectionCapsAtTwentyFourWithAndNMore
  Scenario: It sees six months of drivers, not ninety days
    Given a driver on the anomaly's own service and desk that started four
      months ago, and thirty more inside the window
    When the packet for an anomaly on that service is built
    Then the four-month-old driver appears, where the old ninety-day window
      would have left it out, and the list itself stays at twenty-four rows,
      newest first, with a line naming how many more there are

  @test:TestOwnHistoryNeverCrowdsOutTheAnomalyUnderTheCap
  Scenario: Memory never crowds out the evidence
    Given an analyst whose own history on this desk is three full
      deliverables, on a task whose anomaly, series and drivers already fill
      most of the packet's own bound
    When the packet is built and the whole would run past the 12 KiB cap
    Then the anomaly stays whole, and it is the history section, appended
      last, that is cut
