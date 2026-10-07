package web_test

// Invariant 64: the console makes no outbound call of its own. While it serves
// a page it reaches nowhere; the one exception is POST /sprint/plan/ask, which
// calls a model through deliver.Call, and only when -gateway or -gateway-openai
// is set. README.md and the Dockerfile both say so, and until this file nothing
// checked the sentence: a handler that grew an http.Client would have made the
// documentation false without a test going red.
//
// This is a reading of today's source, the same limit invariants 49 and 50
// state for their own walks. It sees a construction written the ordinary way
// (an identifier selected from net/http or net). It does not see one built
// through reflection, through a function value handed in from elsewhere, or
// inside a third-party library the console imports; those are in the
// "NOT proven" list of the invariant.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/TAIPANBOX/costcrew"
const deliverPath = modulePath + "/internal/deliver"

// outboundSelectors are the identifiers whose mere appearance, as a selector on
// the package, is a way to reach the network. A type (http.Client, net.Dialer)
// counts as well as a call: a field of that type is a client waiting for a
// request, and a composite literal of it is the construction itself.
var outboundNetHTTP = map[string]bool{
	"Client": true, "Get": true, "Post": true, "PostForm": true, "Head": true,
	"DefaultClient": true, "NewRequest": true, "NewRequestWithContext": true,
	"Transport": true, "DefaultTransport": true,
}

func outboundNet(name string) bool {
	return strings.HasPrefix(name, "Dial") || name == "Dialer" ||
		strings.HasPrefix(name, "Lookup") || name == "Resolver"
}

// outboundImports are packages the console has no reason to import at all:
// net/smtp, net/rpc and httputil's reverse proxy are outbound by construction.
// os/exec is not here: internal/engines imports it for exec.LookPath, which
// only searches PATH. Running a process (the way to shell out to curl) is
// refused by name instead, below.
var outboundImports = map[string]bool{
	"net/smtp": true, "net/rpc": true, "net/http/httputil": true,
}

type finding struct {
	pos  token.Position
	what string
}

// scanFile returns every outbound construction in one parsed file.
func scanFile(fset *token.FileSet, f *ast.File) []finding {
	var out []finding
	local := map[string]string{} // local name -> import path, for the two we watch
	for _, im := range f.Imports {
		path, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			out = append(out, finding{fset.Position(im.Pos()), "an import path that does not parse"})
			continue
		}
		if outboundImports[path] {
			out = append(out, finding{fset.Position(im.Pos()), "import of " + path})
		}
		name := ""
		if im.Name != nil {
			name = im.Name.Name
		}
		switch path {
		case "net/http", "net", "os/exec":
			if name == "." {
				// Every identifier is then unqualified and this walk cannot
				// tell http.Get from a local Get. Refused by name, not skipped.
				out = append(out, finding{fset.Position(im.Pos()), "dot import of " + path + ", which this walk cannot read"})
				continue
			}
			if name == "_" {
				continue
			}
			if name == "" {
				name = filepath.Base(path)
			}
			local[name] = path
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch local[id.Name] {
		case "net/http":
			if outboundNetHTTP[sel.Sel.Name] {
				out = append(out, finding{fset.Position(sel.Pos()), "http." + sel.Sel.Name})
			}
		case "net":
			if outboundNet(sel.Sel.Name) {
				out = append(out, finding{fset.Position(sel.Pos()), "net." + sel.Sel.Name})
			}
		case "os/exec":
			if sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext" {
				out = append(out, finding{fset.Position(sel.Pos()), "exec." + sel.Sel.Name})
			}
		}
		return true
	})
	return out
}

// nonTestFiles parses every non-test .go file in dir.
func nonTestFiles(t *testing.T, dir string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files[name] = f
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no non-test Go file: the walk measured nothing", dir)
	}
	return fset, files
}

// ------------------------------------------------------------ the real source

