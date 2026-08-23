package web

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/estate"
	"github.com/TAIPANBOX/costcrew/internal/finops"
)

var tplIntake = page("intake.html")

// Where a number that came from a meeting gets in.
//
// Every other figure in this console is read off a bill. A budget is a
// decision somebody made, and until now the console handed out a template for
// one and had nowhere to take it back: a download that leads nowhere, which is
// worse than none, because somebody fills it in.
//
// Two steps, and the same reason the outbound push has them: this writes the
// figure a team is measured against. The file is read whole, every row is
// checked, the difference is shown with a fingerprint, and nothing is written
// until that fingerprint comes back.

const maxIntake = 2 << 20 // 2 MB, which is tens of thousands of team-months

func (s *Server) intakePage(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	s.renderIntake(w, r, u.May("operator"), estate.BudgetPlan{}, "", "")
}

func (s *Server) renderIntake(w http.ResponseWriter, r *http.Request, canAct bool,
	plan estate.BudgetPlan, fp, note string) {

	s.render(w, tplIntake, struct {
		shell
		Plan   estate.BudgetPlan
		FP     string
		Note   string
		CanAct bool
	}{s.shellFor(r, "Intake", "intake"), plan, fp, note, canAct})
}

// intakeCheck reads the file and shows what it would do. It writes nothing.
func (s *Server) intakeCheck(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	// The multipart body is parsed BEFORE the CSRF check, because ParseForm
	// does not read one: with a file attached, PostFormValue finds no token
	// and every upload is refused with "reload the page and try again", which
	// is the least useful sentence available for the actual cause.
	body, err := readUpload(r)
	if !s.checked(w, r, "/intake", u) {
		return
	}
	if !u.May("operator") {
		redirectMsg(w, r, "/intake", "your account may read and export, but not set a budget")
		return
	}
	if err != nil {
		redirectMsg(w, r, "/intake", err.Error())
		return
	}
	plan, err := s.planBudgets(body)
	if err != nil {
		redirectMsg(w, r, "/intake", err.Error())
		return
	}
	// The file is held in the form, not on the server: a half-finished upload
	// that lives in a temp directory until somebody remembers it is a file
	// nobody owns. It is small, and the browser already has it.
	s.renderIntake(w, r, true, plan, plan.Fingerprint(), string(body))
}

// intakeApply writes exactly the plan that was shown.
func (s *Server) intakeApply(w http.ResponseWriter, r *http.Request) {
	u := s.guard(w, r)
	if u == nil {
		return
	}
	if !s.checked(w, r, "/intake", u) {
		return
	}
	if !u.May("operator") {
		redirectMsg(w, r, "/intake", "your account may read and export, but not set a budget")
		return
	}
	raw := r.PostFormValue("file")
	expect := r.PostFormValue("fingerprint")
	plan, err := s.planBudgets([]byte(raw))
	if err != nil {
		redirectMsg(w, r, "/intake", err.Error())
		return
	}
	n, err := estate.ApplyBudgets(s.db, plan, expect)
	if err != nil {
		redirectMsg(w, r, "/intake", err.Error())
		return
	}
	if s.rec != nil {
		_ = s.rec.Emit("budgets_set", "chargeback", "info", map[string]any{
			"rows": n, "by": u.Username, "lowered": plan.Lowered,
			"in_closed_months": plan.InClosed,
		}, s.delegation(u.Username, "chargeback"))
	}
	redirectMsg(w, r, "/budgets", plural(n, "one budget was set", "budgets were set")+
		" from the file you checked.")
}

func (s *Server) planBudgets(body []byte) (estate.BudgetPlan, error) {
	current, err := estate.CurrentBudgets(s.db)
	if err != nil {
		return estate.BudgetPlan{}, err
	}
	// Which months have been charged. Changing a budget in one rewrites what a
	// team was told it owed, so the page marks those rows rather than
	// refusing: it is somebody's decision to make, not this console's, and it
	// should be made with the fact in front of them.
	closed := map[string]bool{}
	if periods, err := finops.FrozenPeriods(s.db); err == nil {
		for _, p := range periods {
			closed[p] = true
		}
	}
	return estate.ReadBudgets(bytes.NewReader(body), current, closed)
}

// readUpload takes the file out of the form, whether it arrived as an upload
// or pasted into the box.
//
// Both, because the file is three columns and a number: somebody with two rows
// to change should not have to make a file to do it.
func readUpload(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(maxIntake); err == nil {
		if f, _, err := r.FormFile("upload"); err == nil {
			defer f.Close()
			body, err := io.ReadAll(io.LimitReader(f, maxIntake))
			if err != nil {
				return nil, err
			}
			if len(bytes.TrimSpace(body)) > 0 {
				return body, nil
			}
		}
	}
	if pasted := strings.TrimSpace(r.PostFormValue("pasted")); pasted != "" {
		return []byte(pasted), nil
	}
	return nil, errNoFile
}

var errNoFile = &intakeError{"choose a file, or paste the rows into the box"}

type intakeError struct{ s string }

func (e *intakeError) Error() string { return e.s }
