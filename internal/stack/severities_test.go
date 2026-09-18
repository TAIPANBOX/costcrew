package stack_test

// Every emit call site's SEVERITY, held to the shared envelope's closed enum
// by handing it to the real emitter.
//
// TestEverySeverityThisConsoleEmitsIsOneTheEnvelopeAllows (stack_test.go)
// pins the guard: a severity outside the five is refused. It says nothing
// about what the call sites actually pass, and one of them passed "" from
// the day it was written: generated_estate_replaced, journaled inside the
// FOCUS reader's own transaction, so the guard refused the line, the reader
// returned the refusal, and the whole replacement rolled back. Measured on
// the appliance proving run of 2026-09-17 (costcrew#66): a real FOCUS export
// could not land on the board at v0.2.0, and the console kept its 19550
// generated charges. Every unit test handed that reader the hash chain,
// which tolerates an empty severity; only the stack emitter refuses one, and
// no test had put the two together.
//
// So this walks every emit call site the way wiretypes_test.go already walks
// them for the KIND, resolves the severity each one passes, and hands every
// (kind, severity) pair to a real Emitter. A pair the emitter refuses is the
// appliance failure, found here instead of there. A declared wire type no
// call site was found for is a failure too, because this then measured
// nothing about it; and a severity this walk cannot resolve is a failure
// rather than a silent omission, the same posture the kind walk takes for a
// kind it cannot resolve.
//
// go/ast rather than a regular expression, for the reason invariant 49's own
// history gives: a string count is defeated by the first rewrite that keeps
// the words and moves the shape. The walk is still a reading of today's
// source, not a compiler-enforced guarantee; it resolves the shapes this
// repository actually uses (a literal, a local variable assigned literals, a
// helper's parameter through its callers, a package-level function's own
// returns, a typed constant set) and refuses anything else by name.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	asgevent "github.com/TAIPANBOX/agent-stack-go/event"

	"github.com/TAIPANBOX/costcrew/internal/stack"
)

// emitSite is one place a kind and a severity meet an emitter.
type emitSite struct {
	where string
	kinds []string
	sevs  []string
}

