# language: en

Feature: The anomaly desk, end to end: the owner is told, and the queue says how long it takes

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestPostingAnAnomalyDeliverableTellsTheOwner
  Scenario: The owner is told the moment the explanation is posted
    Given an analyst's deliverable on an anomaly task, ending in an
      anomaly.explain option naming a cause, saved as a draft
    When the deliverable is posted
    Then anomaly_explained is journalled at once, carrying the anomaly's own
      owner, the named cause, the option classes offered, and the artifact
      id -- before anybody has decided anything about it

  @test:TestARefusedSecondPostTellsNobodyTwice
  Scenario: A post that is refused tells nobody anything
    Given a deliverable that has already been posted once
    When posting it again is refused, because a stamp is not taken back
    Then no second anomaly_explained event is journalled: telling the owner
      is a consequence of the post actually happening, never of the attempt

  @test:TestOwnerOfAnomalyReturnsTheTeamsOwnerWhenSet
  Scenario: The team's own named owner comes first
    Given an anomaly whose team has a named owner in teams, and whose
      analyst answers to somebody else entirely
    When the owner lookup runs
    Then the team's own owner is returned, with a reason naming the team

  @test:TestOwnerOfAnomalyFallsBackToTheAnalystsOwner
  @test:TestOwnerOfAnomalyWithNoTeamAtAllFallsBackToTheAnalyst
  Scenario: Without a named team owner, the analyst's own owner answers
    Given an anomaly whose team has no row in teams at all, or no team at all
    When the owner lookup runs
    Then the analyst's own owner is returned -- tasks.owner of the task this
      anomaly opened, the same chain B3 already reads for an option's own
      owner -- with a reason saying so

  @test:TestOwnerOfAnomalyIsUnclaimedWithNeitherOwner
  @test:TestOwnerOfAnomalyWithNoTeamAndNoTaskOwnerIsUnclaimed
  Scenario: Neither the team nor the analyst has an owner
    Given an anomaly whose team has no named owner and whose own task
      carries none either
    When the owner lookup runs
    Then it answers "unclaimed" rather than an error, with a reason saying
      both paths were tried

  @test:TestOwnerOfAnomalyHandlesAQuoteInTheOwnersName
  Scenario: An owner's name survives a quote in it
    Given a team whose named owner's own name carries a single quote
    When the owner lookup runs
    Then the name comes back untouched, and the teams table is unharmed:
      proof this is a parameterised query, not string concatenation
      wearing SQL syntax

  @test:TestTheQueuePageShowsTheOwnerAndWhetherItToldThem
  Scenario: The queue shows who to tell and whether they have been told already
    Given an anomaly on the queue whose team has a named owner
    When the anomalies list is opened, before and after its deliverable is
      posted
    Then the owner's name is on the row throughout, and the "told" mark
      appears only once the event has actually gone out

  @test:TestAnomalyClosureDaysReportsTheMedianOfTwoClosedAnomalies
  Scenario: The queue says how long it takes, per desk, over the month
    Given two anomalies on the same desk, closed within the same month at
      two and at six days after detection
    When the closure KPI runs for that desk and month
    Then it reports the median of the two, four days, over exactly two
      closed anomalies

  @test:TestAnomalyClosureDaysRefusesADeskWithNoClosure
  Scenario: A desk that has closed nothing refuses rather than guessing
    Given a desk carrying open anomalies and no closed ones this month
    When the closure KPI runs for that desk
    Then it refuses, by name, rather than reporting a median of zero rows

  @test:TestAnomalyClosureDaysIsZeroWhenClosedTheSameDay
  Scenario: Closed the same day is zero days, not refused
    Given an anomaly detected and closed within the same day
    When the closure KPI runs for its desk
    Then it reports zero days, not a refusal and not a negative number

  @test:TestAnomalyClosureDaysExcludesARowWhoseDetectedAtWontParse
  Scenario: A detected_at that will not parse is excluded, not guessed at
    Given one closed anomaly whose detected_at is unreadable and another,
      on the same desk, whose detected_at is fine
    When the closure KPI runs for that desk
    Then the unreadable row is left out of the median, the readable one
      still reports, and the result says a row was excluded and why

  @test:TestTheAnomalyPageSaysHowLongItHasBeenOpen
  Scenario: The anomaly page says how long it has been open
    Given an open anomaly detected three days ago
    When its own page is opened
    Then it says "open for 3 days"

  @test:TestTheAnomalyPageSaysHowLongItTookToClose
  Scenario: Once closed, the anomaly page says how long it took
    Given an anomaly detected five days ago and dismissed today
    When its own page is opened
    Then it says "closed after 5 days", the same basis as the desk's own
      median, never a different one

  @test:TestTellingTheOwnerNeverChangesTheAnomalysOwnState
  Scenario: Nothing closes without a person
    Given an anomaly whose deliverable is about to be posted
    When it is posted, and the owner is told about it
    Then the anomaly's own state, closed_at and reason are exactly what
      they were before: telling somebody is not deciding anything, and only
      a person's stamp still closes an anomaly
