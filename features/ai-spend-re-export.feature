# language: en

Feature: Exporting the same gateway calls again never counts them twice

  The gateway's FOCUS export is read from a folder that keeps every file. When
  the gateway is upgraded and the same trace is exported again, the new file
  carries the same calls under a new file name, and the console must keep each
  call once. From TokenFuse 1.7.0 a call refused for identity is filed under
  the credential that made it, not under the agent it claimed, so the newer
  copy is the one kept, whichever file the folder lists first.

  # @test:TestAReExportOfTheSameCallsReplacesTheEarlierRows
  Scenario: Re-exporting a trace with the upgraded gateway leaves the totals where they were
    Given a folder holding an export written before TokenFuse 1.7.0
    And the console has imported it
    When the same trace is exported again by TokenFuse 1.7.0 into the same folder
    And the folder is imported again
    Then the number of calls, the money and the daily charges are what they were
    And the agent whose identity was claimed no longer carries the refused call
    And the refused call is filed under the credential, with its key and its reason

  # @test:TestAnOlderExportNeverDisplacesANewerOne
  Scenario: The order of the files in the folder does not decide which copy is kept
    Given a folder in which the 1.7.0 export sorts before the older one
    When the folder is imported, twice
    Then each call is kept once, from the 1.7.0 export

  # @test:TestTwoIdenticalCallsInOneExportStayTwo
  Scenario: Two calls that look alike in one export are still two calls
    Given one export holding two calls with the same run, instant, model, tokens and amount
    When it is imported, and a second export of the same two calls after it
    Then both calls are kept

  # @test:TestTheKeyAndTheBlockReasonAreKeptWhenTheExportCarriesThem
  # @test:TestHostileKeyAndBlockReasonAreRefusedByName
  Scenario: The credential and the reason for a block are kept when the export carries them
    Given an export from TokenFuse 1.7.0
    When it is imported
    Then every call keeps the credential it was made with and, if blocked, why
    And a credential or a reason that is not plain text is refused by name

  # @test:TestAGatewayRefusalWithClientKeysOffIsNamedAsSuchNotAsBadData
  Scenario: A refusal with no credential to file it under is called a refusal, not bad data
    Given the gateway ran with client keys off and refused a call for identity
    When its export is imported
    Then the import says the gateway refused that call and there is nobody to file it under
    And it does not list the call as a broken row

  # @test:TestTheConnectorPageShowsWhatTheLastImportRead
  Scenario: The person who imports sees what the import read
    Given a folder of gateway exports is saved on the connector's page
    When the person imports it
    Then the connector's page shows the import's own account of what it read, refused and counted once
