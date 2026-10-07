# language: en

Feature: What a download carries is data, never markup and never a formula

  # @claude 2026-10-07
  # The ask, paraphrased: values from imported files reach files a person
  # opens. A Markdown packet is opened in a viewer that renders HTML, and a
  # CSV in a spreadsheet that evaluates formulas; a pipe or a newline in a
  # service name broke the packet's table, a script tag in one ran, a cell
  # starting with = ran, and a file name built by concatenation took any
  # parameter a period or a desk carried. x_unit, the field these names
  # most often arrive in, was only trimmed. Invariant 78.

  @test:TestTheExecPacketKeepsAnImportedValueAsText
  Scenario: an imported service name stays text in the executive packet
    Given an open anomaly whose service name carries a pipe, a newline, a
      script tag and a Markdown link
    When the executive packet is downloaded
    Then the anomaly is one table row with its five column breaks
    And the script tag and the link arrive as text, still readable

  @test:TestMDTextKeepsAValueReadableAndInert
  Scenario: Markdown escaping keeps every value readable and inert
    Given a value with HTML, Markdown punctuation, line breaks and invisible
      characters in it
    When it is written into a Markdown download
    Then the HTML is written as entities, the punctuation is escaped, a line
      break is a space, and an invisible character is dropped
    And letters in any script are kept as they were

  @test:TestADownloadsFileNameIsOneParameterWhateverItCarries
  Scenario: a download's file name is one parameter whatever it carries
    Given a period whose name carries a quote and a semicolon, and a desk in
      the link that tries to add a second file name
    When the packet, the results, the allocation and the budget are downloaded
    Then each response names exactly one file, the one it was asked for

  @test:TestEveryDownloadIsNamedThroughOneHelper
  Scenario: every download is named in one place
    Given the console's own source
    When it is searched for the header that names a download
    Then the header is set in one function and nowhere else

  @test:TestACSVExportNeutralisesAFormulaAndKeepsANegativeNumber
  Scenario: a CSV cell that starts like a formula is neutralised and a negative amount is kept
    Given anomalies and a unit whose names start with = + - @ and a tab
    When the findings and the allocation are downloaded as CSV
    Then every text cell that started a formula starts with an apostrophe,
      its value otherwise whole
    And a negative amount in a number column is still a plain negative number

  @test:TestCSVCellNeutralisesByColumnKind
  Scenario: the column decides what a leading minus means
    Given a cell starting with a minus
    When it is in a text column
    Then it is neutralised, because a spreadsheet evaluates -2+3
    When it is a plain number in a number column
    Then it is left alone

  @test:TestXUnitIsRefusedUnlessItIsAPlainBoundedName
  Scenario: a FOCUS row whose unit is not a plain, bounded name is refused by name
    Given a FOCUS file whose x_unit is too long, carries a control, format or
      separator character, is not text, or starts like a formula
    When it is imported
    Then that row is refused with a reason naming x_unit, and nothing is written
    And a unit exactly at the bound, an empty one and one in any script are kept

  @test:TestRefusalsAreCountedWholeButNamedOnlyForTheFirstFew
  Scenario: refused rows are counted whole and named for the first few
    Given a file with a thousand rows the reader refuses
    When it is imported
    Then the sentence counts all thousand, names the first twenty, and says
      how many more there were

  @test:TestALinkInTheFolderIsNotFollowedAndIsNamed
  Scenario: a link in the import folder is not followed, and is named
    Given the import folder holds a symbolic link to a file elsewhere
    When it is imported
    Then the link's target is not read
    And the sentence names the link it passed over
