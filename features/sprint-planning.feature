# language: en

Feature: The supervisor plans the sprint he describes

  @yurii 2026-09-02
  """
  він вже сам розподіляє це все між агентами... в залежності від моделі, від
  всього, задачі.
  """

  @yurii 2026-09-02
  """
  Вони мають вирішувати це все згідно своїх посадових інструкцій.
  """

  @test:TestGoalNamingASkillAddsAnItemForTheAnalystThatHoldsIt
  Scenario: The sprint's goal names a skill, and the analyst holding it gets the item
    Given a sprint form whose goal names a skill from the roster's own taxonomy
    When the sprint is proposed
    Then the active analyst holding that skill gets one item, its Why naming
      the skill the goal named

  @test:TestAWeeklyAnalystDueByCadenceGetsOneItem
  Scenario: An analyst overdue by its own cadence is due, with no goal typed at all
    Given an active analyst whose cadence is weekly and whose last posted
      deliverable is eight days before the sprint's start
    When the sprint is proposed
    Then that analyst gets one item, titled from its own job description and
      naming the cadence and the date of the last post

  @test:TestAReturnedArtifactProducesAReworkItemWithTheReasonVerbatim
  Scenario: A returned deliverable comes back as rework, reason intact
    Given a deliverable returned to the analyst that wrote it, with a reason
    When the sprint is proposed
    Then a "Rework:" item goes to the same analyst, carrying the return
      reason verbatim as its goal

  @test:TestADecisionRequestWithCarriedOptionsProducesOneSupervisorItem
  Scenario: An open decision request shows the person that it is waiting
    Given a decision request whose options are still carried, unanswered
    When the sprint is proposed
    Then one item goes to the supervisor naming how many options are
      waiting, because the crew cannot answer it on its own

  @test:TestAnAnalystWithNoHeadroomIsSkippedAndTheItemSaysSo
  Scenario: An analyst with no headroom left this month is skipped, not silently chosen
    Given two analysts who could take an anomaly's item, one of them already
      spent through its whole monthly guard
    When the sprint is proposed
    Then the item goes to the other analyst, and its Why says the first one
      was skipped for having no headroom left

  @test:TestASuspendedAnalystIsNeverChosenByRouting
  Scenario: A suspended analyst is never routed to, even when it is the only one with the skill
    Given a goal naming a skill that exists only on a suspended analyst
    When the sprint is proposed
    Then the item goes to the supervisor instead, because nobody active
      holds that skill

  @test:TestAnEmptyGoalAddsNothingFromTheGoalSource
  Scenario: Typing nothing into the goal field is allowed
    Given a sprint form with no goal typed
    When the sprint is proposed
    Then the five other sources still apply, and none of the plan's items
      says the goal named anything
