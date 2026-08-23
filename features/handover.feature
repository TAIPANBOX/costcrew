# language: en

Feature: Somebody else can run it

  @yurii 2026-08-23
  """
  Мені важливо, щоб нова версія, яка написана на Go, могла бути передана Тані,
  щоб вона могла запустити, подивитися і так далі все.
  """

  @test:TestSignupIsOpenUntilThereIsAnAdmin
  Scenario: The first person to open a fresh installation can get in
    Given an installation with five seeded owner accounts nobody can sign in to
    When the first person registers
    Then they are admitted, and they are the admin
    And registration closes behind them

  @test:TestSeedOwnersPlacesTheRosterWithNoOwnerConfigured
  Scenario: An installation started with no flags still has owners
    Given costcrew started with no -stack-owner
    When the estate is seeded
    Then every agent on the roster has somebody answering for it

  @test:TestBackfillMandateConverges
  Scenario: Starting it twice reports nothing the second time
    Given an installation that has already started once
    When it starts again
    Then the startup says nothing about work it did not do
