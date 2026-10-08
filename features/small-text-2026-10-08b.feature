# language: en

Feature: Four sentences on the connector and reconciliation pages say what is true

  A read-only review of the console found four small sentences that promised
  or described something the console does not do: the reconciliation page did
  not say it matches a model by its exact name, two usage connectors that read
  a folder were listed as API calls, the gateway connector offered to take a
  dropped folder, and the gateway connector did not lead to the page that
  shows what it imported.

  # @test:TestTheReconciliationPageStatesTheExactModelNameLimit
  Scenario: The reconciliation page says a model is matched by its exact name
    Given the gateway records a model alias and the provider bills its dated id
    When a person reads the reconciliation page
    Then the page says the same calls show as one row over and one row under, and that the total still holds

  # @test:TestTheUsageConnectorsAreListedAsFoldersNotCalls
  Scenario: A usage connector that reads a folder is not listed as an API call
    When a person opens the list of connectors
    Then the two provider usage connectors are listed as folders the console reads

  # @test:TestTheFocusConnectorPageOffersNoDropAndLinksToTheAIPage
  Scenario: The gateway connector asks for a path and leads to the AI page
    When a person opens the TokenFuse connector's page
    Then it asks for a local path and does not offer to take a dropped folder
    And it links to the AI spend page that shows what was imported
