package promptfixture

import "testing"

// Holds decides whether an identifier is in a prompt. A miss is a leak the
// gate does not see, and a false hit is a gate that fails on text that is fine,
// so both directions are pinned here.
func TestHoldsFindsAWholeWordAndNotAPartOfALongerOne(t *testing.T) {
	cases := []struct {
		name, text, id string
		want           bool
	}{
		{"whole word between spaces", "the alpha team rose", "alpha", true},
		{"whole text", "alpha", "alpha", true},
		{"at the end of the text", "team alpha", "alpha", true},
		{"inside a longer word", "alphabet", "alpha", false},
		{"preceded by a longer word", "xalpha", "alpha", false},
		{"first hit inside a longer word, second whole", "xalpha alpha", "alpha", true},
		{"digit before", "3alpha", "alpha", false},
		{"digit after", "alpha2", "alpha", false},
		{"underscore is not a word character", "alpha_1", "alpha", true},
		{"a letter from another alphabet before", "Ωalpha", "alpha", false},
		{"a letter from another alphabet after", "alphaΩ", "alpha", false},
		{"not present at all", "nothing here", "absent", false},
		{"empty text", "", "alpha", false},
		{"short identifier inside a longer word", "xml", "ml", false},
		{"short identifier as a whole word", "use ml now", "ml", true},
		// Six or more bytes: the plain substring rule, so the neighbours do not matter.
		{"long identifier inside a longer word", "subml-platformed", "ml-platform", true},
		{"long identifier next to a letter", "preml-platform", "ml-platform", true},
		{"long identifier absent", "ml-platfor", "ml-platform", false},
	}
	for _, c := range cases {
		if got := Holds(c.text, c.id); got != c.want {
			t.Errorf("%s: Holds(%q, %q) = %v, want %v", c.name, c.text, c.id, got, c.want)
		}
	}
}

// The boundary test must look at the rune before and after the identifier,
// not at a byte: the last byte of a two-byte Cyrillic letter is not an ASCII
// letter, so a byte-based check would call the identifier a whole word after it.
func TestHoldsReadsARuneNotAByte(t *testing.T) {
	if Holds("\u0411alpha", "alpha") {
		t.Error("a Cyrillic letter before the identifier should make it part of a longer word")
	}
	if !Holds("\u0411 alpha", "alpha") {
		t.Error("a Cyrillic word, then a space, then the identifier should be a whole-word hit")
	}
}
