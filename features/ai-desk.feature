# language: en

Feature: AI spend with an agent's name on it

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @test:TestAISpendSectionNamesTheAgentAndTheModel
  Scenario: The AI desk names the agent and the model
    Given a store carrying a month of real ai_calls imported from
      TokenFuse's own FOCUS export
    When the ai-spend analyst's packet is built
    Then it names the agent with the highest cost, grouped by agent, and
      the same month's calls grouped by model

  @test:TestAISpendSectionCountsBlockedCallsAsTheSaving
  Scenario: Blocked calls are the saving, shown as such
    Given a month with a call the guard blocked before it spent anything
    When the ai-spend analyst's packet is built
    Then the blocked call is counted, never costed, and named as the
      guard's saving rather than folded into the desk's spend

  @test:TestUnitEconAIPacketNamesCostPerOutcomeFromTheFixture
  Scenario: The unit-econ-ai analyst is handed cost per outcome
    Given the same month, with two agents that each tag one call with an
      outcome
    When the unit-econ-ai analyst's packet is built
    Then it names a cost per outcome for each of them

  @test:TestUnitEconAIPacketNamesAgentsWithCostAndNoOutcome
  Scenario: A cost with no outcome is said, not invented
    Given an agent that spent this month and tagged no call with an
      outcome
    When the unit-econ-ai analyst's packet is built
    Then it names that agent as carrying a cost with no outcome, rather
      than inventing a cost-per-outcome figure for it

  @test:TestAICallsQueryAnswersOverAICallsAndRefusesCharges
  Scenario: ai_calls_query answers only over ai_calls
    Given a SELECT naming ai_calls, and separately one naming charges or
      accounts
    When the ai-spend analyst calls ai_calls_query with each
    Then the one over ai_calls answers, and the others are refused by name

  @test:TestAgentAttributionKPIBecomesComputedAfterAnImport
  Scenario: agent-attribution reports on the AI desk once spend is real
    Given a store before anything real has been imported
    Then agent-attribution refuses
    But once the TokenFuse FOCUS fixture is imported
    Then agent-attribution reports a percentage and meets its own target

  @test:TestCostPerOutcomeRefusesWithACountWhenNoOutcomeExists
  Scenario: cost-per-outcome refuses with a count when nothing is tagged
    Given a month on the AI desk where every agent that spent tagged no
      call with an outcome
    When the KPI library is read
    Then cost-per-outcome refuses, naming how many of those agents set
      none

  @test:TestCostPerOutcomeReportsOnceAnyOutcomeExists
  Scenario: cost-per-outcome reports once at least one outcome is tagged
    Given a month on the AI desk where at least one call carries an
      outcome
    When the KPI library is read
    Then cost-per-outcome reports a figure
