# language: en

Feature: Sign-in through the organisation's identity provider

  # @claude 2026-10-07
  # The ask, paraphrased: people sign in to the console through the
  # organisation's identity provider with OpenID Connect, so that multi-factor
  # authentication and offboarding are handled where the organisation already
  # handles them. A group in the provider decides the role here; a person in no
  # mapped group gets no access at all; a change of group applies at the next
  # sign-in, and removal from the group ends the person's sessions. Password
  # sign-in can be switched off, keeping only the command line's break-glass
  # account. The console's Content-Security-Policy and its egress gate stay in
  # force, with the provider as a named, tested exception.

  @test:TestSignInThroughTheProviderCreatesTheAccountAndASession
  @test:TestAGoodSignInEstablishesWhoAndWhichRole
  @test:TestTheFirstExternalSignInCreatesTheAccountAtTheMappedRole
  Scenario: the first sign-in creates the account at the role its group maps to
    Given a provider that maps the group finops-admins to admin
    And a person in that group who has never used the console
    When they sign in through the provider
    Then an account is created for them as an admin
    And they hold a session that opens the console's pages

  @test:TestTheIDTokenIsCheckedClaimByClaim
  @test:TestEveryRefusedSignInLeavesNoSessionAndNoAccount
  Scenario: an ID token that fails any check signs nobody in
    Given a sign-in whose ID token has the wrong audience, has expired, is signed by a key the provider does not publish, carries no nonce or the wrong one, names another issuer, or was issued in the future beyond the allowed clock skew
    When the provider sends the browser back
    Then nobody is signed in and no account is created
    And the person is told to start again, never what the provider said

  @test:TestAStateIsSpentByItsFirstUse
  @test:TestAReplayedCallbackIsRefused
  Scenario: a sign-in can be completed once
    Given a sign-in that has already been completed
    When the same redirect from the provider is presented again
    Then it is refused and the provider's token endpoint is not asked again

  @test:TestACallbackInABrowserThatDidNotStartItIsRefused
  @test:TestAStateFromAnotherBrowserIsRefusedAndBurned
  Scenario: a sign-in started in one browser cannot be finished in another
    Given somebody started a sign-in and holds the provider's redirect
    When another browser is sent to that redirect
    Then that browser is not signed in as them
    And the started sign-in can no longer be finished anywhere

  @test:TestNoMappedRoleNeverCreatesAnAccount
  @test:TestTheRoleMappingIsStrictAndHasNoDefault
  @test:TestTheClaimsThatNameAndMapAreReadStrictly
  Scenario: a person in no mapped group gets no access, never a default role
    Given a person whose groups claim holds no mapped value, or no groups claim at all
    When they sign in through the provider
    Then they are told the organisation's sign-in gives them no access here
    And no account is created for them

  @test:TestARoleDowngradeAtTheProviderAppliesAtTheNextSignIn
  @test:TestARoleChangeAtTheProviderAppliesAtTheNextSignIn
  Scenario: a role changed in the provider applies at the next sign-in
    Given an admin whose group in the provider was changed to finops-viewers
    When they next sign in through the provider
    Then their account here is a viewer
    And a session they already held is a viewer's too

  @test:TestRemovalFromTheGroupEndsEverySessionAtTheNextSignIn
  @test:TestNoMappedRoleRefusesAndEndsEverySession
  Scenario: removal from the group ends the person's sessions at the next sign-in
    Given a person signed in through the provider in one browser
    And they have since been removed from every mapped group
    When they sign in through the provider again
    Then they are refused
    And the session in the first browser no longer opens any page

  @test:TestAProviderThatIsDownLeavesPasswordSignInWorking
  @test:TestAProviderThatIsDownIsUnreachableNotACrash
  Scenario: a provider that is down costs its own sign-in and nothing else
    Given the provider does not answer
    When somebody starts a sign-in through it
    Then they are told it could not be reached
    And a local account still signs in with its password

  @test:TestOIDCOnlyRefusesPasswordsExceptTheCommandLinesBreakGlass
  @test:TestUnderOIDCOnlyAPasswordSignsInOnlyToABreakGlassAccount
  Scenario: with password sign-in switched off, only the command line's break-glass account takes a password
    Given -oidc-only is set
    And one account whose password was set from the command line, and one that signed up through the form
    When each signs in with its right password
    Then the command line's account is signed in
    And the other is refused with the same sentence every failed sign-in shows

  @test:TestRegistrationIsClosedWhileAProviderIsConfigured
  Scenario: while a provider is configured, nobody registers through the form
    Given an installation with a provider configured and no admin yet
    When somebody opens or posts the registration form
    Then registration is closed and no account is created
    And a stranger is sent to the sign-in page, not the registration form

  @test:TestALocalAccountIsNeverAdoptedByName
  @test:TestTheSubjectNotTheNameIsTheLink
  @test:TestAnExternalAccountHasNoUsablePassword
  Scenario: an identity is linked by its subject, and never takes over an account by name
    Given a local admin account named root@example.test
    When an identity whose email is root@example.test signs in through the provider
    Then it is refused and the local account is unchanged
    And a person whose email changes at the provider keeps the account they already had

  @test:TestTheClientSecretComesFromOnePlaceAndNeverPrints
  @test:TestTheClientSecretAppearsInNoPageAndNoJournalLine
  Scenario: the client secret is read from the environment or a file and never shown
    Given the client secret is set in the environment or in a file, not both
    When the configuration is logged, printed or wrapped in an error, and sign-ins succeed and fail
    Then the secret appears in no log line, no page and no journal entry

  @test:TestLoadIsOffWhenNothingIsConfigured
  @test:TestLoadRefusesWhatIsHalfConfiguredOrUnsafe
  @test:TestWithNoProviderTheOIDCRoutesAreNotThere
  Scenario: the feature is off unless configured, and a half configuration does not start
    Given no issuer is configured
    Then sign-in is exactly as it was and the provider's routes are not there
    But a configuration with an issuer and a missing client, secret, redirect or role mapping, or a plain http address off this machine, is refused at start

  @test:TestTheSignInPageReachesTheProviderByALinkNotAForm
  Scenario: the way to the provider is a link, so the Content-Security-Policy holds
    Given the console's policy allows a form to post only to the console itself
    When the sign-in page offers the provider
    Then it is a link the console answers with a redirect, and every form still posts to the console

  @test:TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork
  @test:TestTheProviderIsNotContactedUntilASignInStarts
  @test:TestTheSignInClientReachesOnlyHTTPSOrLoopback
  @test:TestNoRedirectFromTheProviderIsFollowed
  @test:TestAResponseOverTheCapIsRefusedNotRead
  @test:TestADiscoveryDocumentNamingAnotherIssuerIsRefused
  @test:TestADiscoveredAuthorizationEndpointOverPlainHTTPIsRefused
  @test:TestWhatTheProviderSaysReachesTheJournalBoundedAndPlain
  Scenario: the sign-in is the console's second way to the network, and a narrow one
    Given the egress gate allows outbound requests only from named packages
    Then the sign-in package is named with its reason and may build a client in one file only
    And it contacts the provider only once somebody starts a sign-in
    And it reaches only https or this machine, follows no redirect, reads no body over one mebibyte, and trusts no discovery document that names another issuer or sends the browser anywhere but https
    And what the provider says reaches the journal cut short and stripped of control characters
