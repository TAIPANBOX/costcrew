# language: en

Feature: An organisation chooses how much of its billing data may reach a model

  The crew's analysts and the supervisor are language models, and what they
  are shown is the organisation's own bill: team names, service names, money,
  agent ids, invoice ids, vendors, commitments, resource ids, the names of the
  people who answered, past deliverables, the goal an operator typed. Nothing
  here used to redact any of it. One setting per installation, a flag and its
  environment twin, now decides how much of that leaves the process: full
  (what this console has always sent, and the default), masked (every name
  replaced by a stable token, the free text left out, no tool that lets the
  model write SQL) and aggregates (only totals).

  # @test:TestPromptDataIsAClosedVocabulary
  Scenario: The setting has three values and nothing else
    Given the setting
    When it is full, masked or aggregates, spelled exactly
    Then it is accepted, and anything else, including a capital letter, a
      space, an empty value or a near miss, is refused with the three words
      it would have accepted

  # @test:TestThePromptDataEnvironmentVariableBacksTheFlagDefault
  Scenario: An installation can set it once, in the environment
    Given the environment variable that backs the flag
    When it is unset, set to masked, or set to a misspelling
    Then the default is full, masked, or a value the same parser refuses

  # @test:TestAMisspeltPromptDataFlagRefusesToStartTheRunner
  Scenario: A misspelling stops the runner before anything is opened
    Given the runner started with a misspelt setting, on the command line or
      in the environment
    When it starts
    Then it exits naming the three modes, before a store is opened or a
      task is priced, because a setting whose purpose is to send less must not
      fall back to sending everything when it is mistyped

  # @test:TestAMisspeltPromptDataFlagRefusesToStartTheConsole
  Scenario: A misspelling stops the console before it listens
    Given the console started with a misspelt setting
    When it starts
    Then it exits naming the three modes, and has opened no store

  # @test:TestFullModeBuildsTheSamePacketItAlwaysDid
  Scenario: Full is today's behaviour, byte for byte
    Given a packet built from the generated estate before this setting existed
    When the same packet is built under the default setting
    Then it is the same bytes, section by section

  # @test:TestNoRealIdentifierLeavesInMaskedOrAggregatesPackets
  Scenario: Under masked and aggregates no real name is in any packet
    Given a whole installation: every team, desk, service, agent, console
      username, invoice, vendor, product, commitment, resource and run id,
      found by walking the schema and not by asking the masker what it knows
    When every packet section is built for every analyst
    Then not one of those values is in what would be sent, byte for byte

  # @test:TestEveryTextColumnIsClassified
  Scenario: A new column cannot add a name nobody decided about
    Given the schema of an installation
    When a text column is in no list of identifiers, free text, generated
      text or things that are neither
    Then the gate fails and names the column, so the decision is made before
      anything passes

  # @test:TestMaskedPacketKeepsMoneyDatesAndCounts
  Scenario: Masking changes names and nothing else
    Given a packet of money, dates, counts and ratios around team and
      service names
    When it is built under masked
    Then every figure and every date is still there, in the same place, and
      the section headers are the same words

  # @test:TestFreeTextIsWithheldUnderMaskedAndAggregates
  Scenario: Text a person typed is not sent, and the packet says so
    Given past deliverable bodies, option summaries, refusal reasons and the
      labels of drivers, any of which can carry a name the store never held
    When a packet is built under masked or aggregates
    Then none of that text is in it, and under masked a one-line stand-in
      says something was left out

  # @test:TestTheOperatorsGoalAndATaskGoalAreWithheldUnderMaskedAndAggregates
  Scenario: The goal an operator typed is not sent
    Given a task whose goal is whatever the operator wrote
    When its prompt is built under masked or aggregates
    Then the goal is replaced by the stand-in line and its words are not in
      the prompt

  # @test:TestOnlyTheRoleFamilysOwnBriefIsSentUnderMasked
  Scenario: A brief somebody typed when hiring is not sent
    Given an analyst hired with a mission written by hand, and one whose
      mission is the role family's own sentence
    When the prompt is built under masked or aggregates
    Then the hand-written brief is replaced by the stand-in and the role
      family's own sentence, which is the same in every installation, is not

  # @test:TestAggregatesCarryNoRowLevelSections
  Scenario: Aggregates send totals and no row
    Given the sections that are one row per driver, deliverable, resource,
      agent, model, invoice, licence or commitment
    When a packet is built under aggregates
    Then none of them is in it, no token of a row-level kind is in it, and
      the sections that are totals are still there

  # @test:TestMaskedPacketsKeepEverySectionHeaderTheirModeSends
  Scenario: Masking does not garble this console's own words
    Given analysts called renewals, commitments and governance, and teams
      called research and growth
    When packets are built under masked
    Then every section header is still the same words, and aggregates drops
      exactly the row-level sections

  # @test:TestSQLToolsAreNotOfferedUnderMaskedOrAggregates
  Scenario: The model is not offered a tool whose argument is SQL it writes
    Given the two tools that run a statement the model wrote
    When the catalogue is rendered under masked or aggregates, for either
      provider, or the model calls one anyway
    Then neither is offered, and a call to one is answered with the reason
      and is not run, because a statement can select any name in a column or
      cut one in two, and no scrub can be trusted to recognise half a name

  # @test:TestEveryToolHasAPolicyDecision
  Scenario: A tool added tomorrow is not offered until somebody decides
    Given the tool catalogue
    When a tool is in no list for a restricting mode
    Then that mode does not offer it, and the decision table names every tool
      that exists and none that does not

  # @test:TestNoRealIdentifierLeavesInAnyToolResult
  Scenario: Under masked and aggregates no real name is in any tool result
    Given a whole installation and an analyst holding every right
    When every tool is called, in each mode, the way a model would call it
    Then no identifier the schema walk found is in any result, and the tools
      that a mode offers answer instead of refusing

  # @test:TestAMaskedToolResultIsTheFullResultWithItsNamesMasked
  Scenario: A masked tool answers the question that was asked
    Given a model that names a team by its token in a tool call
    When the tool runs
    Then it ran for the real team, and its answer is the full answer with the
      names turned to tokens and nothing else changed

  # @test:TestAToolErrorThatEchoesANameIsMasked
  Scenario: An error does not hand a name back
    Given a tool that fails and repeats what it was asked for
    When the model's token was put back before the tool ran
    Then the error the model reads carries the token again

  # @test:TestTheSameNameIsTheSameTokenInEveryRoundAndEveryText
  Scenario: The model can reason across rows and rounds
    Given a team that appears in a packet and again in a tool result
    When both are masked, in the same run or after the store changed
    Then it is the same token each time, so the model can tell it is the
      same team

  # @test:TestTheSameKeyGivesTheSameTokensAcrossRunsAndAnotherKeyDoesNot
  Scenario: Tokens are the installation's own
    Given two installations with different keys and one installation run twice
    When the same name is masked
    Then one installation gives the same token both times and the other
      installation gives a different one

  # @test:TestTheKeyLivesInTheDataDirWithMode0600
  Scenario: The key is kept in the data directory and nobody else can read it
    Given the first start in a restricting mode
    When the key is made
    Then it is 32 random bytes in a file in the data directory, mode 0600

  # @test:TestADataDirectoryTheKeyCreatesIsPrivateAndAnExistingOneIsLeftAlone
  Scenario: A directory made for the key is private
    Given a first start in a restricting mode with a data directory that
      does not exist yet
    When the key makes the directory before the store opens
    Then the directory is private to the account, the way the store makes
      one, and a directory that was already there is left as it was

  # @test:TestThePseudonymKeyCannotBeCommitted
  Scenario: The key cannot be committed by accident
    Given a data directory that is somebody's working directory
    When the key, or the file it is written to before it is linked into
      place, is left in it
    Then version control ignores both

  # @test:TestAKeyFileOthersCanReadIsRefused
  Scenario: A key anyone can read is refused
    Given a key file whose mode lets others read it
    When the installation starts in a restricting mode
    Then it refuses, because whoever read the key can test any name against
      the tokens

  # @test:TestAPolicyIsSafeAndConsistentWhenManyTasksUseItAtOnce
  Scenario: Tasks that run at once get the same tokens
    Given a runner working several tasks at once through one policy
    When each masks text and puts names back, many times
    Then every one of them gets the same token for the same name, no name
      survives, and every round trip returns the text it started from

  # @test:TestReidentifyBringsBackExactlyTheNamesThatWereMasked
  Scenario: The answer a person reads has real names
    Given an answer the model wrote in tokens
    When it is re-identified
    Then every token this installation handed out is the name it stood for

  # @test:TestATokenTheModelInventedStaysAsWritten
  Scenario: A token the model made up is left alone
    Given a token that maps to nothing, or one with a digit too many
    When the answer is re-identified
    Then it is exactly as the model wrote it

  # @test:TestWhatReachesTheModelOverTheWireLeaksNoIdentifierAndTheDraftComesBackNamed
  Scenario: End to end, nothing leaves and the draft is named
    Given a runner under masked or aggregates, and a model that asks for a
      tool in tokens and answers in tokens
    When a task is run
    Then none of the requests it received holds a real name from the packet,
      the tool schemas or the tool results, and the draft that is saved reads
      with the real names

  # @test:TestCallHandsBackTheAnswerWithItsRealNames
  Scenario: Every answer comes back through one door that puts the names back
    Given a call through the shared caller under masked
    When the model answers in tokens
    Then the caller receives real names

  # @test:TestThePromptSaysWhichModeItWasBuiltUnder
  Scenario: A recorded prompt says which setting built it
    Given a prompt in each mode
    When it is read
    Then it states the mode in one line, once

  # @test:TestTheModeIsOnTheToolCallEventsAndTheCrewRanSummary
  Scenario: A run's evidence says what policy it ran under
    Given a run in each mode
    When it calls a model, dispatches a tool and finishes
    Then the tool_call events and the crew_ran summary carry the mode

  # @test:TestThePlanPacketLeaksNoIdentifierOrGoalUnderMaskedAndAggregates
  Scenario: The supervisor's planning call is held to the same setting
    Given a sprint plan with an operator's goal and a roster
    When the planning prompt is built under masked or aggregates
    Then it holds no real name and not the goal, and under aggregates names
      no agent and no service at all

  # @test:TestAPlanAnswerNamingTokensIsAcceptedOnlyOnceItIsReidentified
  Scenario: A plan that re-routes work by token is checked against real names
    Given a plan answer naming an analyst by its token
    When the console validates it
    Then it is refused as written and accepted once re-identified

  # @test:TestPuttingANameBackCannotWriteArgumentsOfItsOwn
  Scenario: A hostile name cannot rewrite a tool call
    Given a team whose name contains a quote and the text of another argument
    When the model's token for it is put back into a tool call
    Then the call still has the arguments the model wrote, and the name is one
      string among them
