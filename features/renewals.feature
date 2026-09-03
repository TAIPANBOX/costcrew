# language: en

Feature: The SaaS manager's calendar, and where it stops

  @yurii 2026-09-02
  """
  більш повною мірою замінити людей на цих посадах
  """

  @yurii 2026-09-02
  """
  переговори з вендером проводити він сам особі не може
  """

  @test:TestRenewalsSectionListsTheCalendarWithNoticeDeadlines
  Scenario: The calendar says what renews and when notice is due
    Given seat lines a person imported from vendor exports, some renewing
      inside the next ninety days and one just past that edge
    When the SaaS portfolio manager's or the renewals analyst's own packet
      is built
    Then it lists every renewal inside the window with its own notice
      deadline, and names none of the ones past it

  @test:TestIdleSeatsAreCountedNotGuessed
  Scenario: Idle seats are counted, not guessed
    Given an imported seat line naming how many seats are issued and how
      many are active
    When idle seats and the money sitting in them are read back
    Then idle is issued minus active exactly, and the waste is idle seats
      times the price actually paid, to the cent, with no float anywhere on
      that path

  @test:TestRenewalNegotiationIsNeverDecidedInsideTheConsole
  Scenario: The negotiation is a person's
    Given a renewal whose ask is a vendor.negotiate, the class this
      practice never lets any link -- analyst, supervisor, or the owner's
      own stamp -- decide as a console action
    When the SaaS portfolio manager's or the renewals analyst's own job
      description is checked against that class
    Then neither role may decide it alone, and it does not escalate to
      anybody either: it stays an option a person carries into the
      conversation itself, outside the console
