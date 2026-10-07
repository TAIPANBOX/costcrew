# language: en

Feature: Money is found, never saved, and a drop is not money found

  # Paraphrased from the finding that opened this scenario (costcrew,
  # 2026-10-07): the Results page counts only a finding's signed excess as
  # money found, so a spend that fell is a finding worth having and not money
  # anybody recovered. The crew-cost KPI kept its own copy of the sum, with
  # the absolute value, and counted the drop.

  @test:TestTheCrewCostKPIDoesNotCountADropAsMoneyFound
  @test:TestResultsAndTheCrewCostKPIAgreeOnMoneyFound
  Scenario: The crew-cost KPI and the Results page state one figure
    Given an accepted finding whose spend rose and another whose spend fell
    When the crew-cost KPI and the Results page are read
    Then both state the signed sum as money found, the drop does not raise it,
      and the KPI does not meet its "less than it finds" target on the strength
      of the drop
