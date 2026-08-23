# language: en

Feature: Who may do what

  @yurii 2026-08-22
  """
  Треба додати в аккаунт можливість видалення, але з правами адміністратора
  можна це робити.
  """

  @test:TestAnOperatorCannotEscalateThroughAccounts
  Scenario: An operator cannot climb
    Given an operator account
    When it tries to promote itself, create an admin, or demote the admin
    Then every attempt is refused
    And the database shows no role changed and no account created

  @test:TestAViewerCannotWrite
  Scenario: A viewer reads and exports and writes nothing
    Given a viewer with a real session and a real CSRF token
    When it posts to any of the thirty write routes
    Then each one refuses it, saying the account may read and export but not act

  @test:TestEveryRouteRequiresASession
  Scenario: A stranger is turned away everywhere
    Given no session at all
    When any of the forty-eight read routes is opened
    Then it turns the stranger away, except the seven that carry a written
      reason not to

  @test:TestEveryWriteRouteChecksCSRF
  Scenario: A request from another site is refused
    Given an admin session and a request with no token, or the wrong one
    When it posts to any write route
    Then it is refused for the token, and says so

  @test:TestWhatAViewerCanRead
  Scenario: A viewer sees who the accounts are and none of the controls
    Given a viewer
    When it opens the accounts page
    Then the page answers, and carries no form for creating, removing or
      re-roling an account
