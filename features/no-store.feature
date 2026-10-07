# language: en

Feature: No cache keeps a page or a download of this console

  # @claude 2026-10-07
  # The ask, paraphrased: authenticated pages and exports carried no
  # Cache-Control, so a browser on a shared machine, a proxy, or the back
  # button after sign-out could show the estate's figures to the next person.
  # The stylesheet holds nothing about the estate and stays cacheable.
  # Invariant 79.

  @test:TestEveryGuardedResponseIsMarkedNoStore
  Scenario: every page and every download is marked no-store
    Given a signed-in person, and separately a stranger
    When either requests any page or download the console serves
    Then the response says no cache may store it, the redirect a stranger is
      turned away with and a page that does not exist included

  @test:TestTheStylesheetIsNotMarkedNoStore
  Scenario: the stylesheet may be cached
    Given the stylesheet every page shares
    When it is requested
    Then it is not marked no-store
