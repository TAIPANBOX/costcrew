# language: en

Feature: Azure's own bill reaches the crew

  @decided 2026-11-23: the FinOps console takes real data first, the
  gateway's spend and Azure's billing export, before a reserved instance or
  savings plan adviser is started. Until this reader, Azure was the one cloud
  in the catalogue whose bill could not reach the board: the AWS and Google
  FOCUS exports were read, and the Azure Cost Management FOCUS export was
  listed as documented, not built.

  What Microsoft documents about the export, and what this reader assumes
  where it does not, is stated in the header of
  internal/connectors/azurefocus.go.

  @test:TestAzureCostManagementFocusIsRead
  Scenario: An Azure Cost Management FOCUS export becomes the azure desk's real charges
    Given a CSV in the shape Microsoft documents for the FOCUS export, with
      usage, usage a reservation covered, the reservation's purchase and a
      credit
    When the azure-focus reader imports the folder it was synced to
    Then every billed row lands in charges under the azure desk, marked as
      real, summed in exact micro-dollars and rounded to cents once, the
      purchase on its own line and the credit kept negative, and the covered
      usage, which bills nothing, writes no charge

  @test:TestAnAzureFocus10FileIsReadWithMinuteTimestampsAndXInvoiceId
  Scenario: An export from the older FOCUS 1.0 dataset is read as well
    Given an export whose timestamps carry minutes and no seconds and whose
      invoice is in x_InvoiceId, as Azure's FOCUS 1.0 dataset writes them
    When the azure-focus reader imports it
    Then the rows land on their days with their invoice

  @test:TestTheMinuteTimestampIsAzuresAloneAndTheAWSReaderStaysStrict
  Scenario: Accepting Azure's timestamp does not loosen another cloud's reader
    Given an AWS file whose timestamps carry no seconds
    When the AWS reader imports it
    Then the row is refused by name, as before

  @test:TestTheAzureReaderIsBuiltAndAsksForAFolder
  Scenario: The catalogue says the Azure reader is built and asks for the folder
    Given the connector catalogue
    When a person opens the Azure Cost Management entry
    Then it is marked built and asks for the folder the export was synced to,
      not for a blob container this console never opens

  @test:TestOnlyTheNewestExportRunOfAPeriodIsCounted
  Scenario: Several runs of a month-to-date export count the month once
    Given two runs of the same month's export, each holding the month so far
    When the folder is imported
    Then only the run its manifest says is newest is read, and the older run
      is named as superseded rather than added

  @test:TestARunReplacedOnDiskReplacesItsRowsInTheStore
  Scenario: A new run replaces what an earlier run put on the board
    Given yesterday's run was imported, and today the folder holds only
      today's run under a new run id
    When the folder is imported again
    Then the board holds today's run alone, and a day only yesterday's run
      had is gone from it

  @test:TestTwoRunsWithoutAManifestAreRefusedNotGuessed
  Scenario: Runs that cannot be ordered are refused, not guessed between
    Given two runs of one month, one without its manifest
    When the folder is imported
    Then nothing of that month is read, and the refusal names the month and
      the missing manifest

  @test:TestTwoRunsSubmittedAtTheSameInstantAreRefused
  Scenario: Two runs submitted at one instant are refused
    Given two runs whose manifests give the same submission time
    When the folder is imported
    Then nothing of that month is read, and the refusal says why

  @test:TestARunMissingAPartItsManifestListsIsRefusedAndTheEarlierRunStands
  Scenario: A run still being synced does not replace the run before it
    Given a newer run whose manifest lists a part the folder does not hold
    When the folder is imported
    Then the newer run is not read, the earlier run stays on the board, and
      the refusal names the missing part

  @test:TestARunWithARefusedPartIsRolledBackWhole
  Scenario: A run lands whole or not at all
    Given a newer run with one good part and one broken part
    When the folder is imported
    Then none of the newer run lands, and the earlier run stays on the board

  @test:TestHostileManifestsAreRefusedByName
  Scenario: A manifest that cannot be trusted refuses its month by name
    Given a manifest that is not JSON, has no submission time, is over a
      megabyte, names a file outside its folder, or is a link
    When the folder is imported
    Then nothing of that month is read, and the refusal names why

  @test:TestRunsOfDifferentPeriodsAreIndependent
  Scenario: Each month's runs are judged on their own
    Given one run of September and one run of October
    When the folder is imported
    Then both months are read
