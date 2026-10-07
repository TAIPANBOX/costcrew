# language: en

Feature: The console's HTTP edge refuses what a file or a stranger can send it

  # @claude 2026-10-07
  # Three defects found by reading the HTTP surface: the downloadable results
  # page wrote values from an imported file into HTML unescaped; no response
  # carried a Content-Security-Policy or any of the other headers a browser
  # reads to defend itself; and no request body was capped while the server set
  # a timeout for the request line and nothing else. The ask was to escape
  # every value in that export, to set the headers on every response with a
  # policy the pages actually satisfy, and to cap bodies and set the server's
  # timeouts without cutting off the slowest legitimate page.

  @test:TestResultsExportEscapesWhatAnImportedRowCarries
  Scenario: a service named as a script tag is shown as text in the downloadable report
    Given an anomaly whose service, source, day and cause came from an
      imported file and are each a script tag
    When somebody downloads the results as an HTML file
    Then every one of those values appears escaped
    And no script tag appears anywhere in the file

  @test:TestResultsExportStillSaysWhatItSaid
  Scenario: escaping the report does not change what it says
    Given the seeded estate and no hostile row
    When somebody downloads the results as an HTML file
    Then it still carries the headline, the unexplained anomalies, the desks
      and the decisions needed

  @test:TestResultsExportHasNoHandWrittenHTMLWriter
  Scenario: the report's writer cannot go back to building HTML by hand
    Given the function that serves the downloadable report
    When its source is read
    Then it calls no Fprintf, Sprintf or Write of its own and executes one
      html/template, so a value reaches the page only escaped

  @test:TestEveryRouteCarriesTheSecurityHeaders
  Scenario: every response carries the browser-side defences
    Given every route in the route table, public and private, GET and POST
    When each is requested as a stranger and as a signed-in person
    Then every response carries nosniff, X-Frame-Options DENY, a same-origin
      Referrer-Policy and a Content-Security-Policy, errors and redirects included

  @test:TestTheContentSecurityPolicyAllowsNoScript
  Scenario: the policy allows no script, and only styles may be inline
    Given the Content-Security-Policy the console sends
    When its directives are read
    Then no script is allowed, framing, base and form targets are closed, and
      the only inline allowance is for styles

  @test:TestNoPageReliesOnWhatThePolicyForbids
  Scenario: the policy does not break any page
    Given every page the console serves and every template it holds
    When they are read for scripts, event handlers and outside resources
    Then there are none, so the policy forbids nothing a page needs

  @test:TestSignOutWorksWithoutAScript
  Scenario: Sign out is a real form button
    Given the sidebar's Sign out control
    When it is submitted with no script running
    Then the session ends and the next page asks for a sign-in

  @test:TestStrictTransportSecurityFollowsTheCookiePosture
  Scenario: the HTTPS-only promise is made only where TLS is in front
    Given a console behind a TLS proxy, one that terminates TLS itself and one on plain HTTP
    When each answers a request
    Then the first two send Strict-Transport-Security and the third does not

  @test:TestAnOversizedPostIsRefusedAndChangesNothing
  Scenario: a request body over the cap is refused with 413 before any handler runs
    Given a signed-in operator and a form with a field the handler ignores,
      padded past 1 MiB, declared or chunked
    When it is posted
    Then the answer is 413 with a plain message, and the setting it would have
      changed is unchanged

  @test:TestANormalPostStillWorks
  Scenario: a form at the cap still works
    Given a signed-in operator and a form of exactly 1 MiB
    When it is posted
    Then the handler runs and does what the form asks

  @test:TestAStrangerCannotMakeTheLoginFormReadMegabytes
  Scenario: a stranger cannot make the sign-in form read megabytes
    Given nobody is signed in
    When a 2 MiB body is posted to the sign-in form
    Then the answer is 413

  @test:TestIntakeStillAcceptsAFileUpToItsOwnCap
  Scenario: the budget intake still takes a file of 2 MB
    Given a budgets file of exactly 2 MB, which is larger than the general cap
    When it is uploaded to the intake check
    Then the preview is shown

  @test:TestIntakeRefusesAFileOverItsCapInsteadOfCuttingIt
  Scenario: a budgets file over 2 MB is refused, not read in part
    Given a budgets file one byte over 2 MB
    When it is uploaded to the intake check
    Then the person is told it is over 2 MB and no preview of a cut file is shown

  @test:TestAnOversizedIntakeUploadIsRefusedAndChangesNothing
  Scenario: an upload far over the intake's cap is refused before it is read
    Given a budgets upload more than a megabyte over its cap
    When it is posted
    Then the answer is 413 and no budget changed

  @test:TestIntakeApplyAcceptsTheEncodedFileItCheckedAndRefusesMore
  Scenario: the intake's second step carries the checked file back at its encoded size
    Given a 2 MB file sent back as a form field the way a browser encodes it
    When it is posted to apply
    Then it is not refused as too large, while a body far past that is

  @test:TestTheServerSetsEveryTimeoutAndOutlastsTheLongestHandler
  Scenario: the server waits on no peer forever and never cuts off the slowest page
    Given the server the console is started with
    When its timeouts are read
    Then the header, read, write and idle timeouts are all set, and the write
      timeout is more than twice the longest wait on a model
