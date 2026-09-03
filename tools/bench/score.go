package main

// Scoring: parse a deliverable, then check it against the truth.
// B7-SPEC.md section 2, steps 4 and 5.

import (
	"regexp"
	"strings"
	"time"

	"github.com/TAIPANBOX/costcrew/internal/anomaly"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/world"
)

// score is one case's result: the four booleans, what was actually named,
// and the call's cost.
type score struct {
	ServiceNamed bool
	DayNamed     bool
	KindRight    bool
	CauseMatched bool
	NamedCause   string
	CostMicros   int64
}

// trueKindFor looks up the registry entry that produced an anomaly's own
// Driver label, and reads its Kind. Matched by label rather than by
// service/day: the anomaly already carries the label as its truth, and a
// second, independent walk of Covers()-style date arithmetic would only be
// another way to get the same fact, less directly.
func trueKindFor(drivers []world.Driver, label string) (kind string, ok bool) {
	for _, d := range drivers {
		if d.Label == label {
			return d.Kind, true
		}
	}
	return "", false
}

// causeWord finds "cause" as a whole word, case-insensitively, so
// "because" is never mistaken for it.
var causeWord = regexp.MustCompile(`(?i)\bcause\b`)

// sentenceEnd is where a sentence carrying the word "cause" is taken to
// stop: the next full stop, exclamation, question mark, or line break.
var sentenceEnd = regexp.MustCompile(`[.!?\n]`)

// namedCause extracts what a deliverable names as the cause, in the order
// B7-SPEC.md section 2 step 4 gives: the summary of the first
// anomaly.explain option, else the sentence after "cause" in the prose,
// else "none established" if those exact words appear, else empty.
//
// @claude, and coarse: a model's prose does not owe this function a
// grammar, and both fallbacks are heuristics over free text. See the
// report's NOT proven line.
func namedCause(body string) string {
	if opts, found, reason := crew.ParseOptions(body); found && reason == "" {
		for _, o := range opts {
			if o.Class == "anomaly.explain" {
				return strings.TrimSpace(o.Summary)
			}
		}
	}
	if loc := causeWord.FindStringIndex(body); loc != nil {
		rest := body[loc[1]:]
		if end := sentenceEnd.FindStringIndex(rest); end != nil {
			rest = rest[:end[0]]
		}
		rest = strings.TrimSpace(rest)
		rest = strings.TrimLeft(rest, ":-–— \t")
		rest = strings.TrimSpace(rest)
		if rest != "" {
			return rest
		}
	}
	if strings.Contains(strings.ToLower(body), "none established") {
		return "none established"
	}
	return ""
}

// wordRE splits a driver label into the words causeMatches judges.
var wordRE = regexp.MustCompile(`[A-Za-z0-9]+`)

// causeMatches is B7-SPEC.md section 2 step 5's rule: every word of the
// driver label longer than three letters appears in the named cause,
// case-insensitively. A three-letter word ("and", "the") never has to
// match on its own; that is what keeps a label like "Month-end batch on
// the storage array" from being failed by an analyst who dropped "the".
func causeMatches(label, named string) bool {
	if strings.TrimSpace(named) == "" {
		return false
	}
	namedLower := strings.ToLower(named)
	for _, w := range wordRE.FindAllString(label, -1) {
		if len(w) <= 3 {
			continue
		}
		if !strings.Contains(namedLower, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

// dayFormats are the renderings dayNamed accepts beyond the packet's own
// ISO form, so an analyst who writes "22 June 2026" is not scored wrong for
// a date it plainly got right.
var dayFormats = []string{"January 2, 2006", "2 January 2006"}

func dayNamed(body, day string) bool {
	if strings.Contains(body, day) {
		return true
	}
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return false
	}
	for _, f := range dayFormats {
		if strings.Contains(body, t.Format(f)) {
			return true
		}
	}
	return false
}

// kindRight prefers the structured signal (a driver.one-time or
// driver.recurring option class) over prose, the same "option class or the
// prose" order B7-SPEC.md section 2 step 5 gives.
func kindRight(body string, opts []crew.Option, trueKind string) bool {
	for _, o := range opts {
		switch o.Class {
		case "driver.one-time":
			return trueKind == "one-time"
		case "driver.recurring":
			return trueKind == "recurring"
		}
	}
	lower := strings.ToLower(body)
	switch trueKind {
	case "one-time":
		return strings.Contains(lower, "one-time") || strings.Contains(lower, "one time")
	case "recurring":
		return strings.Contains(lower, "recurring")
	}
	return false
}

// scoreDeliverable is the whole of section 2 steps 4 and 5 over one
// deliverable, given the case it answers and what it truly cost.
func scoreDeliverable(an anomaly.Anomaly, trueKind string, body string, costMicros int64) score {
	opts, _, _ := crew.ParseOptions(body)
	named := namedCause(body)
	return score{
		ServiceNamed: strings.Contains(body, an.Service),
		DayNamed:     dayNamed(body, an.Day),
		KindRight:    kindRight(body, opts, trueKind),
		CauseMatched: causeMatches(an.Driver, named),
		NamedCause:   named,
		CostMicros:   costMicros,
	}
}
