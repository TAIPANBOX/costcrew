# language: en

Feature: A link leads somewhere, and a tool says what it does

  # @claude 2026-10-07
  # Four small defects, each something a person or a neighbouring service
  # trips over: a unit's link that answered 404, an agent with no rights
  # written as "tools": null, an identity source closed with no lock, and a
  # parity tool whose usage named a flag it does not have and whose scrub
  # missed the Go console's own audit hash.

  @test:TestEveryTeamLinkOnTheMoneyPagesLeadsToAPage
  Scenario: a unit linked from the chargeback and allocation pages has a page
    Given a FOCUS import charged spend to a unit that is not on the roster
    When a person follows the unit's link on the chargeback or allocation page
    Then the page answers, shows the unit's spend, and says it is not on the
      roster rather than inventing a business unit for it

  @test:TestATeamNobodyChargedIsStillNotFound
  Scenario: a name nobody charged is still not found
    Given a name that is neither a roster team nor a unit any charge carries
    When its team page is requested
    Then the console answers that there is no such team

  @test:TestAnAgentWithNoRightsHasAnEmptyToolsList
  Scenario: an agent with no rights reaches nothing, written as an empty list
    Given an agent on the roster holding no rights
    When the roster is written as idryx's agents source
    Then its tools are an empty list, not null

  @test:TestAnEntryCarriesTheRightsTheAgentHolds
  Scenario: an agent's entry carries the rights it holds
    Given an agent with rights, a parent and a hire date
    When its idryx entry is written
    Then the entry names those rights, its parent and the date, in its own list

  @test:TestCloseDecidesUnderTheLockAndClosesOnce
  Scenario: closing the console's identity source is decided under its lock
    Given the console holds a SPIFFE identity
    When it is closed from several places at once
    Then the workload source is closed once, with the lock held

  @test:TestIdentityAfterCloseKeepsTheLastIdentityAndAsksNothing
  Scenario: a closed identity source answers with what it last held
    Given the identity source has been closed
    When a page asks which identity the console holds
    Then the answer is the last identity, and the closed source is not asked

  @test:TestCloseAndIdentityDoNotRace
  Scenario: closing and reading the identity at once is not a data race
    Given the race detector is on
    When the identity is read while the source is being closed
    Then the detector reports nothing

  @test:TestTheUsageNamesEveryFlagEachSubcommandDefines
  Scenario: the parity tool's usage names the flags it actually has
    Given each parity subcommand's own flags
    When its usage is printed
    Then every flag it defines is named on its line, and no line names a flag
      that does not exist

  @test:TestTheJournalHashIsScrubbedInTheMarkupTheGoConsoleRenders
  Scenario: two fresh installs do not differ on the audit page's chain hash
    Given two installs of the same binary, each with its own journal
    When their audit pages are normalised for comparison
    Then the chain hash in the Go console's own markup is scrubbed, and a
      code cell that is not a hash is left alone
