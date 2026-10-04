# language: en

Feature: Every analyst family says in a closed list what it decides alone and what it hands up

  # @decided 2026-10-04: eight families had an empty decides_alone and five an
  # empty hands_up, with only prose beside them. The class lists are written
  # from each family's existing prose, shipped without a review, and the roles
  # gate refuses an empty list from now on unless the family carries an
  # explicit, reasoned exemption (the aim is none). The sixth clause of the
  # never sentence, acting on a task somebody blocked, is in the never list
  # now, bound to the test that holds it.

  @test:TestTheDecidesAloneListsAreWrittenFromTheProse
  Scenario: The decides-alone lists are written from the prose
    Given the eight families whose decides_alone was empty beside prose
    When roles.yaml is read
    Then the finops partner decides both commentary classes, and the AI spend
      analyst and the executive reporter each decide the variance
      commentary, and the five whose prose names no class stay empty

  @test:TestTheHandsUpListsAreWrittenFromTheProse
  Scenario: The hands-up lists are written from the prose
    Given the five families whose hands_up was empty beside prose
    When roles.yaml is read
    Then sustainability hands publishing up, deep analysis hands its own
      guard up as a roster change, migration watch hands an overrun over plan
      up, and the two whose prose says nothing is handed up stay empty

  @test:TestEveryAnalystFamilyHasBothListsOrAReasonedExemption
  Scenario: An empty list always has its reason
    Given every analyst family in roles.yaml
    When its two lists are checked
    Then each list is either written or empty beside an exemption of at
      least forty characters, and never both

  @test:TestTheExemptionsAreExactlyTheOnesDecided
  Scenario: An exemption needs a decision, not a quiet edit
    Given the exemptions this change names: five for decides_alone and two
      for hands_up
    When roles.yaml is read
    Then those families carry an exemption and no other family does

  @test:TestMayDecideAndEscalatesFollowTheWrittenLists
  Scenario: The lists are read by what decides and what escalates
    Given the written lists and the exempt families
    When a family is asked whether it may decide a class alone, and who it
      hands a class up to
    Then it may decide what it lists, an exempt family may not decide what
      it does not list, and a handed-up class goes to the link that owns it

  @test:TestARestrictedSustainabilityAnalystNowOwesAnOptionsBlock
  Scenario: A restricted analyst that may only propose proposes in options
    Given the sustainability analyst, restricted to propose only, now handing
      publishing up
    When its deliverable names no option
    Then it is returned for naming none, while the benchmarking analyst, with
      both lists exempt, may still end a deliverable in no options

  @test:TestRolesAreBoundRefusesAnEmptyDecidesAlone
  Scenario: The roles gate refuses an empty decides_alone
    Given roles.yaml with the finops partner's decides_alone emptied
    When scripts/roles-are-bound.sh runs
    Then it fails and names the family and the list

  @test:TestRolesAreBoundRefusesAnEmptyHandsUp
  Scenario: The roles gate refuses an empty hands_up
    Given roles.yaml with migration watch's hands_up emptied
    When scripts/roles-are-bound.sh runs
    Then it fails and names the family and the list

  @test:TestRolesAreBoundRefusesAnExemptionTooThinToBeAReason
  Scenario: An exemption must be a reason
    Given an exemption of three characters
    When scripts/roles-are-bound.sh runs
    Then it fails and says the exemption is thin

  @test:TestRolesAreBoundRefusesAnExemptionBesideAListThatIsNotEmpty
  Scenario: An exemption left behind after the list was written is refused
    Given a family that lists decides_alone and also carries an exemption
      for it
    When scripts/roles-are-bound.sh runs
    Then it fails and says the exemption is stale

  @test:TestRolesAreBoundRefusesAnAnalystDecidingAClassItDoesNotOwn
  Scenario: An analyst family decides alone only a class the analyst link owns
    Given a family given the supervisor's anomaly.accept to decide alone
    When scripts/roles-are-bound.sh runs
    Then it fails and says the class is owned by the supervisor

  @test:TestRolesAreBoundAcceptsAReasonedExemptionInOtherWords
  Scenario: The gate does not mind how an exemption is worded
    Given an exemption reworded at full length
    When scripts/roles-are-bound.sh runs
    Then it passes

  @test:TestTheNeverListCarriesTheBlockedClause
  Scenario: The crew never acts on a task somebody blocked, and the list says so
    Given the never list in roles.yaml
    When it is read
    Then it has six clauses, the sixth being to act on a task somebody blocked,
      and the sentence the card and the prompt show contains all six

  @test:TestABlockedTaskIsNotWorkedAround
  Scenario: A task somebody stopped stays stopped
    Given a board with queued, active, returned and blocked tasks
    When the runner picks the work to do
    Then it takes the first three and leaves every blocked one, whoever
      blocked it and whatever the reason

  @test:TestTheBlockedClauseIsBoundToTheRunnersTest
  Scenario: The blocked-task clause names the test that holds it
    Given roles.yaml's never_bound list
    When it is read
    Then the clause about a blocked task is bound to the runner's own test

  @test:TestRolesAreBoundRefusesANeverBindingWhoseTestIsGone
  Scenario: A binding whose test has gone is refused
    Given a never binding naming a test that exists nowhere
    When scripts/roles-are-bound.sh runs
    Then it fails and says the binding is dangling

  @test:TestRolesAreBoundRefusesANeverBindingWhoseClauseWasTakenOut
  Scenario: A binding for a clause that left the list is refused
    Given the blocked-task clause taken out of the never list while its
      binding stays
    When scripts/roles-are-bound.sh runs
    Then it fails and says the binding has no clause
