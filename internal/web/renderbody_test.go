package web

import (
	"strings"
	"testing"
)

// A deliverable must not show its own syntax to the reader.
//
// The fixture is REAL output, copied from a live run on 2026-08-24, because a
// fixture I write myself only proves I can describe the renderer I just wrote.
// The seeded drafts were written to match the old renderer, so it handled bold
// and "## " and everything agreed with itself; a model then wrote 44 of them
// and the page printed ###, ---, numbered lists and dashes back verbatim.
const liveDeliverable = "**Anomaly Triage Deliverable**  \n" +
	"**AWS Desk - FinOps Practice**  \n" +
	"**Date:** 2026-08-24  \n\n" +
	"---  \n\n" +
	// The heading is GLUED to the line under it by a single newline, and the
	// line ends in two spaces. My first fixture had a blank line there instead,
	// which is the shape I would have written, and the test passed while the
	// running page still printed "### Anomaly Summary" as text.
	"### **Anomaly Summary**  \n" +
	"**Observation:** On **2026-07-14**, EC2 spiked.\n\n" +
	"1. **What happened?** Identify the root cause.\n" +
	"2. **Will it recur?** Assess likelihood.\n\n" +
	"#### **1. Root Cause**  \n" +
	"- **Established Cause:** *None confirmed at this time.*\n" +
	"- **Autoscaling Misconfiguration:** the group over-provisioned.\n"

func TestADeliverableDoesNotShowItsOwnSyntax(t *testing.T) {
	got := string(renderBody(liveDeliverable))

	for _, leak := range []struct{ mark, why string }{
		{"###", "a heading printed as hashes"},
		{"---", "a rule printed as dashes"},
		{"\n- ", "a bullet printed as a dash"},
		{"1. **", "a numbered item printed with its number and its asterisks"},
		{"**", "bold printed as asterisks"},
	} {
		if strings.Contains(got, leak.mark) {
			t.Errorf("%s: %q is still in the output", leak.why, leak.mark)
		}
	}

	for _, want := range []struct{ frag, why string }{
		{"<hr>", "the rule became a rule"},
		{"<h4>", "the ### heading became a heading"},
		{"<ul>", "the list became a list"},
		{"<li>", "the items became items"},
		{"<strong>Observation:</strong>", "bold survived as bold"},
		{"<em>None confirmed at this time.</em>", "italic survived as italic"},
	} {
		if !strings.Contains(got, want.frag) {
			t.Errorf("%s: no %q in the output", want.why, want.frag)
		}
	}
}

// A model writes this page's content, so it must not be able to write its tags.
func TestADeliverableCannotPutATagOnThePage(t *testing.T) {
	got := string(renderBody(
		"<script>alert(1)</script>\n\n" +
			"### <img src=x onerror=alert(1)>\n\n" +
			"- <b>not bold</b>"))
	for _, tag := range []string{"<script", "<img", "<b>"} {
		if strings.Contains(got, tag) {
			t.Errorf("%q reached the page: the body is written by a model and "+
				"escaping is the only thing between it and the reader", tag)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Error("the script tag was not escaped, it was dropped: a reader should " +
			"see what the model wrote, harmlessly")
	}
}

// The seeded drafts must render exactly as they did before.
func TestTheSeededStyleStillRenders(t *testing.T) {
	got := string(renderBody("## A heading\n\n**Label.** Some prose.\n\nMore prose."))
	for _, want := range []string{"<h3>A heading</h3>", "<strong>Label.</strong>", "<p>More prose.</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q: the 279 seeded deliverables render through this too", want)
		}
	}
}
