# language: en

Feature: A skill becomes a tool

  @yurii 2026-09-02
  """
  щоб вони були вже готові з відповідними навичками, які потребують FinOps
  Analyst в реальному житті. І з відповідними розуміннями свого якогось
  робочого простору, робочого флоу.
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
