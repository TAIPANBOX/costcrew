# language: en

Feature: A run on the local engine waits for the operator's server instead of timing out in its queue

  A server on the organisation's own hardware usually answers one request at
  a time and makes the rest wait. The runner used to keep four tasks in
  flight whatever the engine, and on such a server the tasks at the back of
  the queue ran out of time while they waited and were blocked, although the
  server would have answered every one of them in turn. The operator now sets
  how many local tasks run at once, the default is one, and the vendor
  engines keep their own width.

  A run asked to work one named task that cannot run says that task's own
  reason, instead of a sentence listing every reason any task might have had.

  # @test:TestOnAServerThatAnswersOneAtATimeNoLocalTaskIsBlockedWaiting
  Scenario: Tasks on a server that answers one at a time all finish
    Given four tasks on the local engine and a server that answers one request
      at a time, with a round timeout shorter than three of its answers
    When the runner runs them live without being told how many at once
    Then the server is handed one request at a time and every task finishes,
      none blocked for having waited

  # @test:TestLocalParallelSetsHowManyLocalTasksRunAtOnce
  Scenario: The operator raises the width for a server that has the slots
    Given five local tasks and -local-parallel set to three
    When the runner runs them live
    Then the server is handed three requests at once and never a fourth, and
      the run says how many local tasks it runs at a time

  # @test:TestVendorEnginesStillRunFourAtOnce
  Scenario: The vendor engines keep four at once
    Given six tasks on a vendor engine
    When the runner runs them live
    Then four are in flight at once, as before

  # @test:TestAVendorTaskIsNotHeldBehindTheLocalQueue
  Scenario: A vendor task does not wait for a local slot
    Given a run with two local tasks ahead of one vendor task
    When the runner runs them live, one local task at a time
    Then the vendor task is sent while the first local task is still being
      answered

  # @test:TestWaitingForALocalSlotIsNotCountedAgainstTheTasksDeadline
  Scenario: Waiting for a slot is not counted against a task's own deadline
    Given three local tasks run one at a time, each needing less than its
      deadline but together more
    When the last one finally gets its slot
    Then it is given its whole deadline from that moment and finishes

  # @test:TestLocalParallelIsValidatedBeforeTheStoreOpens
  Scenario: A width that is not a width is refused before anything happens
    Given -local-parallel below one or above the most any server here has
    When the runner starts
    Then it is refused naming the flag, before the data directory is touched

  # @test:TestOnlyATaskThatWasRefusedSaysItsOwnReason
  Scenario: A named task that was refused says why
    Given a run limited with -only to a task its own guard refuses
    When the run is started live
    Then it stops before any call and names that task and the reason it was
      refused

  # @test:TestOnlyATaskThatIsNotOpenSaysSo
  Scenario: A named task that is not open is not called refused
    Given a run limited with -only to a task that is not among the open tasks
    When the run is started live
    Then it says the task is not among the open tasks this run priced, and
      not that it was refused
