# language: en

Feature: A driver written from an option carries the window the option named

  @claude 2026-09-03
  No quote of Yurii's exists for this scenario: found by Yurii reading
  internal/finops/apply.go while C3 landed ProjectWithDrivers ("recurring
  ones repeat by their window"), and this session's own reading of
  internal/detect.Driver.Covers and internal/world/series.go's Drivers() is
  what turns that reading into the rule below, not his own words about it.

  A recurring driver applied from an option used to get a one-day window
  and behave, in every number the forecast and the detector produce, exactly
  like a one-time one while the word "recurring" stayed beside it.
  driver.recurring and driver.one-time now carry a structured target naming
  the window, the same way allocation.rule carries a rule id and a method
  (costcrew#31); a class asked to write a fact it was not given refuses
  rather than guesses one from the wall clock.

  @test:TestApplyDriverRecurringWritesADriversRow
  @test:TestDriverRecurringWithAValidTargetIsSaved
  Scenario: A recurring driver says when it is expected
    Given a driver.recurring option naming a target, the window during which
      the rhythm is expected
    When the option is saved and then applied
    Then the drivers row carries that window, start to end, never a single
      day dated to whenever Apply happened to run

  @test:TestDriverRecurringWithNoTargetIsRefused
  @test:TestDriverOneTimeWithNoAnomalyAndNoTargetIsRefused
  @test:TestApplyDriverRecurringWithNoTargetWritesNoDriversRow
  Scenario: A driver never gets a window nobody gave it
    Given a driver.recurring option with no target, or a driver.one-time
      option with no target on a task with no anomaly of its own
    When the option is saved
    Then it is refused whole, with the reason, and nothing is written; an
      option that reaches Apply anyway by bypassing that gate still writes
      no drivers row and Apply says so in its own returned error

  @test:TestApplyDriverOneTimeOnAnAnomalyKeepsTheAnomalysDay
  @test:TestDriverOneTimeOnAnAnomalyTaskNeedsNoTarget
  Scenario: A one-time driver is the anomaly's own day
    Given a driver.one-time option on a task that came from an anomaly
    When the option is saved with no target and then applied
    Then the drivers row's window is the anomaly's own day, start and end
      both, because that day IS the driver and there is nothing to ask
