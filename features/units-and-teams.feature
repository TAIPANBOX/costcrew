# language: en

Feature: A customer unit, a team and a period are each named for what they are

  The AI gateway reports spend by customer unit. Once a person stamps a rule,
  a unit is charged back under a business unit, and its page should say which
  one. The teams page should list the units beside the teams and call a
  team's own unit its business unit, and the chargeback page should say its
  units' figures are for the month being looked at, not for today.

  # @test:TestAUnitsPageNamesTheBusinessUnitItsRuleChargesItUnder
  Scenario: A unit's page names the business unit a stamped rule charges it under
    Given a customer unit the gateway reports, with a rule a person stamped
    When a person opens that unit's page
    Then it names the business unit, who stamped the rule and when
    And a unit with no rule says no rule covers it yet

  # @test:TestTheTeamsPageListsTheCustomerUnitsAndNamesBusinessUnits
  Scenario: The teams page lists the customer units and calls a team's unit its business unit
    Given customer units with spend in the month
    When a person opens the teams page
    Then the units are listed, each linked to its page, with its business unit or no rule yet
    And the page points at the same list on the chargeback page
    And the team table's column is called business unit

  # @test:TestTheChargebackUnitsPanelNamesItsPeriodNotToday
  Scenario: The chargeback page says which month its units' figures are
    When a person opens the chargeback page for a month
    Then the customer units panel names that month as live figures
    And nothing in it says the figures are today's
