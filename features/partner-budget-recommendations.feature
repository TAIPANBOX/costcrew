# language: en

Feature: A provider's own budget recommendation, cited beside the team's real one

  @yurii 2026-09-03
  """
  це можна отримувати від користувача, або, наприклад, подивитись, які
  пропозиції дають провайдери хмарні
  """

  @yurii 2026-09-03
  """
  Так, звісно, роби все, про що ми говоримо, треба протестувати і зробити
  як варіант використання.
  """

  @test:TestAWSBudgetsRecommendedIsRead
  Scenario: A provider's own recommendation CSV is imported
    Given a CSV export of AWS Budgets' own recommended threshold, one row
      per team and month
    When it is imported through the aws-budgets-recommended connector
    Then budget_recommendations holds one row per team and month, and the
      import summary says how many rows it read

  @test:TestPartnerBudgetSectionCitesBothFiguresWithTheGap
  Scenario: The provider's own suggestion sits beside the team's real budget, never in place of it
    Given a team's real budget for a month, set by finance, and a provider's
      own recommendation for the same team and month
    When a finops-partner's task packet is built for that desk
    Then the packet carries both figures, the gap between them in cents and
      as a percentage, and a sentence naming which figure is which, ending
      in "not applied anywhere"

  @test:TestPartnerBudgetSectionAbsentWhenOnlyTheRealBudgetExists
  @test:TestPartnerBudgetSectionAbsentWhenOnlyTheRecommendationExists
  Scenario: The section says nothing when only one side exists
    Given either a real budget with no provider recommendation for that
      team and month, or a provider recommendation with no real budget for
      it
    When a finops-partner's task packet is built for that desk
    Then the packet carries no partner-budget section at all, never a
      figure shown beside an invented zero

  @test:TestPartnerBudgetSectionIgnoresARecommendationForAMonthWithNoRealBudgetRow
  Scenario: The gap is named, never invented from nothing
    Given a team with a real budget for September and a provider
      recommendation for both September and October, where only September
      has a real budget to pair it with
    When a finops-partner's task packet is built for that desk
    Then September's line names both figures and their gap, and October's
      recommendation is left out rather than shown against a budget that
      does not exist

  @test:TestEndToEndAnImportedRecommendationReachesAPostedDeliverable
  Scenario: The whole path, from an imported CSV to a posted brief that names the gap
    Given a provider's recommendation CSV, not yet imported, and a team's
      real budget already on file for the same month
    When the CSV is imported, a finops-partner's task packet is built, and
      the analyst's brief is posted
    Then the posted brief names both figures and the gap between them, and
      never states the provider's recommendation as the team's own budget

  @test:TestCurrentBudgetsAndSpendInMonthSourceNeverMentionsBudgetRecommendations
  @test:TestImportingABudgetRecommendationNeverChangesRealBudgetComputations
  Scenario: A provider's suggestion never becomes the console's own budget figure
    Given a team's real budget, its spend for the month, and a provider's
      recommendation for the same team and month, all on file together
    When the recommendation is imported and every budget and guard
      computation this console has is read again
    Then BudgetVsActual, CurrentBudgets and SpendInMonth are unchanged, byte
      for byte, and neither CurrentBudgets nor SpendInMonth's own source
      ever comes to mention budget_recommendations