func TestEveryWireTypeIsEmittedWithASeverityTheEnvelopeAccepts(t *testing.T) {
	root := repoRoot(t)
	sites := emitSites(t, root)
	if len(sites) == 0 {
		t.Fatal("no emit call site was read, so this test measured nothing")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "costcrew.ndjson")
	em, err := stack.Open(stack.Config{EventsPath: path, Host: "costcrew.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer em.Close()

	// Every pair through the real guard, not a copy of its table.
	covered := map[string]bool{}
	for _, s := range sites {
		for _, kind := range s.kinds {
			covered[stack.WireTypeOf(kind)] = true
			for _, sev := range s.sevs {
				if err := em.Emit(kind, "walker", sev, map[string]any{"anomaly": "A-1"}, nil); err != nil {
					t.Errorf("%s emits %q with severity %q, which the emitter refuses: %v\n"+
						"a consumer that validates refuses the whole line, and the caller "+
						"that journals it inside a transaction rolls the transaction back",
						s.where, kind, sev, err)
				}
			}
		}
	}

	// Every declared wire type has a call site this walk resolved; otherwise
	// the declaration carries a type this test measured nothing about.
	for _, wire := range stack.WireTypes() {
		if !covered[wire] {
			t.Errorf("WireTypes declares %q and no emit call site resolved to it, "+
				"so nothing here checked the severity it goes out with", wire)
		}
	}

	// And read the file back through the contract, so the check above is
	// not the emitter agreeing with itself.
	if err := em.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		asgevent.SeverityInfo: true, asgevent.SeverityLow: true, asgevent.SeverityMedium: true,
		asgevent.SeverityHigh: true, asgevent.SeverityCritical: true,
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("nothing reached the stream, so nothing was checked")
	}
	for i, line := range lines {
		ev, err := asgevent.Unmarshal([]byte(line))
		if err != nil {
			t.Fatalf("line %d: the contract refused it: %v", i+1, err)
		}
		if !allowed[ev.Severity] {
			t.Errorf("line %d (%s) went out with severity %q", i+1, ev.Type, ev.Severity)
		}
	}
}

// ----------------------------------------------------------------- the walk

// pkgSource is one directory's non-test files, with what a resolver needs
// looked up by name: its plain functions and its typed string constants.
type pkgSource struct {
	files  []*ast.File
	funcs  map[string]*ast.FuncDecl // plain functions, by name
	consts map[string]string        // package-level string constants, by name
	typed  map[string][]string      // typed string constants, by type name
}

func emitSites(t *testing.T, root string) []emitSite {
	t.Helper()
	fset := token.NewFileSet()
	pkgs := map[string]*pkgSource{}
	for _, top := range []string{"internal", "tools", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			dir := filepath.Dir(p)
			ps, ok := pkgs[dir]
			if !ok {
				ps = &pkgSource{funcs: map[string]*ast.FuncDecl{}, consts: map[string]string{}, typed: map[string][]string{}}
				pkgs[dir] = ps
			}
			ps.files = append(ps.files, f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, ps := range pkgs {
		ps.index()
	}

	var sites []emitSite
	for _, ps := range pkgs {
		for _, f := range ps.files {
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				// A recorder forwarding to its members (store.Tee) is not a
				// call site: its kind and severity are whatever its own caller
				// passed, and that caller is walked on its own.
				if fd.Name.Name == "Emit" {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					c, ok := n.(*ast.CallExpr)
					if !ok || calleeName(c.Fun) != "Emit" || len(c.Args) != 5 {
						return true
					}
					where := fset.Position(c.Pos()).String()
					r := &resolver{t: t, ps: ps, where: where}
					sites = append(sites, emitSite{
						where: strings.TrimPrefix(where, root+string(filepath.Separator)),
						kinds: r.resolve(c.Args[0], fd, 0),
						sevs:  r.resolve(c.Args[2], fd, 0),
					})
					return true
				})
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].where < sites[j].where })
	return sites
}

func (ps *pkgSource) index() {
	for _, f := range ps.files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					ps.funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				if d.Tok != token.CONST {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					typeName := ""
					if id, ok := vs.Type.(*ast.Ident); ok {
						typeName = id.Name
					}
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							continue
						}
						ps.consts[name.Name] = v
						if typeName != "" {
							ps.typed[typeName] = append(ps.typed[typeName], v)
						}
					}
				}
			}
		}
	}
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// resolver turns one argument expression into every string value it can
// take, reading only this package's own source, and fails the test by name
// on a shape it does not know rather than guessing at it.
type resolver struct {
	t     *testing.T
	ps    *pkgSource
	where string
}

const resolveDepth = 6

func (r *resolver) fail(format string, args ...any) []string {
	r.t.Helper()
	r.t.Fatalf("%s: %s\nresolve it here or change the call site, because an unresolved "+
		"severity reaches the bus unchecked", r.where, fmt.Sprintf(format, args...))
	return nil
}

func (r *resolver) resolve(e ast.Expr, scope *ast.FuncDecl, depth int) []string {
	r.t.Helper()
	if depth > resolveDepth {
		return r.fail("resolution went %d levels deep and stopped", depth)
	}
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return r.fail("a %s literal where a string belongs", x.Kind)
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return r.fail("unquoting %s: %v", x.Value, err)
		}
		return []string{v}
	case *ast.ParenExpr:
		return r.resolve(x.X, scope, depth)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return r.fail("a %s expression where a string belongs", x.Op)
		}
		var out []string
		for _, l := range r.resolve(x.X, scope, depth) {
			for _, rr := range r.resolve(x.Y, scope, depth) {
				out = append(out, l+rr)
			}
		}
		return out
	case *ast.CallExpr:
		return r.resolveCall(x, scope, depth)
	case *ast.Ident:
		return r.resolveIdent(x, scope, depth)
	}
	return r.fail("a %T expression this walk does not read", e)
}

