# language: en

Feature: A skill becomes a tool

  @decided 2026-09-02
  """
  The agents come ready with the skills a FinOps analyst needs in real life,
  and with an understanding of their own workspace and workflow.
  """

  @test:TestThePacketCarriesTheAnomalysFigures
  Scenario: An analyst is handed the figures its task is about
    Given a task that came from an anomaly
    When the prompt is built for the analyst assigned to it
    Then it carries the anomaly's own excess, service and day
    And an analyst with no figures-read is told so plainly instead of
      handed nothing

  @test:TestAnAllowedToolActuallyRuns
  Scenario: A skill is a tool it can call
    Given an analyst whose skill backs a right the catalogue maps a tool to
    When it calls that tool with a well-formed argument
    Then the dispatcher runs the tool and hands back its own answer

  @test:TestAToolTheAnalystHasNoRightForIsRefused
  Scenario: A right it does not hold is refused and the supervisor is told
    Given an analyst without sql-readonly
    When it calls charges_query
    Then the dispatcher refuses it by name, journals tool_refused with the
      right it needed, and the console prints a line

  @test:TestChargesQueryHostileInputsNeverTouchARow
  Scenario: A query cannot leave the charges
    Given every hostile statement B2-SPEC.md section 3.3 names, against a
      store carrying a canary row in analysts
    When each is sent to charges_query
    Then every one is refused by name, none of them touches the canary row,
      and none of them panics

  # No decision is recorded for this scenario.

  @test:TestAStakeholderBriefingAnalystCallingBudgetsSucceeds
  Scenario: A role's own reads promise is backed by a right it actually holds
    Given a partner analyst whose family's reads line promises the team's
      budgets
    When it calls the budgets tool
    Then the dispatcher runs it rather than refusing it

  @test:TestEveryFamilysReadsPromiseIsBackedByARight
  Scenario: Every family's reads line is backed by the rights its members hold
    Given every role family in roles.yaml, and the rights that gate a
      catalogue tool
    When the roster's own skills are turned into rights
    Then no family's job description promises a read none of its members can
      make
