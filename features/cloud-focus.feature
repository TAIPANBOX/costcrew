# language: en

Feature: A cloud provider's own bill reaches the crew

  Until these readers existed no cloud bill reached the crew: the connector
  catalogue listed the AWS Data Exports folder and the GCP BigQuery export as
  documented, not built, and the only FOCUS reader required the gateway's own
  columns and refused a plain cloud file (costcrew#68). The ask is a reader
  for the plain FOCUS file a cloud provider writes itself, as CSV or CSV.gz in
  a folder, with no S3 client and no BigQuery client in this binary.

  The scenarios are the behaviour of that ask, in plain words. Which FOCUS
  versions are read, and what was and was not measured against a real export,
  is stated in the header of internal/connectors/cloudfocus.go.

  @test:TestAWSDataExportsFocusIsRead
  Scenario: An AWS Data Exports FOCUS 1.2 file in a folder becomes the aws desk's real charges
    Given a CSV in the AWS Data Exports FOCUS 1.2 shape, in a folder, with
      usage, a Savings Plan fee, tax, a credit and a correction to the
      previous invoice
    When the aws-data-exports reader imports the folder
    Then every row lands in charges under the aws desk, marked with the
      connector as its provenance so none of it reads as generated, with
      each amount summed in exact micro-dollars and rounded to cents once

  @test:TestAFocus10FileIsReadAsWell
  Scenario: A FOCUS 1.0 file from AWS is read too, and a month's tax row is kept
    Given a CSV in the shape of a real AWS FOCUS 1.0 export, with timestamps
      in milliseconds, amounts to ten decimals, no invoice column, and the
      month's tax as one row for the whole billing period
    When the aws-data-exports reader imports the folder
    Then usage, credits and tax are three separate lines, the credits are
      not netted away, and the tax row lands whole on the day its period
      starts and is counted in the sentence rather than refused

  @test:TestGCPBillingExportFocusIsRead
  Scenario: A CSV flattened from the GCP BigQuery FOCUS view becomes the gcp desk's real charges
    Given a CSV exported from Google's BigQuery FOCUS view, whose tags are a
      list of key and value records
    When the gcp-billing-export reader imports the folder
    Then every row lands in charges under the gcp desk with its provenance
      set, and the team is read from the tag list

  @test:TestEveryChargeCategoryLandsAndAPurchaseIsNeverUsage
  Scenario: A purchase, a tax, a credit and an adjustment are never counted as usage
    Given one row of each FOCUS charge category, including a purchase far
      larger than the usage
    When the folder is imported
    Then each lands under its own category and the usage total contains only
      the usage row

  @test:TestANegativeBilledCostIsKeptNotRefused
  Scenario: A credit or a refund is kept with its sign
    Given rows whose BilledCost is negative, which FOCUS allows
    When the folder is imported
    Then they are kept, rounded the same way as every other amount, and the
      sentence says how many negative rows there were

  @test:TestTheTeamComesFromAConfigurableTagKey
  Scenario: The team comes from the tag the connector is told to read
    Given rows with team tags under different keys and some with none
    When the folder is imported with the team tag key set
    Then each row takes its team from the first of the keys that has a text
      value, a row with none is shared cost, and the sentence counts both

  @test:TestARevisedFileReplacesItsOwnEarlierVersion
  Scenario: A period AWS revises is not counted twice
    Given an export file that was imported, then overwritten in place with
      corrected content
    When the folder is imported again
    Then only the corrected rows remain, and a day the new file no longer
      has disappears

  @test:TestCloudFocusRefusesToMixWithTheGeneratedEstate
  Scenario: Real cloud rows are not mixed into the generated estate
    Given a store that still holds the generated estate
    When a cloud export is imported without the replace-generated yes
    Then it is refused and nothing is written, and with the yes the generated
      rows go, the real ones arrive and the replacement is journaled

  @test:TestCloudFocusTestWritesNothing
  Scenario: Test says what Import would do and writes nothing
    Given a configured folder
    When the connector is tested
    Then the sentence names the files, days, rows, sub accounts and total
      that Import would report, and no row is written

  @test:TestCloudFocusHostileInput
  Scenario: A bad row or a bad file is refused by name and the rest is read
    Given files with a missing column, the wrong currency, a bad timestamp,
      ragged rows, a truncated gzip, an unterminated quote, another
      provider's rows and an empty folder, beside a good file
    When the folder is imported
    Then each is refused by name, a refused file contributes nothing, and the
      good file is imported

  @test:TestAGzipBombIsRefusedByName
  Scenario: A small gzip that inflates past the limit is refused
    Given a gzip of a few kilobytes that inflates to many megabytes
    When the folder is imported with a smaller unpacked-size limit
    Then that file is named as refused for its unpacked size and the setting
      that changes the limit, and none of its rows are kept

  @test:TestACloudFocusFileOverAHundredMegabytesStaysBounded
  Scenario: A file of over a hundred megabytes is streamed, not held
    Given a CSV of more than 100 MB
    When the folder is imported
    Then every row is read and the live heap does not grow with the file

  @test:TestAnAWSExportReachesTheConsoleThroughTheConnectorPage
  Scenario: An operator can do all of this from the connector page
    Given the aws-data-exports connector page
    When an operator saves a folder, tests it, and imports with the
      replace-generated box
    Then the page offers that box and the optional settings, and the aws desk
      shows the export's own services
