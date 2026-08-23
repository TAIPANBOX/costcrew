# language: en
#
# Every scenario here comes from something Yurii asked for, quoted verbatim
# above it. They are NOT derived from the code: a scenario written by reading
# what I built only proves I can describe my own work.
#
# Each scenario names the test that holds it with an at-test tag. Nothing reads
# these files at runtime; scripts/features-are-bound.sh asserts the binding in
# both directions.

Feature: An agent belongs to somebody, and belongs to them completely

  @yurii 2026-08-22
  """
  І треба мати можливість видаляти агентів, але це мають робити тільки ті,
  хто є їхнім власником. І також дати можливість передати агента в інший
  юніт, чи в інший там деск, чи ще кудись. Відповідно, витрати також мають
  переходити на інших власників агента.
  """

  @test:TestAnOperatorCannotRemoveOrTransferSomebodyElsesAgent
  Scenario: Somebody who does not own an agent cannot take it off the roster
    Given an agent hired by alice
    And bob is an operator, so he may act
    When bob tries to remove it
    Then he is refused, and told that only alice or an admin may
    And the agent is still on the roster

  @test:TestAnOperatorCannotRebriefSomebodyElsesAgent
  Scenario: Nor rewrite what it is for, nor raise what it may spend
    Given an agent hired by alice with a guard of 150.00 a month
    When bob tries to re-brief it with a guard of 5000.00
    Then he is refused
    And the guard in the database is still 150.00

  @test:TestAnOperatorCannotChangeTheStateOfSomebodyElsesAgent
  Scenario: Nor stop its work
    Given an active agent hired by alice
    When bob tries to suspend it
    Then he is refused, and the agent is still active

  @test:TestTheOwnerAndAnAdminCanStillManage
  Scenario: The owner manages her own, and an admin manages anybody's
    Given an agent hired by alice
    When alice re-briefs it and suspends it
    Then both take effect
    When an admin puts it back to work
    Then that takes effect too, because somebody has to clean up after a
      person who has left, and that is when the owner cannot act

  @test:TestHiringMakesYouTheOwner
  Scenario: Hiring an agent makes you its owner
    Given alice is an operator
    When she hires an agent
    Then its owner is alice and not the name the installation was configured with
    And she can re-brief what she just hired

  @test:TestATransferMovesTheAuthorityWithTheAgent
  Scenario: A transfer moves the authority in both directions
    Given an agent hired by alice
    When alice transfers it to bob
    Then bob may re-brief it
    And alice may not, and may not take it back

  @test:TestATransferSplitsTheSpendAtTheHandover
  Scenario: The spend follows the agent, split at the handover
    Given an agent hired by alice that has cost 166.46, of which 32.62 is
      still open work
    When alice transfers it to bob
    Then bob answers for the 32.62 that is still running
    And alice answers for the 133.84 she authorised and that is closed
    And the two halves add up to what the agent has cost

  @test:TestTheRebriefPageFollowsOwnership
  Scenario: The card does not offer a control the reader cannot use
    Given an agent hired by alice
    When bob opens its card
    Then there is no re-brief link on it
    And going to the re-brief page directly sends him back to the card with
      the reason