func TestTheConsoleConstructsNoOutboundHTTPClientOrRequest(t *testing.T) {
	for _, dir := range []string{".", "../../cmd/costcrew"} {
		fset, files := nonTestFiles(t, dir)
		for name, f := range files {
			for _, fd := range scanFile(fset, f) {
				t.Errorf("%s: %s: the console reaches the network here. The one route is deliver.Call "+
					"(POST /sprint/plan/ask, behind -gateway), and README.md says so; a second one "+
					"makes that sentence false", name, fd.what)
			}
		}
	}
}

// deliver.Call is the one allowed route, and it is allowed in one place. Found
// in exactly one file, so a second handler that starts spending is a test
// failure that sends its author to the README sentence, not a quiet addition.
func TestDeliverCallIsReachedFromExactlyOnePlace(t *testing.T) {
	var where []string
	for _, dir := range []string{".", "../../cmd/costcrew"} {
		fset, files := nonTestFiles(t, dir)
		for name, f := range files {
			alias := ""
			for _, im := range f.Imports {
				if p, _ := strconv.Unquote(im.Path.Value); p == deliverPath {
					alias = "deliver"
					if im.Name != nil {
						alias = im.Name.Name
					}
				}
			}
			if alias == "" {
				continue
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == alias && sel.Sel.Name == "Call" {
						abs, _ := filepath.Abs(dir)
						where = append(where, filepath.Base(abs)+"/"+name+":"+strconv.Itoa(fset.Position(sel.Pos()).Line))
					}
				}
				return true
			})
		}
	}
	if len(where) != 1 || !strings.HasPrefix(where[0], "web/planning.go:") {
		t.Errorf("deliver.Call is reached from %v, want exactly one place, web/planning.go (POST /sprint/plan/ask)", where)
	}
}

// reachable returns the module packages (as directories relative to the module
// root) that the roots import, directly or not, outside test files.
func reachable(t *testing.T, roots ...string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		_, files := nonTestFiles(t, filepath.Join("../..", dir))
		for _, f := range files {
			for _, im := range f.Imports {
				p, _ := strconv.Unquote(im.Path.Value)
				if strings.HasPrefix(p, modulePath+"/") {
					queue = append(queue, strings.TrimPrefix(p, modulePath+"/"))
				}
			}
		}
	}
	return seen
}

// Among the module's own packages the console imports, only internal/deliver
// builds outbound requests. internal/enforce also does (it is the budget pusher
// behind tools/enforce) and README says the console never imports it.
func TestOnlyTheDeliveryPackageAmongThoseTheConsoleImportsReachesTheNetwork(t *testing.T) {
	reached := reachable(t, "internal/web", "cmd/costcrew")
	if !reached["internal/deliver"] || !reached["internal/store"] {
		t.Fatalf("the import walk did not reach internal/deliver and internal/store (%v): it measured nothing", reached)
	}
	var holders []string
	for dir := range reached {
		if dir == "internal/web" || dir == "cmd/costcrew" {
			continue // the two roots are held by the test above, file by file
		}
		fset, files := nonTestFiles(t, filepath.Join("../..", dir))
		for _, f := range files {
			if len(scanFile(fset, f)) > 0 {
				holders = append(holders, dir)
				break
			}
		}
	}
	sort.Strings(holders)
	if len(holders) != 1 || holders[0] != "internal/deliver" {
		t.Errorf("packages the console imports that build outbound requests: %v, want exactly [internal/deliver]", holders)
	}
	if reached["internal/enforce"] {
		t.Error("the console imports internal/enforce, which pushes budgets to another system")
	}
}

// ------------------------------------------------------------- the walk itself

func scanSnippet(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", "package p\n"+src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("snippet does not parse: %v\n%s", err, src)
	}
	var got []string
	for _, fd := range scanFile(fset, f) {
		got = append(got, fd.what)
	}
	return got
}

