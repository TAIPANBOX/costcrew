package web

import "github.com/TAIPANBOX/costcrew/internal/deliver"

// promptDataView is what /engines and /sprint/plan say about the
// installation's -prompt-data setting (invariant 70): the mode line the model
// itself is shown, and, when the mode restricts what is sent, that the text a
// person types is withheld. Until invariant 91 the setting was visible only in
// the console's start-up log, so a person typing a sprint goal had no way to
// know the supervisor would never read it.
type promptDataView struct {
	Mode     string // full, masked or aggregates
	Line     string // deliver.ActivePolicy().ModeLine(), word for word
	Withheld bool   // true under masked and aggregates: typed text is not sent
	Stand    string // what the model reads where the typed text would be
}

func currentPromptData() promptDataView {
	pol := deliver.ActivePolicy()
	return promptDataView{
		Mode:     string(pol.Mode()),
		Line:     pol.ModeLine(),
		Withheld: !pol.Full(),
		Stand:    deliver.WithheldFreeText,
	}
}
