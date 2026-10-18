# language: en

Feature: every agent this console mints sits under the costcrew path

  @decided 2026-10-18
  """
  The console names each agent it mints under its own path, so one gateway
  client key bound to the costcrew prefix covers the whole crew, and no other
  agent of the same trust domain. The trust domain stays the deployment's own.
  """

  @test:TestAnalystIDsLiveUnderTheCostcrewPath
  Scenario: an analyst, the supervisor and the detector are named under the path
    Given a trust domain and an analyst's name
    When the console names the analyst, its supervisor and its detector
    Then each id is that domain, then costcrew, then the name

  @test:TestEveryMintedIDAtTheLongestNameIsAPassportURI
  Scenario: every id the console mints is a valid agent passport URI
    Given the longest name the roster allows
    When the id is built for a host that still fits
    Then agent-passport accepts it and it carries the costcrew path

  @test:TestOpenRefusesAHostTooLongForAnAnalystID
  Scenario: a host whose ids would be too long is refused before anything is written
    Given a trust domain one byte too long for the costcrew path
    When the console opens its event stream
    Then it refuses, and no event names an id a consumer cannot parse

  @test:TestSweepingLeavesOtherAgentsOfTheSameTrustDomainAlone
  Scenario: removing departed analysts never removes another agent of the same domain
    Given a passport of another agent in this trust domain outside the costcrew path
    When the console removes the passports of analysts no longer on the roster
    Then the other agent's passport is still there

  @test:TestTheOnBehalfOfChainNamesTheAnalystUnderTheCostcrewPath
  Scenario: the gateway chain names the analyst under the path
    Given an owner and an analyst
    When the console builds the on-behalf-of chain for a gateway call
    Then the chain is the owner's user id, then the analyst under the costcrew path
