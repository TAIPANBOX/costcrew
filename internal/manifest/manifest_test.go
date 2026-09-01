// The declaration in components.json, proved against this repository.
//
// Same division as TAIPANBOX/vouchryx, where this shape was worked out:
// estate-gates reads across repositories and cannot read source, so the
// `checked` bucket is asserted here, by the repository that has the toolchain.
// The `declared` bucket is asserted against nothing except the presence of its
// reason, deliberately.
//
// One difference from vouchryx and it is the one a launcher trips over: this
// console is configured by FLAGS. The two environment variables it reads are
// not how a deployment wires it, so `checked.flags` is the field that carries
// the surface, and it is proved against the flag.String call sites.
package manifest_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type manifest struct {
	Schema     string `json:"schema"`
	Components []struct {
		Name    string `json:"name"`
		Class   string `json:"class"`
		Checked struct {
			Package       string                             `json:"package"`
			ListenDefault string                             `json:"listen_default"`
			HealthPath    string                             `json:"health_path"`
			Env           map[string]struct{ Required bool } `json:"env"`
			Flags         map[string]struct{ Required bool } `json:"flags"`
		} `json:"checked"`
		Declared map[string]struct {
			Why string `json:"why"`
		} `json:"declared"`
	} `json:"components"`
}

func root(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no go.mod above the test's working directory")
	return ""
}

func load(t *testing.T) (manifest, string) {
	t.Helper()
	r := root(t)
	raw, err := os.ReadFile(filepath.Join(r, "components.json"))
	if err != nil {
		t.Fatalf("components.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("components.json is not JSON this reader understands: %v", err)
	}
	if len(m.Components) == 0 {
		t.Fatal("components.json declares nothing, so this test measured nothing")
	}
	return m, r
}

// A binary this repository builds and does not declare is invisible from
// outside by construction. Seven here, and six of them are tools no deployment
// installs, which is exactly why the estate's own registry cannot be the place
// this is written down.
func TestEveryBinaryThisRepositoryBuildsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)
	list := exec.Command("go", "list", "-f",
		`{{if eq .Name "main"}}{{.ImportPath}}{{end}}`, "./...")
	list.Dir = r
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	built := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			built[line] = true
		}
	}
	if len(built) == 0 {
		t.Fatal("go list found no main package, so this test measured nothing")
	}
	declared := map[string]bool{}
	for _, c := range m.Components {
		declared[c.Checked.Package] = true
	}
	for p := range built {
		if !declared[p] {
			t.Errorf("this repository builds %s and components.json does not declare it", p)
		}
	}
	for p := range declared {
		if !built[p] {
			t.Errorf("components.json declares %s and this repository does not build it", p)
		}
	}
}

// The console's flag surface, which is how a deployment configures it.
// Every binary's flag surface, which is how a deployment configures each of
// them, and NOT only the console's.
//
// It was only the console's until 2026-09-01, and the cost of that was found
// from outside: stack-k8s passes `-ceiling "$(COSTCREW_CEILING)"` to
// `costcrew-run`, the flag exists at tools/run/main.go, and this manifest
// declared no flags for that component at all. estate-gates' C16 saw an
// environment variable handed to a process with nothing in the estate
// declaring a reader, because the reader is a FLAG and the value reaches it by
// `$(VAR)` substitution, which no source-reading check can follow.
//
// The flag it could not see is the one with money behind it: `-live` refuses
// to run without `-ceiling`, and the ceiling is the only figure standing
// between a crew of agents and a provider account.
//
// The source directory comes from the DECLARED package path rather than from a
// list here. A tool added to tools/ and forgotten in components.json is already
// caught by the binary test above; this one must not need a second edit to
// cover it.
func TestEveryFlagEveryBinaryDefinesIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)
	const mod = "github.com/TAIPANBOX/costcrew/"
	re := regexp.MustCompile(`flag\.(?:String|Int|Bool|Duration|Float64)\("([a-z-]+)"`)

	measured := 0
	for _, c := range m.Components {
		pkg := c.Checked.Package
		if !strings.HasPrefix(pkg, mod) {
			t.Errorf("%s declares package %q, which is outside this module, so its flags cannot be read", c.Name, pkg)
			continue
		}
		dir := filepath.Join(r, filepath.FromSlash(strings.TrimPrefix(pkg, mod)))
		entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(entries) == 0 {
			// Not "this binary has no flags". This is "nothing was read", and
			// the two must never render the same, which is the whole reason
			// the console's version of this test carried a Fatal.
			t.Errorf("%s: no .go file under %s, so this measured NOTHING about its flags", c.Name, dir)
			continue
		}
		defined := map[string]bool{}
		for _, f := range entries {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, mm := range re.FindAllStringSubmatch(string(b), -1) {
				defined[mm[1]] = true
			}
		}
		measured++
		for f := range defined {
			if _, ok := c.Checked.Flags[f]; !ok {
				t.Errorf("%s defines -%s and components.json does not declare it", c.Name, f)
			}
		}
		for f := range c.Checked.Flags {
			if !defined[f] {
				t.Errorf("components.json declares -%s on %s and it defines no such flag", f, c.Name)
			}
		}
	}
	if measured == 0 {
		t.Fatal("no component's source was read, so this test measured nothing")
	}
}