// Every way the sentence "no outbound client or request" can be broken, one
// snippet each, each required to be seen. The real source is clean, so the test
// above cannot go red on its own; these are what stop the walk from quietly
// matching nothing.
func TestTheEgressWalkSeesEveryConstructionItNames(t *testing.T) {
	cases := map[string]string{
		"http.Get":                   `import "net/http"; func f() { http.Get("x") }`,
		"http.Post":                  `import "net/http"; func f() { http.Post("x", "", nil) }`,
		"http.PostForm":              `import "net/http"; func f() { http.PostForm("x", nil) }`,
		"http.Head":                  `import "net/http"; func f() { http.Head("x") }`,
		"http.NewRequest":            `import "net/http"; func f() { http.NewRequest("GET", "x", nil) }`,
		"http.NewRequestWithContext": `import "net/http"; func f() { http.NewRequestWithContext(nil, "GET", "x", nil) }`,
		"http.DefaultClient":         `import "net/http"; func f() { http.DefaultClient.Do(nil) }`,
		"a client literal":           `import "net/http"; func f() { _ = &http.Client{} }`,
		"a client value literal":     `import "net/http"; var c = http.Client{}`,
		"a client typed field":       `import "net/http"; type s struct{ c *http.Client }`,
		"a transport":                `import "net/http"; var t = &http.Transport{}`,
		"the default transport":      `import "net/http"; var t = http.DefaultTransport`,
		"a method value":             `import "net/http"; var g = http.Get`,
		"net.Dial":                   `import "net"; func f() { net.Dial("tcp", "x") }`,
		"net.DialTimeout":            `import "net"; func f() { net.DialTimeout("tcp", "x", 0) }`,
		"net.DialTCP":                `import "net"; func f() { net.DialTCP("tcp", nil, nil) }`,
		"a dialer":                   `import "net"; var d = net.Dialer{}`,
		"net.LookupHost":             `import "net"; func f() { net.LookupHost("x") }`,
		"an aliased net/http":        `import h "net/http"; func f() { h.Get("x") }`,
		"an aliased net":             `import n "net"; func f() { n.Dial("tcp", "x") }`,
		"exec.Command":               `import "os/exec"; func f() { exec.Command("curl", "x") }`,
		"exec.CommandContext":        `import "os/exec"; func f() { exec.CommandContext(nil, "curl", "x") }`,
		"an aliased os/exec":         `import e "os/exec"; func f() { e.Command("curl") }`,
		"import of net/smtp":         `import _ "net/smtp"`,
		"import of httputil":         `import "net/http/httputil"; var _ = httputil.NewSingleHostReverseProxy`,
		"a dot import of net/http":   `import . "net/http"; func f() { Get("x") }`,
		"a dot import of net":        `import . "net"; func f() { Dial("tcp", "x") }`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scanSnippet(t, src); len(got) == 0 {
				t.Errorf("the walk saw nothing in:\n%s", src)
			}
		})
	}
}

// And what a server uses must not be refused, or the gate is deleted the
// first week: http.Handler, http.Error, http.Redirect, the status constants,
// net.SplitHostPort, a local variable that happens to be called Get.
func TestTheEgressWalkLeavesServerCodeAlone(t *testing.T) {
	cases := map[string]string{
		"handler types":        `import "net/http"; type s struct{ h http.Handler; w http.ResponseWriter; r *http.Request }`,
		"error and redirect":   `import "net/http"; func f(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusTeapot); http.Redirect(w, r, "/", http.StatusSeeOther) }`,
		"a mux":                `import "net/http"; var m = http.NewServeMux()`,
		"a cookie":             `import "net/http"; var c = http.Cookie{}; func f() { http.SetCookie(nil, &c) }`,
		"a server":             `import "net/http"; var s = &http.Server{}`,
		"header parsing":       `import "net"; func f() { net.SplitHostPort("a:1"); net.ParseIP("1.1.1.1") }`,
		"listening":            `import "net"; func f() { net.Listen("tcp", "127.0.0.1:0") }`,
		"looking up a command": `import "os/exec"; func f() { exec.LookPath("aws") }`,
		"a local Get":          `type c struct{}; func (c) Get() {}; func f() { var x c; x.Get() }`,
		"a local http":         `type h struct{ Client int }; func f() { var http h; _ = http.Client }`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if got := scanSnippet(t, src); len(got) != 0 {
				t.Errorf("the walk refused server code with %v in:\n%s", got, src)
			}
		})
	}
}
