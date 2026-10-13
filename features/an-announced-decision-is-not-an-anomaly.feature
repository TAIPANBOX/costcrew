# language: en

Feature: An announced decision is judged as a step, not raised as an anomaly

  @decided 2026-10-13
  The crew's own cost decision, announced and confirmed by the owner before
  it lands, is judged as a step: the optimisation is not raised as an
  anomaly, while a miss against what was announced still is. The owner closes
  the announcement, never the crew alone.

  A saving the crew recommended used to reach the anomaly queue first, because
  the detector judged every day against a baseline that took a month to catch
  up with the new level, and a forecast could not see a step before it landed.
  A driver.announced option names a service, a window, and the expected change
  of that service's daily cost; the detector shifts the history before the
  window by that change, and the forecast carries the step for each day of the
  window that has not landed.

  @test:TestAnAnnouncedSavingIsNotRaised
  @test:TestAnOwnersAnswerOnAnAnnouncedOptionWritesTheDriver
  Scenario: A saving announced before it lands is not raised as an anomaly
    Given a saving the owner has confirmed before it lands, with its window
      and its expected daily amount
    When the detector judges the days of that window
    Then the saving is not raised, because the baseline already sits at the
      level the decision announced

  @test:TestTheSameSavingUnannouncedIsRaised
  Scenario: The same saving, not announced, is raised
    Given the same fall in a service's daily cost, and no announcement for it
    When the detector judges the first day of the fall
    Then the fall is raised on that day, as it always was

  @test:TestASavingMuchSmallerThanAnnouncedIsRaisedNamingIt
  @test:TestTheRuleStatesTheDayAndTheExpectedLevelOnly
  Scenario: A saving much smaller than announced is raised, naming the decision
    Given a saving that falls far short of the amount the owner confirmed
    When the detector judges a day inside the window
    Then the day is raised, and the finding names the decision and states the
      day's amount against the expected level

  @test:TestARealSpikeInsideTheWindowIsStillRaised
  Scenario: A real spike on the same service inside the window is still raised
    Given an announced saving, and a spike on the same service inside its window
    When the detector judges the spike's day
    Then the spike is raised, and no other finding is added for the saving

  @test:TestAForecastFrozenBeforeTheStepCarriesTheStep
  @test:TestOnceAWindowDayLandsTheMeasuredRateWins
  Scenario: A forecast frozen before the step already carries the step
    Given a decision announced before the forecast is frozen, with a window
      that has not begun
    When the forecast is frozen
    Then the frozen figure carries the announced amount for each day of the
      window, and the basis names the decision with its amount; once a day
      of the window has landed, that day's measured rate is used instead

  @test:TestAnnouncedEffectsMeasuresTheStepAfterClose
  @test:TestTheDecisionPageSaysWhatAnAnnouncedDecisionMeasured
  Scenario: After the month closes the decision shows its announced and measured effect
    Given a closed month in which an announced decision's service moved by a
      different amount from the one announced
    When the decisions page is read for that decision
    Then it shows the announced amount beside the measured one, and whether the
      owner's answer came before the start or after it

  @test:TestTheSupervisorsPassNeverAppliesAnAnnouncedOption
  @test:TestAnAnnouncedOptionWithNoTargetWritesNothing
  Scenario: A proposal the owner has not answered changes nothing
    Given a deliverable proposing an announced decision that the owner has not
      answered
    When the supervisor's pass runs over the sprint
    Then the option is carried to the owner, no drivers row is written, and the
      detector judges the days exactly as it did before the proposal

  @test:TestAnOldDatabaseGainsTheColumnAndItsDriversReadAsBefore
  @test:TestARecurringDriverWritesNoAmount
  Scenario: A database from the previous version gains the column and its drivers behave as before
    Given a store made before announced decisions existed, with its drivers
    When the console opens it
    Then the column is added, every existing driver reads as it did, and no
      existing driver is given an amount it never had

  @test:TestTheSupervisorStampCannotApplyAnAnnouncedOption
  @test:TestNoRoleDecidesAnAnnouncedDriverAlone
  Scenario: Only the owner closes an announced decision
    Given an announced decision, which no role in the crew decides alone
    When the supervisor's own stamp is applied to it
    Then the stamp is refused, and the decision waits for the owner

  @decided 2026-10-13
  Two defects the first live run found are settled here. A service-level saving
  is split across the teams on its desk in proportion to what each spends, so
  no team is charged with the whole of it, and a team the decision names takes
  the whole of its own. A saving may not be larger than the spend it saves: it
  is refused when it is saved and again when it is applied. An increase is not
  bounded, because a model swap can multiply the spend it replaces.

  @test:TestTheServiceLevelSavingThatLandsProportionallyRaisesNothing
  @test:TestTheSplitIsProportionalToEachTeamsSpend
  @test:TestTheSplitSumsExactlyToTheStep
  Scenario: A service-level saving is split across the teams in proportion to their spend
    Given a saving announced for a service on a desk, naming no team, and four
      teams that spend on that service
    When the detector judges each team's own series
    Then each team takes its share in proportion to its median daily spend in
      the 28 days before the start, the shares sum to the saving exactly, and a
      saving that lands in proportion raises no finding

  @test:TestATeamNamedSavingMovesOnlyThatTeam
  @test:TestAMeasuredTeamDecisionReadsThatTeamsSpend
  @test:TestAnAnnouncedDriverKeepsItsTeam
  Scenario: A saving that names a team moves that team alone and is measured against its spend
    Given a saving announced for one team on a service
    When the detector judges the teams on that service, and the month is measured
    Then only the named team's series takes the step, and the measured figure
      is the named team's own spend against its own baseline

  @test:TestASavingOverTheMedianSpendIsRefused
  @test:TestAnAnnouncedSavingOverItsBoundIsRefusedWhenSaved
  @test:TestApplyRefusesASavingOverItsBoundWithItsReason
  Scenario: A saving larger than the spend it saves is refused when saved and when applied
    Given a saving of 300.00 a day on a service that spent 260.00 a day in the
      28 days before its start
    When the option is saved, or when an owner applies an option that was never saved
    Then it is refused with the reason, which names the saving, the median and
      the window, and no drivers row is written

  @test:TestASavingAtTheMedianSpendIsAccepted
  @test:TestAnAnnouncedSavingAtItsBoundIsSaved
  @test:TestAnIncreaseIsNotBounded
  Scenario: A saving equal to the spend is accepted, and an increase is never bounded
    Given a saving equal to the median daily spend, and an increase twenty
      times the spend
    When each is saved
    Then the saving equal to the median is accepted, and the increase is accepted

  @test:TestASavingWithNoSpendToMeasureIsRefused
  @test:TestANamedTeamsSavingIsBoundedByThatTeamsSpend
  @test:TestTheBoundIsTheMedianNotTheMean
  Scenario: A saving with no spend to measure is refused, and a named team's saving is bounded by its own spend
    Given a saving on a service with no spend in the 28 days before its start,
      a saving named to a team larger than that team's spend, and a service whose
      median is far below its mean
    When each is saved
    Then the first is refused as having nothing to measure against, the second
      is refused against the team's own spend, and the third is judged by the
      median, so a saving over the median is refused even when it is under the mean

  @test:TestTheDecisionPageStatesTheAnnouncedLineOnceAndShowsTheSplit
  Scenario: The decision page states the announced line once and shows each team's share
    Given an applied decision that names no team, on a desk with two teams
    When the owner reads the decision page
    Then the line opens with the announced amount under its label, and the page
      lists each team with its share of the spend and the change a day its share takes