func (r *resolver) resolveCall(c *ast.CallExpr, scope *ast.FuncDecl, depth int) []string {
	r.t.Helper()
	// string(x): a conversion of a named string type, read as every constant
	// of that type this package declares.
	if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "string" && len(c.Args) == 1 {
		return r.resolve(c.Args[0], scope, depth)
	}
	// A package-level function of this package: every value it returns.
	if id, ok := c.Fun.(*ast.Ident); ok {
		fd, ok := r.ps.funcs[id.Name]
		if !ok || fd.Body == nil {
			return r.fail("a call to %s, which is not a plain function of this package", id.Name)
		}
		var out []string
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			if len(ret.Results) != 1 {
				r.fail("%s returns %d values and this walk reads one", id.Name, len(ret.Results))
			}
			out = append(out, r.resolve(ret.Results[0], fd, depth+1)...)
			return true
		})
		if len(out) == 0 {
			return r.fail("%s returns nothing this walk could read", id.Name)
		}
		return out
	}
	return r.fail("a call through %s, which this walk does not follow into another package",
		calleeName(c.Fun))
}

func (r *resolver) resolveIdent(id *ast.Ident, scope *ast.FuncDecl, depth int) []string {
	r.t.Helper()
	// A package-level string constant.
	if v, ok := r.ps.consts[id.Name]; ok {
		return []string{v}
	}
	// A parameter: a typed constant set when its type has one, otherwise
	// whatever every caller in this package passes for it.
	if idx, typ, ok := paramOf(scope, id.Name); ok {
		if vals, ok := r.ps.typed[typ]; ok {
			return vals
		}
		if scope.Recv != nil {
			return r.fail("%s is a parameter of the method %s, whose callers this walk does not follow",
				id.Name, scope.Name.Name)
		}
		var out []string
		found := 0
		for _, f := range r.ps.files {
			for _, decl := range f.Decls {
				caller, ok := decl.(*ast.FuncDecl)
				if !ok || caller.Body == nil {
					continue
				}
				ast.Inspect(caller.Body, func(n ast.Node) bool {
					c, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if fn, ok := c.Fun.(*ast.Ident); !ok || fn.Name != scope.Name.Name || idx >= len(c.Args) {
						return true
					}
					found++
					out = append(out, r.resolve(c.Args[idx], caller, depth+1)...)
					return true
				})
			}
		}
		if found == 0 {
			return r.fail("%s is a parameter of %s and nothing in this package calls it",
				id.Name, scope.Name.Name)
		}
		return out
	}
	// A local variable: every value it is assigned in this function.
	var out []string
	assigned := 0
	ast.Inspect(scope.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				l, ok := lhs.(*ast.Ident)
				if !ok || l.Name != id.Name {
					continue
				}
				if len(s.Rhs) != len(s.Lhs) {
					r.fail("%s is assigned from a multi-value expression this walk does not read", id.Name)
				}
				assigned++
				out = append(out, r.resolve(s.Rhs[i], scope, depth+1)...)
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if name.Name != id.Name || i >= len(s.Values) {
					continue
				}
				assigned++
				out = append(out, r.resolve(s.Values[i], scope, depth+1)...)
			}
		}
		return true
	})
	if assigned == 0 {
		return r.fail("%s is neither a constant, a parameter nor assigned in %s", id.Name, scope.Name.Name)
	}
	return out
}

// paramOf reports the position and type name of a parameter of fd, when
// name is one; the type name is empty for a type that is not a bare
// identifier.
func paramOf(fd *ast.FuncDecl, name string) (int, string, bool) {
	idx := 0
	for _, field := range fd.Type.Params.List {
		typ := ""
		if id, ok := field.Type.(*ast.Ident); ok {
			typ = id.Name
		}
		if len(field.Names) == 0 {
			idx++ // an unnamed parameter still takes a position
			continue
		}
		for _, n := range field.Names {
			if n.Name == name {
				return idx, typ, true
			}
			idx++
		}
	}
	return 0, "", false
}
