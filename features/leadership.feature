# language: en

Feature: The leadership page names itself, shows four live figures, and reads only

  @yurii 2026-09-03
  """
  Можливо, треба ще подивитись по інтерфейсу самого CostCrew. Можливо, там
  якісь нові речі, які можна було б додати з тих, що ми імплементували.
  """

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах.
  """

  @test:TestTheLeadershipPageShowsTheFourFiguresForTheLatestPeriod
  Scenario: A leader reads the four figures for the latest period, live
    Given a seeded estate whose allocation coverage, cost with no owner and
      AI spend attributed to an agent all compute for the latest period
    When a signed-in person opens the leadership page
    Then it shows the three figures' own values and names both the current
      period and the one before it

  @test:TestTheLeadershipPageShowsARefusedKPIAsRefusedNeverZero
  Scenario: A figure this console cannot compute reads as a refusal, never as a zero
    Given cost per business outcome, which refuses on an estate with no AI
      import
    When a signed-in person opens the leadership page
    Then that tile carries the refusal's own sentence and its own markup
      contains no "0.0"

  @test:TestTheLeadershipPageShowsASmallCostPerOutcomeWithoutLosingItsDigits
  Scenario: A small real figure keeps its own digits, never rounded away to a zero
    Given cost per business outcome computed for both the current and the
      previous period, each a real value under ten cents
    When a signed-in person opens the leadership page
    Then the tile carries both values and the delta between them, exactly
      as the KPI library itself wrote them, with no reading rounded away to
      "0.0"

  @test:TestTheLeadershipPageSaysNoPreviousPeriodForTheFirstPeriod
  Scenario: The estate's first period has nothing to compare against
    Given a store with exactly one month of charges
    When a signed-in person opens the leadership page
    Then the period line says "no previous period" rather than naming one

  @test:TestTheLeadershipPageListsOnlyPublishedLeadershipPacks
  Scenario: Only published leadership packs are listed, newest published first
    Given a draft leadership pack, a published team explainer, and two
      published leadership packs stamped in reverse creation order
    When a signed-in person opens the leadership page
    Then the draft and the team explainer are both absent, and the two
      leadership packs appear newest published first

  @test:TestTheLeadershipPageHasNoControlsEvenForAnOperator
  Scenario: A leader reads; an operator sees exactly what a leader sees
    Given an operator, who can act everywhere else in this console
    When they open the leadership page
    Then its own content carries no form, no CSRF field and no button

  @test:TestAScriptTagInAPacksTopicRendersAsTextOnTheLeadershipPage
  Scenario: A hostile pack title renders as text, never as markup
    Given a published leadership pack whose topic is a script tag
    When a signed-in person opens the leadership page
    Then the tag appears as escaped text, never executed as markup

  @test:TestTheKPIsAndExplainersPagesLinkToTheLeadershipPage
  Scenario: The KPIs and Explainers pages both point to the leadership page
    Given no sidebar entry for it, because the sidebar is already at its
      own budget
    When a signed-in person opens the KPIs page or the Explainers page
    Then each page's own header carries a link to the leadership page, on
      Explainers even before anything has ever been published
