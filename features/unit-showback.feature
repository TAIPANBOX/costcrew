# language: en

Feature: The box's AI spend charges back to the units the gateway already knows

  # The ask (costcrew#74), paraphrased: the AI gateway labels every call with
  # the customer unit that made it, and the FOCUS reader writes that label as
  # the charge's team, so spend arrives already named "aws", "gcp" or
  # "finops". Nothing then allocated by it, and the showback export listed
  # only the generated estate's teams. A FinOps team's first question about
  # agent spend is which team it belongs to. The ask: a rule keyed on that
  # unit, an analyst's option to propose it, a person's stamp to apply it, so
  # the chargeback and the showback carry one row per customer unit beside the
  # fixture's teams, and the period still balances to the cent.

  @test:TestTheShowbackCarriesOneRowPerUnitOnlyAfterTheOwnersStamp
  @test:TestAfterTheStampsTheShowbackHasOneRowPerUnitAndBalancesToTheCent
  Scenario: A stamped unit rule puts one showback row per unit, and it balances
    Given a console holding a FOCUS import with two customer units, aws and gcp
    And an allocation.rule option for each, carried to the owner
    When the owner stamps both and the period is closed
    Then the showback export has one row for aws and one for gcp, each under
      the business unit its rule names, and the rows add up to the cent to
      what was imported

  @test:TestBeforeAnyRuleTheUnitsAreOneVisibleUnruledRowAndTheFileBalances
  @test:TestOnlyTheRuledUnitGetsItsOwnRowAndTheRestStaysVisibleInOne
  Scenario: A unit nobody has ruled on is still in the file, as one line
    Given the same import and no rule at all, or a rule for aws alone
    When the showback is read
    Then the spend of every unit without a rule is one "(unruled units)" row,
      no unit is dropped, and the file still adds up to the import

  @test:TestUnitRowsSitBesideTheFixturesTeamsWithoutMovingThem
  Scenario: Units sit beside the fixture's teams and move none of them
    Given the generated estate and two customer units with a rule each
    When the showback is read
    Then the fixture's teams come first in their usual order, the units follow
      by name, and the whole of it plus the unallocated remainder is the bill

  @test:TestAUnitRuleIsAppliedByAStampAndRecordsWhoStampedIt
  @test:TestAStampOnADifferentBusinessUnitReplacesTheRuleRatherThanAddingASecond
  Scenario: A rule is the stamp of a person and is recorded as theirs
    Given an allocation.rule option naming the unit aws and a business unit
    When the owner applies it
    Then one rule exists for aws, under that business unit, naming who
      stamped it and the option it came from, and a later stamp replaces it

  @test:TestAUnitRuleForAUnitWithNoRowsIsRefusedAndTheOptionStaysOpen
  @test:TestAProposalForAUnitWithNoRowsIsRefusedWhenItIsWritten
  @test:TestAUnitRuleOnRowsTheTokenFuseReaderDidNotWriteIsRefused
  Scenario: A rule for a unit that has no rows is refused
    Given an allocation.rule option naming a unit no TokenFuse row carries
    When the analyst writes it, or somebody tries to stamp it
    Then it is refused with the unit's name, nothing is written, and an option
      that was refused at the stamp is not marked applied

  @test:TestAUnitRuleForARosterTeamIsRefused
  Scenario: A unit cannot take a roster team's name
    Given a unit whose name is one of the ten roster teams
    When a rule for it is stamped
    Then it is refused, because the showback already gives that name to the
      roster team's own row

  @test:TestTheSupervisorNeverAppliesAUnitRule
  @test:TestSavingAUnitRuleProposalWritesNoRule
  @test:TestOnlyTheOwnerOrAnAdminCanStampAUnitRule
  Scenario: A unit rule cannot be applied without a stamp
    Given a unit rule proposed by an analyst and carried by the supervisor
    When the proposal is saved, the supervisor's pass runs, and somebody who
      is neither the owner nor an admin, or who has no valid token, tries to
      stamp it
    Then no rule exists until the owner's own stamp

  @test:TestUnitRuleTargetHostileInputs
  @test:TestUnitRuleTargetBoundariesAreAccepted
  @test:TestAHostileUnitNameNeverReachesTheShowbackFileOrTheMarkup
  Scenario: A unit's name comes out of somebody else's file and is treated so
    Given unit names that are empty, padded, over the bound, carry control or
      direction characters, or begin as a spreadsheet formula, and targets
      that mix the two rule shapes or carry fields nothing reads
    When a rule is proposed for them
    Then every one is refused with a reason and the boundary cases are accepted,
      and an unruled unit's name is never printed in the showback or rendered
      as markup

  @test:TestAnUnstampedUnitIsNamedAsUnruledOnTheChargebackPage
  @test:TestClosePackSectionNamesAUnitWithNoRuleAndTheShapeOfAProposal
  @test:TestClosePackSectionNamesTheBusinessUnitOfARuledUnit
  Scenario: The chargeback page and the analyst's packet say which units have no rule
    Given units with spend and no rule
    When the chargeback page is read, or the chargeback analyst's close pack is built
    Then each unit is named with what it was charged and "no rule yet", and the
      packet says in what shape a proposal is written

  @test:TestTheGeneratedEstatesShowbackIsUntouchedByUnits
  @test:TestClosePackSectionSaysNothingOfUnitsOnTheGeneratedEstate
  Scenario: An estate with no customer units looks exactly as it did
    Given the generated estate
    When the chargeback page, the showback and the close pack are read
    Then there is no units panel, no unruled row and no units section
