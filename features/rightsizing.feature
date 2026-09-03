# language: en

Feature: Rightsizing and idle, from the providers' own recommendations

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @yurii 2026-09-02
  """
  змінювати інфраструктуру ... він сам особі не може
  """

  @test:TestAWSRightsizingIsRead
  Scenario: AWS Cost Explorer's own rightsizing recommendations arrive as a file
    Given a CSV Cost Explorer's Rightsizing Recommendations report already
      exported to a folder, with an instance's current type, its recommended
      type, the estimated monthly saving and the lookback the report was run
      with
    When the aws-rightsizing reader imports the folder
    Then every row lands in the recommendations table under the aws desk,
      with the resource, the action, the current and recommended size, the
      saving and the lookback all carried across

  @test:TestGCPRecommenderIsRead
  Scenario: GCP's own Recommender export arrives as a file
    Given a CSV export of Recommender's machine-type and idle-VM
      recommendations, with the project, the resource, the recommender's own
      subtype, the current and recommended machine type and the monthly
      saving
    When the gcp-recommender reader imports the folder
    Then every row lands in the recommendations table under the gcp desk

  @test:TestAzureAdvisorIsRead
  Scenario: Azure Advisor's own cost recommendations arrive as a file
    Given a CSV export of Advisor's cost recommendations, which reports a
      saving as POTENTIAL ANNUAL cost rather than monthly
    When the azure-advisor reader imports the folder
    Then every row lands in the recommendations table under the azure desk,
      its saving divided by twelve and rounded to the same monthly cents
      every other row in the table carries

  @test:TestHostileRightsizingInput
  Scenario: A file missing a column a reader needs is refused, by name
    Given a CSV export with one of its required columns missing
    When a reader imports the folder it is in
    Then the import is not a hard error, and the file is named as refused
      along with the specific column it expected and did not find

  @test:TestBuiltMeansAReaderExists
  Scenario: A rightsizing reader earns Built only by existing
    Given the three new catalogue entries, aws-rightsizing, gcp-recommender
      and azure-advisor
    When the catalogue's own Status is derived
    Then each is Built because, and only because, a reader is actually
      registered for it in the readers map, the same rule invariant 22
      already holds for every connector in this console

  @test:TestRecommendationsSectionRanksBySavingFromAFixtureImport
  Scenario: The optimizer's packet ranks the list by saving
    Given several imported recommendations on one desk, with different
      monthly savings
    When the optimizer's own packet section is built for that desk
    Then the row with the largest saving leads the list, not the row that
      happened to import first and not the row with the largest current size

  @test:TestRecommendationsSectionFlagsShortLookbackNotLong
  Scenario: The optimizer's packet names the risk a short lookback carries
    Given one recommendation with a fourteen-day lookback and another with a
      ninety-day one
    When the optimizer's own packet section is built
    Then the fourteen-day row carries the sentence that a monthly job looks
      idle to a window that short, and the ninety-day row does not

  @test:TestTheRightsizingPageStartsWithNoneImported
  Scenario: A desk nothing has been imported for says so
    Given a desk no rightsizing reader has ever been pointed at
    When the rightsizing page is opened
    Then that desk's own section says "none imported", not an empty table
      with no explanation

  @test:TestARoleCannotDecideAClassItDoesNotOwn
  Scenario: The change is a person's, never this console's
    Given infra.change, the class every row on the optimizer's own list
      ends in
    When any link in the crew, the optimizer included and the owner too, is
      asked whether it may decide infra.change
    Then it may not: nobody in the crew owns it, so this console can never
      apply a resize, a stop or a move on its own, only hand the list up
      for a person to act on
