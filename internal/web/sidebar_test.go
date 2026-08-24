package web_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The sidebar fits a window without a scrollbar of its own.
//
// This is not a style rule. A nav taller than the viewport gets its own scroll
// container, and that scroll position is state a reader cannot see: every item
// in the list looks like every other one, and a page load resets it to the top.
// Measured in the browser at 1440x900 before this was fixed:
//
//	content 1244, box 900, over by 344
//	aiming at Teams after the panel had been scrolled  99px hit Crew
//	                                                  132px hit Sprints
//	                                                  132px could hit a group
//	                                                        label, where a
//	                                                        click does nothing
//
// Crew and Sprints are the two Yurii named, unprompted, on 2026-08-24.
//
// @measured, Chrome at 1440x1020, 2026-08-24: content 936, slack 84.
// The same measurement at 900px of viewport is 36px SHORT, so the trap returns
// on a window under about 940px tall. That is the honest limit of this fix; the
// structural answer is fewer than 26 destinations, which is not my call.
//
// The test holds the INPUTS that produced 936, because Go cannot lay out CSS.
// It is therefore a budget, not a rendering: it catches the regression that
// actually happens, which is a link being added or a padding growing back.
func TestTheSidebarFitsAWindow(t *testing.T) {
	css := read(t, "assets/app.css")
	layout := read(t, "templates/layout.html")

	nav := between(t, layout, "<nav>", "</nav>")
	links := strings.Count(nav, "<a href=")
	groups := strings.Count(nav, `<div class="group">`)

	// Everything below was measured together. Changing one without re-measuring
	// is how "it fits" starts hiding a number again.
	for _, c := range []struct {
		what  string
		got   int
		limit int
	}{
		{"links in the sidebar", links, 26},
		{"group labels", groups, 6},
		{"nav a padding-top", pxIn(t, css, `nav a \{[^}]*padding: (\d+)px`), 3},
		{"nav .group padding-top", pxIn(t, css, `nav \.group \{[^}]*padding: (\d+)px`), 9},
		{"nav padding-top", pxIn(t, css, `\nnav \{[^}]*padding: (\d+)px`), 12},
	} {
		if c.got > c.limit {
			t.Errorf("%s is %d, budget %d: the sidebar was measured at 936px "+
				"with these values and a 1020px window has 84px of slack. Over "+
				"the budget it grows its own scrollbar, which resets on every "+
				"page load and puts a different link under the cursor",
				c.what, c.got, c.limit)
		}
	}
}

// A window shorter than the sidebar must not give the sidebar its own scroll.
//
// The budget above only helps a window tall enough to hold 936px. Yurii hit the
// same defect a second time after that fix, on a shorter window, and described
// it exactly: "якщо я натискаю на якусь одну вкладку, відкривається інша, я на
// ту саму ще раз натискаю, відкривається ще інша."
//
// Pinning the panel means height:100vh and overflow-y:auto, and on a short
// window that is a scroll container whose position nobody can see and which a
// page load resets. Below the breakpoint the panel goes back into the flow: it
// can still be too tall, and then it scrolls WITH the page, which is a scroll
// the reader can see and the browser resets identically every time.
//
// @measured 2026-08-24, Chrome at 1440x800 and 1280x700, page at three scroll
// positions each: the panel cannot scroll internally, and every visible link
// hits itself. Before this, at 1440x900, aiming at Teams hit Crew or Sprints.
//
// A sidebar you have to scroll up to reach is a nuisance. A sidebar that opens
// the wrong page is a defect.
func TestAShortWindowUnpinsTheSidebar(t *testing.T) {
	css := read(t, "assets/app.css")

	rule := regexp.MustCompile(`(?s)@media \(max-height: (\d+)px\) \{(.*?)\n\}`).
		FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("no max-height rule: on a window shorter than the sidebar, the " +
			"sidebar keeps its own scrollbar, and that scrollbar puts a " +
			"different link under the cursor than the one that was there")
	}
	at, body := rule[1], rule[2]

	if !strings.Contains(body, "position: static") {
		t.Errorf("the max-height rule does not unpin the sidebar: %q", body)
	}
	if !strings.Contains(body, "height: auto") {
		t.Errorf("the max-height rule leaves height:100vh, which is what makes " +
			"the panel a scroll container")
	}
	if !strings.Contains(body, "overflow-y: visible") {
		t.Errorf("the max-height rule leaves overflow-y:auto, so the panel can " +
			"still scroll inside itself")
	}
	// The breakpoint has to clear the measured 936px panel, with room for a
	// browser that rounds differently.
	if n := atoi(t, at); n < 936 {
		t.Errorf("the breakpoint is %dpx and the panel is 936px: between those "+
			"two the panel is pinned AND too tall, which is the exact case this "+
			"exists to remove", n)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func between(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	j := strings.Index(s, to)
	if i < 0 || j < i {
		t.Fatalf("no %s ... %s in the layout", from, to)
	}
	return s[i:j]
}

func pxIn(t *testing.T, css, pattern string) int {
	t.Helper()
	m := regexp.MustCompile(`(?s)` + pattern).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("no rule matching %s: the sidebar budget is measuring nothing", pattern)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}
