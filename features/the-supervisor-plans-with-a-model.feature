# language: en

Feature: The supervisor plans with a model, beside the deterministic plan

  @yurii 2026-09-02
  """
  він вже сам розподіляє це все між агентами... в залежності від моделі, від
  всього, задачі.
  """

  @yurii 2026-09-02
  """
  Вони мають вирішувати це все згідно своїх посадових інструкцій.
  """

  @test:TestAskPlanAcceptsAValidRerouteAndShowsBothPlans
  Scenario: The supervisor distributes the work itself
    Given a fake model answer that re-routes an item to a different active
      analyst holding that item's own skill, on its own desk
    When a person asks the supervisor to plan the sprint
    Then the re-routed item is accepted and shown beside the deterministic
      plan, which is still there, unchanged

  @test:TestAnItemWithNoRefIsRefusedWhole
  Scenario: It never invents work
    Given a fake model answer naming an item with no ref at all
    When the answer is validated against the deterministic plan
    Then the whole answer is refused, with the reason, and nothing from it
      is applied

  @test:TestAnUnroutableItemCannotBeReassigned
  Scenario: It never routes past a job description
    Given a deterministic item that was not routed by any skill (a blocked
      task returning to the analyst who owns it)
    When the model's answer reassigns it to somebody else
    Then the reassignment is refused: there is no job-description-backed
      pool to check an alternative against

  @test:TestARouteToASuspendedAnalystIsRefused
  Scenario: It never routes to anybody inactive
    Given a model answer that routes an item to a suspended analyst who
      does hold the item's own skill
    When the answer is validated
    Then it is refused, naming the suspended analyst

  @test:TestApproveModelPlanCreatesATaskWithTheModelsOwnBudget
  Scenario: A person still approves
    Given the supervisor's own plan, shown beside the deterministic one
    When the person approves the supervisor's plan through the ordinary
      approve form
    Then the board carries exactly the items the model named, through the
      SAME crew.Approve the deterministic plan already used, and the other
      plan can no longer be approved for the same sprint

  @test:TestAskPlanRefusesBeforeAnyCallWhenWorstExceedsPerTask
  Scenario: The call is priced before it is made, and refused if it is too much
    Given the supervisor's own per-task guard lowered below any plausible
      worst case
    When a person asks the supervisor to plan
    Then the call is refused before the gateway is ever reached, and the
      page says why

  @test:TestAskPlanRefusesWithNoGatewayConfigured
  Scenario: No gateway, no call
    Given a console with no TokenFuse gateway configured
    When a person asks the supervisor to plan
    Then the ask is refused with one sentence, nothing is called, and
      nothing is settled

  @test:TestSettlePlanAskLandsInSpendInMonthForSupervisor
  Scenario: The actual cost is the supervisor's own spend
    Given a settled planning call for the supervisor
    When the same month's spend is read back
    Then the supervisor's own figure carries that cost, the same figure
      routing and headroom checks already read

  @test:TestDueNeverCallsThePlanner
  Scenario: A clock-driven run never asks the model to plan
    Given the cadence runner's own source
    When it is checked for this step's own model-planning symbols
    Then none of them appear anywhere in it: -due stays deterministic by
      construction

  @test:TestZeroItemsIsALegalAnswer
  Scenario: Nothing this sprint is a legal answer
    Given a model answer naming zero items
    When it is validated
    Then it is accepted, and shown as "nothing this sprint" rather than
      refused

  @test:TestAskPlanRendersAScriptTagAsText
  Scenario: A hostile why is text, never markup
    Given a model answer whose why carries a script tag
    When the plan page renders it
    Then the tag appears as escaped text, never as executable markup
