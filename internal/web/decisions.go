package web

// The owner's answer. B3-SPEC.md section 5.
//
// `@yurii 2026-09-02`: "супервайзер питає власника тільки тоді, коли він сам
// не може вирішити це питання, тобто, що стосується безпосередньо взаємодії
// людей або прийняття якихось ключових рішень."

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/TAIPANBOX/costcrew/internal/auth"
	"github.com/TAIPANBOX/costcrew/internal/crew"
	"github.com/TAIPANBOX/costcrew/internal/finops"
	"github.com/TAIPANBOX/costcrew/internal/money"
)

var tplDecision = page("decision.html")

type decisionOptionView struct {
	optionView
	TaskID int
}

// decisionPage is one owner's decision request for one sprint: the
// narrative deliverable the supervisor's pass wrote, plus a live list of the
// options still carried (an option already answered drops off this list on
// its own, because it reads state='carried' rather than a frozen copy).
func (s *Server) decisionPage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	sprintID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		http.Error(w, "no such sprint", http.StatusNotFound)
		return
	}
	owner := r.PathValue("owner")

	artID, found, err := crew.DecisionRequestFor(s.db, sprintID, owner)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no decision request on file for that owner in that sprint", http.StatusNotFound)
		return
	}
	art, err := getArtifact(s.db, artID)
	if err != nil {
		http.Error(w, "no such deliverable", http.StatusNotFound)
		return
	}

	opts, err := crew.CarriedOptionsFor(s.db, sprintID, owner)
	if err != nil {
		http.Error(w, "store unavailable", http.StatusInternalServerError)
		return
	}
	rows := make([]decisionOptionView, 0, len(opts))
	for _, o := range opts {
		taskID, _ := crew.TaskOfArtifact(s.db, o.Artifact)
		rows = append(rows, decisionOptionView{
			// Window is carried through (driverWindow, work.go) so this
			// struct keeps compiling against optionView's own shape, even
			// though decision.html does not render it: DRIVER-WINDOW-SPEC.md
			// section 3 asks for the window on the TASK page alone.
			optionView{o, money.Cents(o.FigureCents), money.Cents(o.SavingCents), driverWindow(o)}, taskID,
		})
	}

	s.render(w, tplDecision, struct {
		shell
		Sprint    int
		Owner     string
		Body      template.HTML
		Options   []decisionOptionView
		CanAnswer bool
	}{s.shellFor(r, "Decision request", "sprints"), sprintID, owner,
		renderBody(art.Body), rows, mayAnswerFor(u, owner)})
}

// optionAction is the owner's stamp: apply calls finops.Apply as the acting
// owner, marking the option applied; refuse marks it refused with a reason.
// Nothing else calls finops.Apply from this package -- the analyst's own
// Post (work.go's artifactAction) stamps the DELIVERABLE and never touches
// an option at all, which is what "an analyst's Post applies nothing" still
// means with options in the picture.
func (s *Server) optionAction(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.guard(w, r)
		if u == nil {
			return
		}
		artID, aerr := strconv.Atoi(r.PathValue("artifact"))
		ordinal, oerr := strconv.Atoi(r.PathValue("ordinal"))

		// A best-effort redirect target, ignoring any lookup failure here:
		// the CSRF and role checks below must run whether or not the id in
		// the URL is real, the same reason artifactAction (work.go) builds
		// its own "back" this way. An unknown id becomes its own message
		// only AFTER s.checked, never before -- a 404 ahead of that check
		// would mean the lookup happened before the stranger was turned
		// away, which is exactly what concrete()'s own comment
		// (guarded_test.go) requires never happens.
		back, sprint, owner := "/board", 0, ""
		if aerr == nil {
			if taskID, terr := crew.TaskOfArtifact(s.db, artID); terr == nil {
				if t, gerr := crew.GetTask(s.db, taskID); gerr == nil {
					if o, oerr2 := crew.TaskOwner(s.db, taskID); oerr2 == nil && o != "" {
						sprint, owner = t.Sprint, o
						back = fmt.Sprintf("/sprint/%d/decisions/%s", sprint, owner)
					}
				}
			}
		}

		if !s.checked(w, r, back, u) {
			return
		}
		if aerr != nil || oerr != nil || owner == "" {
			redirectMsg(w, r, back, "no such option")
			return
		}
		if !mayAnswerFor(u, owner) {
			redirectMsg(w, r, back, "only "+owner+", who this decision request is "+
				"addressed to, or an admin, may answer it")
			return
		}

		opt, err := crew.GetOption(s.db, artID, ordinal)
		if err != nil {
			redirectMsg(w, r, back, "no such option")
			return
		}
		if opt.State != crew.OptionCarried {
			redirectMsg(w, r, back, "this option is "+string(opt.State)+", not carried: "+
				"it is not this decision request's to answer any more")
			return
		}

		if kind == "apply" {
			err = finops.Apply(s.db, opt, u.Username, s.rec)
		} else {
			err = crew.MarkOptionRefused(s.db, artID, ordinal, u.Username, r.PostFormValue("reason"))
		}
		if err != nil {
			s.done(w, r, back, err)
			return
		}
		if perr := crew.PostDecisionRequestIfComplete(s.db, sprint, owner); perr != nil {
			redirectMsg(w, r, back, "answered, but the request could not be closed out: "+perr.Error())
			return
		}
		redirectMsg(w, r, back, "")
	}
}

// mayAnswerFor is section 5's "only the owner's stamp": the same shape
// roster.go's mayManage already holds for an agent's own owner.
func mayAnswerFor(u *auth.User, owner string) bool {
	if u == nil {
		return false
	}
	if u.May("admin") {
		return true
	}
	return u.May("operator") && u.Username == owner
}

// getArtifact reads one deliverable by id. crew.Artifacts reads by TASK, not
// by artifact id, because every existing caller already has the task; this
// is the one caller that only has the artifact.
func getArtifact(db *sql.DB, id int) (crew.Artifact, error) {
	taskID, err := crew.TaskOfArtifact(db, id)
	if err != nil {
		return crew.Artifact{}, err
	}
	arts, err := crew.Artifacts(db, taskID)
	if err != nil {
		return crew.Artifact{}, err
	}
	for _, a := range arts {
		if a.ID == id {
			return a, nil
		}
	}
	return crew.Artifact{}, crew.ErrNotFound
}
