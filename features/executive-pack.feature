# language: en

Feature: The executive reporter's fortnight is four numbers and why each moved

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestExecutiveSectionCarriesTheFourNumbers
  @test:TestExecutiveSectionShowsTheLastExplanationOnTheDeskThatMovedMost
  Scenario: Four numbers, and why each moved, from what the desks posted
    Given the KPI library's allocation coverage, the cost with no owner, AI
      spend attributed to an agent and cost per business outcome, each with
      its value the period before, and a posted explanation on the desk
      whose spend moved most this period
    When the executive reporter's packet is built
    Then it carries all four figures with their previous value and the
      delta, and the last posted explanation on the desk that moved most,
      never a template sentence standing in for either

  @test:TestExecutiveSectionShowsARefusedKPIAsRefusedNeverZero
  @test:TestExecutiveNeverGivesARefusedKPIAValue
  Scenario: A refusal is said as one, never as a number
    Given cost per business outcome, which this console cannot compute
      because no outcome metric is connected yet
    When the executive reporter's packet is built
    Then that figure reads "refused" with its own reason, and never as a
      zero a reader could mistake for a real reading

  @test:TestApplyExplainerPublishPublishesTheArtifactsBodyAsAnExplainer
  @test:TestTheLeadershipPageShowsTheExecutivePackOnlyAfterAStamp
  Scenario: The pack reaches leadership only through a stamp
    Given the executive reporter's deliverable, posted, ending in an
      explainer.publish option whose summary is the pack's own title
    When a person's stamp applies that option
    Then the artifact's own body is published as an explainer addressed to
      leadership, and the leadership page shows it with its date and its
      four numbers as of then; before that stamp, nothing on that page
      names it at all

  @test:TestExecutiveSaysNoPreviousPeriodForTheFirstPeriod
  Scenario: The estate's first period has nothing to compare against
    Given an estate with exactly one month of charges
    When the executive reporter's packet is built
    Then each of the four figures says "no previous period" rather than a
      delta computed against nothing
