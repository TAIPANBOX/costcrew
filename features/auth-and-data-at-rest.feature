# language: en

Feature: Sign-in does not leak who exists, and what is stored on disk is not a way in

  # @claude 2026-10-07
  # Four findings from reading the code, written as what should be true.
  # Session tokens sat in the database in clear text, so a copy of app.db was a
  # set of live logins. A locked account answered differently from an unknown
  # one, so the form could be asked which names exist. The data directory and
  # the journal were readable by every local user. And the sentence that the
  # console makes no outbound call while serving a page had no check behind it.
  # Passports and the -stack-events file are shared with other services by
  # design and are not part of the file-permission change.

  @test:TestTheDatabaseHoldsNoSessionTokenInTheClear
  Scenario: the database holds a hash of the session, never the session
    Given somebody has signed in and holds a session cookie
    When the database file and its write-ahead log are read
    Then the cookie value appears nowhere in them
    And what the sessions table holds is the SHA-256 of the cookie

  @test:TestAHashReadFromTheDatabaseIsNotACookie
  Scenario: a value read out of the database does not sign anybody in
    Given a session row has been read out of the database
    When any value from that row is presented as the session cookie
    Then nobody is signed in

  @test:TestSessionUserSurvivesHostileCookies
  Scenario: hostile cookie values resolve to nobody and break nothing
    Given an installation with a signed-in account
    When a cookie is empty, a megabyte long, carries a NUL, an SQL tail, or is
      the hash of a real cookie
    Then it resolves to nobody, with no error

  @test:TestOldClearTextSessionsAreEndedAndErasedByTheMigration
  Scenario: sessions written before the change are ended and leave no trace on disk
    Given a database written by the version that stored cookies in clear text
    When the new version opens it
    Then those sessions no longer sign anybody in
    And the old cookie values are not readable in the database file or its log
    And the accounts are untouched
    And the sign-out of everybody is written in the journal

  @test:TestTheMigrationRunsOnceAndKeepsTheSessionsItDidNotWrite
  Scenario: restarting does not sign everybody out again
    Given the migration has already run
    When the console is started twice more
    Then a session started after the migration still signs in

  @test:TestSessionLifecycleByTheCookieValue
  Scenario: ending a session, or letting it expire, still works by the cookie value
    Given a session cookie
    When the session is ended, or its time has passed
    Then the cookie no longer signs anybody in

  @test:TestCSRFStaysBoundToTheCookieValue
  Scenario: the CSRF token is still derived from the cookie value
    Given a session cookie
    When the CSRF token for it is computed
    Then it is the keyed hash of the cookie under the installation's key
    And a token from one session does not verify in another

  @test:TestEveryFailedSignInSaysTheSame
  Scenario: an unknown name, a wrong password and a locked account get one answer
    Given an account that exists and a name that does not
    When each is refused: the unknown name, a wrong password, and a locked
      account even with the right password
    Then the words of the refusal are identical in all three
    And they do not say that an account is locked, or for how long

  @test:TestTheRefusalDoesNotChangeAsFailuresAccumulate
  Scenario: the refusal does not change as failed attempts pile up
    Given an account that has been guessed at several times
    When it is refused again and again
    Then every refusal reads like the first

  @test:TestEveryFailedSignInLooksTheSameFromOutside
  Scenario: the sign-in form's status and redirect do not differ either
    Given the console serving its sign-in form
    When a stranger tries an unknown name, a wrong password, and a locked account
    Then the status and the place they are sent back to are identical
    And no cookie is set

  @test:TestTheLockoutItselfIsStillThere
  Scenario: the lockout and its journal entries are unchanged
    Given an account with three wrong passwords against it
    When the right password is then presented
    Then it is still refused, until the lock runs out
    And each wrong password was written in the journal

  @test:TestALockedAccountCostsAsMuchAsAnUnknownOne
  Scenario: a locked account is not the fast answer of the three
    Given an unknown name and a locked account
    When each is refused
    Then both pay for one password hash

  @test:TestTheDataDirectoryIsPrivateWhenTheStoreCreatesIt
  Scenario: a data directory the console creates is private to its account
    Given a data directory that does not exist
    When the console opens its store there
    Then the directory is created readable only by the account running it

  @test:TestAnExistingDataDirectoryKeepsItsMode
  Scenario: a directory that was already there is left as found
    Given a data directory that already exists, such as the working directory
    When the console opens its store there
    Then its mode is not changed

  @test:TestEveryFileTheStoreCreatesIsPrivate
  Scenario: the database, its log and the journal are private, on every start
    Given a fresh data directory
    When the console starts, writes, stops, and starts again
    Then app.db, app.db-wal, app.db-shm and the journal are readable only by
      the account running it, both times

  @test:TestFilesFromBeforeTheChangeAreTightenedOnOpen
  Scenario: an installation that already has world-readable files is tightened
    Given a data directory whose files were made before this change
    When the console opens it
    Then those files become private without waiting to be recreated

  @test:TestTheJournalIsCreatedPrivateByTheFirstAppend
  Scenario: the journal is private from the moment it exists
    Given a store with no journal yet
    When the first record is appended
    Then the journal is created readable only by the account running it

  @test:TestAChmodTheFilesystemRefusesIsAWarningNotSilenceNotAnOutage
  Scenario: a file the console cannot make private is reported, not hidden and not fatal
    Given a mount that refuses a permission change
    When the console opens its store
    Then it still starts, and the refusal is listed among its warnings

  @test:TestSharedFilesStayReadableByOtherServices
  Scenario: the passports and the bus event file stay readable by the services they are for
    Given the console writing agent passports and its event stream
    When their permissions are read
    Then other accounts can still read them

  @test:TestTheSessionSecretAndTheDatabaseFilesCannotBeCommitted
  Scenario: the signing key and the data files cannot be committed by accident
    Given the repository's ignore list
    When it is read
    Then it names the session secret, the database with its log files, and the journal

  @test:TestTheConsoleConstructsNoOutboundHTTPClientOrRequest
  Scenario: the console builds no outbound HTTP client or request of its own
    Given the source of the console and of its command
    When it is walked for any construction of an outbound client or request
    Then there is none

  @test:TestDeliverCallIsReachedFromExactlyOnePlace
  Scenario: the supervisor's plan-ask is the one route out
    Given the source of the console and of its command
    When it is searched for calls to the model-calling door
    Then there is exactly one, in the plan-ask handler

  @test:TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork
  Scenario: no other package the console imports can reach out either
    Given every package of this module that the console imports, directly or not
    When they are walked the same way
    Then only the delivery package builds outbound requests
    And the budget-pushing package is not among them

  @test:TestTheEgressWalkSeesEveryConstructionItNames
  Scenario: the walk really sees each way of reaching the network
    Given a snippet for each way to build a client, a request or a dial
    When the walk reads it
    Then it reports every one

  @test:TestTheEgressWalkLeavesServerCodeAlone
  Scenario: the walk does not refuse what a server legitimately uses
    Given handlers, redirects, cookies, a mux and a listener
    When the walk reads them
    Then it reports nothing
