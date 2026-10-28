# language: en

Feature: The CI run is shorter and still proves everything it proved

  @claude 2026-10-28
  The request was to cut the wall-clock time of the main CI workflow on a pull
  request and on a push to main without removing, skipping or weakening any
  test, gate, teeth case or lint, and without renaming a job that branch
  protection requires. The words below paraphrase that request; no quote of
  anybody is in them.

  @test:TestTeethShardsCoverEveryCase
  Scenario: Every teeth case runs once in the shards CI starts
    Given the teeth cases split into the number of shards the workflow starts
    When each shard lists the cases it would run
    Then every case is in exactly one shard, the workflow lists every shard
      its totals expect, and the plan is for that number of shards

  @test:TestTeethShardsCoverEveryCaseWithoutThePlan
  Scenario: A shard count the plan was not made for is still a partition
    Given a person who runs five shards on a laptop
    When each shard lists the cases it would run
    Then every case is in exactly one of the five, counted by position

  @test:TestTheTeethPlanScriptBalancesTheShardsAndKeepsAGateTogether
  Scenario: The plan balances the shards by the seconds a case took
    Given the seconds each case took in earlier runs
    When the planner splits the cases over shards
    Then no shard is more than a tenth over the mean, and the cases that plant
      a fault against one test stay in one shard

  @test:TestTheRequiredChecksWaitForEveryJob
  Scenario: The two required checks are red unless every job passed
    Given a workflow whose required checks wait for the jobs doing the work
    When a job is added that neither of them waits for, or a required check is
      skipped instead of red when a job it needs fails
    Then the gate fails and names the job or the missing condition
