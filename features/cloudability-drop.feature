# language: en

Feature: Agent spend from the gateway reaches Cloudability

  # The ask, paraphrased: the agent spend the gateway records shows up inside
  # Apptio Cloudability, grouped by agent and by business unit, as a
  # month-to-date FOCUS drop that Cloudability's Custom Data FOCUS ingress
  # accepts. Before this, nothing connected the two in either direction.
  #
  # costcrew-cloudability reads the folder the tokenfuse-focus connector reads,
  # with that connector's own reader, and writes the drop into a local folder
  # laid out the way the ingress reads a bucket. Copying that folder to the
  # bucket is the operator's step, and the tool makes no network call.

  # @test:TestADropCarriesEachAgentsSpendByUnitTaggedForGrouping
  Scenario: Each agent's spend arrives by business unit, tagged so Cloudability can group by it
    Given a month of gateway exports with two agents in two business units on two models
    When the drop for that month is made
    Then each day's spend for one agent, unit, model and credential is one FOCUS row
    And its Tags name the agent, the unit, the model and the credential
    And the drop's total is the gateway's total to the micro-dollar

  # @test:TestADropHoldsTheWholeMonthToDate
  Scenario: A drop holds the whole month so far, not only the newest export
    Given exports in several files covering the first days of the month, and calls from the months either side
    When the drop for this month is made
    Then every day from the first to the latest is in it
    And the calls from the other months are left out and counted

  # @test:TestASecondDropSupersedesAndNeverAppends
  # @test:TestADropIsTheSameBytesEveryTime
  Scenario: A later drop replaces the month, it never adds to it
    Given a drop for this month already in the folder
    When the drop is made again later from the same exports
    Then a new epoch folder holds the whole month again, the data byte for byte the same
    And the earlier folder is left as it was
    And a drop is never written into a folder that already exists, nor behind a later one

  # @test:TestTheManifestIsWhatCloudabilityReads
  # @test:TestTheReportPeriodRunsMonthToMonthAcrossYearsAndLeapFebruary
  Scenario: The folder and the manifest are laid out the way Cloudability reads them
    When a drop is made
    Then it sits under the vendor, the report period and the epoch, a prefix-Manifest.json beside a gzipped CSV
    And the manifest says FOCUS 1.1, CSV, GZIP and the bucket's name
    And its report keys name every file written, and none of them carries the bucket's name
    And December's period runs into January and a leap February keeps its 29th

  # @test:TestEveryMandatoryFocus11ColumnIsFilledHonestly
  # @test:TestTheDropValidatorRefusesEachViolation
  Scenario: Every column FOCUS 1.1 makes mandatory is there, and none is invented
    When a drop is made
    Then every mandatory FOCUS 1.1 column is present
    And a column is empty only where the specification allows a null
    And every value a column restricts is one the specification allows
    And a drop that breaks any of that is refused before it is written

  # @test:TestManySubCentCallsAreSummedExactly
  Scenario: Many calls worth less than a cent add up to what they cost
    Given ten thousand calls at a third of a cent each, and ten at $0.0035
    When the drop is made
    Then each row's cost is the exact sum of its calls, to the micro-dollar, never rounded per call

  # @test:TestACallExportedTwiceIsInTheDropOnce
  Scenario: A call the gateway exported twice is in the drop once
    Given the same calls exported before and after the gateway's upgrade, in one folder
    When the drop is made
    Then each call's money is counted once, and the drop is the one the newer export alone makes

  # @test:TestAnOlderExportWithoutTheNewColumnsStillMakesADrop
  Scenario: An export from before the gateway named credentials still makes a drop
    Given an export without x_key_id, x_block_reason and x_identity_reason
    When the drop is made
    Then its calls are in the drop, with no credential named

  # @test:TestBlockedCallsAreCountedNotCharged
  Scenario: A call the gateway blocked is counted, never charged
    Given calls the gateway blocked beside calls it let through
    When the drop is made
    Then the blocked calls cost nothing and are counted on their group's row
    And a group of only blocked calls has no row, and the tool says how many calls that was

  # @test:TestHostileInputMakesNoDrop
  # @test:TestAnAmountTooLargeToHoldIsRefusedNotWrapped
  # @test:TestFlagsThatWouldWriteOutsideTheFolderAreRefused
  Scenario: Input the tool cannot vouch for makes no drop at all
    Given an export that is malformed, in another currency, past what 64 bits hold, or carries a cell a spreadsheet would run
    And flags that would write outside the drop folder
    When the drop is made
    Then no drop folder is written, and the tool says why
