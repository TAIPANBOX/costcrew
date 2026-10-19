# language: en

Feature: The console's defaults send less, refuse more, and keep its key to itself

  # @decided 2026-10-17
  # Paraphrase: the review of the 2026-10-17 security findings asked for
  # defaults that fail safe. Three of them were the console's own: the prompt
  # data it sends a model defaulted to everything, a listener another machine
  # could reach served plain HTTP with cookies that could travel in the clear,
  # and the session key was read whatever its mode. Each gets a safe default,
  # and the one way to get the old behaviour is to name it.
  #
  # Invariant 110 in CLAUDE.md holds these with their gates.

  @test:TestAnAddressAnotherMachineCanReachNeedsTLSOrTheExplicitFlag
  Scenario: an address other machines can reach is refused without TLS or the explicit flag
    Given the console is asked to listen on an address that is not loopback
    When it is started without -behind-tls and without -insecure-http
    Then it refuses to start, and its message names both flags

  @test:TestAnAddressAnotherMachineCanReachNeedsTLSOrTheExplicitFlag
  Scenario: the same address starts when plain HTTP there is named on purpose, or TLS is in front
    Given the console is asked to listen on an address that is not loopback
    When it is started with -insecure-http, or with -behind-tls
    Then it starts

  @test:TestExposedListenerMarksEveryCookieSecureWithoutTheFlag
  Scenario: every cookie is Secure on an address that is not loopback, whatever -behind-tls says
    Given the console listens on an address that is not loopback and -behind-tls is off
    When somebody logs in over plain HTTP
    Then the session cookie carries Secure

  @test:TestLoginOverPlainHTTPStaysInsecureWithoutBehindTLS
  Scenario: on loopback nothing changes
    Given the console listens on loopback and -behind-tls is off
    When somebody logs in over plain HTTP
    Then the session cookie does not carry Secure, exactly as before

  @test:TestLoopbackListenIsOnlyTheLoopbackInterface
  Scenario: only the loopback interface counts as loopback
    Given an address that listens on every interface, or a name that is not localhost
    When the console decides whether that address is loopback
    Then it answers no

  @test:TestASessionSecretReadableByItsGroupOrOthersStopsTheStart
  Scenario: a session key that another account can read stops the start
    Given a session key file whose group or other accounts can read it
    When the console starts
    Then it refuses to start and names chmod 600, and the key is left as it was

  @test:TestASessionSecretThatIsALinkStopsTheStart
  Scenario: a session key that is a link stops the start
    Given the session key path is a symbolic link to a file that is owner-only
    When the console starts
    Then it refuses to start, and the file the link names is left alone

  @test:TestAnUnreadableSessionSecretIsNotReplaced
  Scenario: a session key the console cannot read is not replaced with a new one
    Given a session key file this account cannot read
    When the console starts
    Then it refuses to start and the file is not overwritten

  @test:TestTheSessionSecretIsCreatedOwnerOnlyWhenAbsent
  Scenario: a missing session key is created owner-only
    Given a data directory with no session key
    When the console starts
    Then the key it creates is readable by its owner alone

  @test:TestThePromptDataDefaultIsMasked
  Scenario: with nothing configured the console sends masked prompt data
    Given neither -prompt-data nor COSTCREW_PROMPT_DATA is set
    When the console or the runner starts
    Then the prompt data mode is masked

  @test:TestTheEnvironmentStillChoosesFull
  Scenario: full is still available when it is named
    Given COSTCREW_PROMPT_DATA is set to full
    When the console or the runner starts
    Then the prompt data mode is full

  @test:TestPromptDataSourceSaysWhereTheSettingCameFrom
  Scenario: the start log says whether the prompt data mode was chosen or is the default
    Given the mode came from the default, the command line or the environment
    When the console starts
    Then its log names where the mode came from