func TestEveryEnvironmentVariableThisRepositoryReadsIsDeclaredAndTheReverse(t *testing.T) {
	m, r := load(t)
	name := regexp.MustCompile(`"(COSTCREW_[A-Z0-9_]+)"`)
	read := map[string]bool{}
	for _, dir := range []string{"cmd", "internal", "tools"} {
		_ = filepath.Walk(filepath.Join(r, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, mm := range name.FindAllStringSubmatch(string(b), -1) {
				read[mm[1]] = true
			}
			return nil
		})
	}
	if len(read) == 0 {
		t.Fatal("no COSTCREW_ variable was found, so this test measured nothing")
	}
	declared := map[string]bool{}
	for _, c := range m.Components {
		for k := range c.Checked.Env {
			declared[k] = true
		}
	}
	for v := range read {
		if !declared[v] {
			t.Errorf("this repository reads %s and components.json does not declare it", v)
		}
	}
	for v := range declared {
		if !read[v] {
			t.Errorf("components.json declares %s and nothing reads it", v)
		}
	}
}

func TestTheDeclaredListenDefaultIsTheOneTheConsoleUses(t *testing.T) {
	m, r := load(t)
	b, err := os.ReadFile(filepath.Join(r, "cmd", "costcrew", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	found := regexp.MustCompile(`flag\.String\("addr",\s*"([^"]+)"`).FindStringSubmatch(string(b))
	if found == nil {
		t.Fatal("the -addr flag no longer carries a literal default, so this test measured nothing")
	}
	if m.Components[0].Checked.ListenDefault != found[1] {
		t.Errorf("components.json says the console listens on %q; main.go says %q",
			m.Components[0].Checked.ListenDefault, found[1])
	}
}

// AND THE HALF NO CENTRAL FILE COULD DO: start it and ask.
//
// The declared health path must answer without a credential, because that is
// what a launcher polls and a launcher holds none. This console guards nearly
// every route behind a session; internal/web/guarded_test.go lists the public
// ones and /healthz is there with the reason. This proves the route is public
// against a RUNNING server rather than against that list.
func TestTheConsoleAnswersItsDeclaredHealthPathWithNoCredential(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	m, r := load(t)
	svc := m.Components[0]

	bin := filepath.Join(t.TempDir(), "console")
	build := exec.Command("go", "build", "-o", bin, svc.Checked.Package)
	build.Dir = r
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the declared package: %v\n%s", err, out)
	}

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + itoa(port)
	data := t.TempDir()
	cmd := exec.Command(bin, "-addr", addr, "-data", data)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	if !waitFor(addr, 60*time.Second) {
		t.Fatalf("the console never listened on %s", addr)
	}
	// A client that does NOT follow redirects, and that is the whole
	// difference between this test checking something and checking nothing.
	// Every guarded route here answers 303 to /login, and /login is public and
	// returns 200, so a following client turns "you may not be here" into
	// agreement. Measured: with http.Get, declaring `/board` as the health
	// path passed.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	url := "http://" + addr + svc.Checked.HealthPath
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET %s with no session answered %d; a declared health path a "+
			"launcher polls must answer without one", url, resp.StatusCode)
	}
}

func TestEveryDeclaredEntryCarriesItsReason(t *testing.T) {
	m, _ := load(t)
	for _, c := range m.Components {
		for k, v := range c.Declared {
			if strings.TrimSpace(v.Why) == "" {
				t.Errorf("%s: declared entry %q has no `why`", c.Name, k)
			}
		}
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func waitFor(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
