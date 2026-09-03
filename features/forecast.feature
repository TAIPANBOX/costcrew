# language: en

Feature: The forecast that is scored

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestForecastingSectionShowsDriverLines
  Scenario: A known driver moves the projection and is named
    Given a registered driver whose own window falls inside the month being
      projected
    When the forecaster's packet is built for that desk
    Then the projection is moved by the driver's own measured effect instead
      of the plain run rate blending it away, and the packet names the
      driver, its kind and its window

  @test:TestApplyForecastFreezeUsesTheOptionsSummaryAsTheBasis
  Scenario: The freeze is a proposal
    Given a forecaster's deliverable ending in a forecast.freeze option
    When the supervisor applies that option
    Then the month is frozen under the supervisor's own decision, and the
      forecaster's own written explanation becomes the recorded basis

  @test:TestForecastingSectionShowsTheMissWithItsMissedDriver
  Scenario: The miss names what was missed
    Given a frozen month that has since closed, and a driver registered only
      after the freeze that explains the gap
    When next month's packet is built for that desk
    Then it carries the miss -- frozen, actual, and the difference -- and
      names the driver the freeze could not have known about
