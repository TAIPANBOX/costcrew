// The Dockerfile and components.json, held to the same answer about which
// binaries ship in the image.
//
// # WHY THIS EXISTS
//
// The image carried `costcrew` and `costcrew-run` and nothing else, while the
// documentation named `costcrew-enforce` and `costcrew-idryxsource` as part of
// closing the loop (costcrew#75). An appliance with no Go toolchain could not
// run either. The two lists, what the manifest says ships and what the
// Dockerfile copies, lived in different files and nothing compared them, so
// the gap was found by an operator on a clean machine and not by a gate.
//
// `checked.image` in components.json is the declaration, and this file is the
// comparison. It reads the Dockerfile as text, because that is the only thing
// that says what the image holds without building one (a build needs a
// daemon, and CI should not need one to notice a disagreement).
package manifest_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dockerfile is what the image build says, reduced to the three facts this
// gate compares.
type dockerfile struct {
	built  map[string]string // binary name -> package directory it is built from
	copied map[string]bool   // binaries copied into the runtime stage
	froms  []string          // the image reference of every FROM
}

var (
	reBuild = regexp.MustCompile(`-o\s+/out/(\S+)\s+\./(\S+)`)
	reCopy  = regexp.MustCompile(`(?m)^COPY\s+--from=build\s+/out/(\S+)\s+/usr/local/bin/(\S+)\s*$`)
	reFrom  = regexp.MustCompile(`(?m)^FROM\s+(?:--platform=\S+\s+)?(\S+)`)
)

// parseDockerfile joins backslash continuations first, so a `go build` split
// over two lines is read as the one command it is.
func parseDockerfile(src string) dockerfile {
	src = strings.ReplaceAll(src, "\\\n", " ")
	d := dockerfile{built: map[string]string{}, copied: map[string]bool{}}
	for _, m := range reBuild.FindAllStringSubmatch(src, -1) {
		d.built[m[1]] = m[2]
	}
	for _, m := range reCopy.FindAllStringSubmatch(src, -1) {
		if m[1] == m[2] { // /out/x -> /usr/local/bin/x, same name
			d.copied[m[1]] = true
		}
	}
	for _, m := range reFrom.FindAllStringSubmatch(src, -1) {
		d.froms = append(d.froms, m[1])
	}
	return d
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// disagreements returns one line per way the Dockerfile and the manifest differ
// about the image. Empty means they agree. `declared` maps binary name to the
// package directory components.json gives it, for the components marked
// `checked.image`.
func disagreements(d dockerfile, declared map[string]string) []string {
	var out []string
	if len(d.copied) == 0 && len(declared) == 0 {
		return []string{"neither the Dockerfile nor components.json names any image binary, so this measured nothing"}
	}
	for _, name := range keys(declared) {
		if !d.copied[name] {
			out = append(out, name+" is declared in the image (checked.image) and the Dockerfile does not COPY it into the runtime stage")
		}
	}
	for _, name := range keys(d.copied) {
		if _, ok := declared[name]; !ok {
			out = append(out, name+" is copied into the image and components.json does not mark it checked.image")
		}
	}
	for _, name := range keys(d.copied) {
		dir, built := d.built[name]
		if !built {
			out = append(out, name+" is copied into the image and no `go build -o /out/"+name+"` produces it")
			continue
		}
		if want, ok := declared[name]; ok && want != dir {
			out = append(out, name+" is built from ./"+dir+" and components.json says its package is ./"+want)
		}
	}
	for _, name := range keys(d.built) {
		if !d.copied[name] {
			out = append(out, name+" is built in the Dockerfile and never copied into the runtime stage")
		}
	}
	return out
}

// unpinned lists the FROM references that name no sha256 digest. A tag can be
// repointed under an operator without anyone choosing that; a digest cannot.
func unpinned(froms []string) []string {
	var out []string
	for _, f := range froms {
		if !strings.Contains(f, "@sha256:") {
			out = append(out, f)
		}
	}
	return out
}

func readDockerfile(t *testing.T) dockerfile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root(t), "Dockerfile"))
	if err != nil {
		t.Fatalf("Dockerfile: %v", err)
	}
	d := parseDockerfile(string(raw))
	if len(d.froms) == 0 {
		t.Fatal("the Dockerfile has no FROM line, so this test measured nothing")
	}
	return d
}

// declaredImage is the manifest's side: binary name -> package directory.
func declaredImage(t *testing.T) map[string]string {
	t.Helper()
	m, _ := load(t)
	const prefix = "github.com/TAIPANBOX/costcrew/"
	out := map[string]string{}
	for _, c := range m.Components {
		if !c.Checked.Image {
			continue
		}
		if !strings.HasPrefix(c.Checked.Package, prefix) {
			t.Fatalf("%s: package %q is not under %s", c.Name, c.Checked.Package, prefix)
		}
		out[c.Name] = strings.TrimPrefix(c.Checked.Package, prefix)
	}
	return out
}

// The real files. This is the gate: change the Dockerfile's binaries without
// the manifest, or the manifest without the Dockerfile, and it names which.
func TestTheDockerfileShipsExactlyTheBinariesTheManifestSaysItDoes(t *testing.T) {
	for _, line := range disagreements(readDockerfile(t), declaredImage(t)) {
		t.Error(line)
	}
}

// The ask in costcrew#75, stated as the names it gave: the launcher's compose
// file has to be able to run them from the image.
func TestTheImageCarriesTheEnforceAndIdryxSourceBinaries(t *testing.T) {
	d := readDockerfile(t)
	for _, name := range []string{"costcrew", "costcrew-run", "costcrew-enforce", "costcrew-idryxsource"} {
		if !d.copied[name] {
			t.Errorf("the image does not carry %s", name)
		}
	}
}

// Base images by digest. The tag stays in a comment beside the digest, where a
// reader and dependabot's docker ecosystem can see which release it is.
func TestEveryBaseImageIsPinnedByDigest(t *testing.T) {
	for _, f := range unpinned(readDockerfile(t).froms) {
		t.Errorf("FROM %s names no @sha256: digest", f)
	}
}

// The comparison itself, on Dockerfiles written for the purpose, so that a
// parser regression cannot make the real-file test pass by seeing nothing.
func TestTheDockerfileComparisonSeesEachWayTheyCanDisagree(t *testing.T) {
	const buildB = " \\\n && CGO_ENABLED=0 go build -trimpath -o /out/b ./tools/b"
	const copyB = "COPY --from=build /out/b /usr/local/bin/b\n"
	const good = `FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:aa AS build
RUN CGO_ENABLED=0 go build -trimpath -o /out/a ./cmd/a \
 && CGO_ENABLED=0 go build -trimpath -o /out/b ./tools/b
FROM gcr.io/distroless/static-debian12@sha256:bb
COPY --from=build /out/a /usr/local/bin/a
COPY --from=build /out/b /usr/local/bin/b
`
	both := map[string]string{"a": "cmd/a", "b": "tools/b"}
	cases := []struct {
		name     string
		src      string
		declared map[string]string
		want     string // substring of a disagreement; empty means none expected
	}{
		{"agree", good, both, ""},
		{"copy dropped", strings.Replace(good, copyB, "", 1), both,
			"b is declared in the image"},
		{"build and copy dropped", strings.Replace(strings.Replace(good, copyB, "", 1), buildB, "", 1), both,
			"b is declared in the image"},
		{"copied but not declared", good, map[string]string{"a": "cmd/a"},
			"b is copied into the image and components.json does not mark it"},
		{"copied but never built", strings.Replace(good, buildB, "", 1), both,
			"no `go build -o /out/b` produces it"},
		{"built but never copied", good + "RUN go build -o /out/c ./tools/c\n", both,
			"c is built in the Dockerfile and never copied"},
		{"copied under another name", strings.Replace(good, "/usr/local/bin/b", "/usr/local/bin/other", 1), both,
			"b is declared in the image"},
		{"built from another package", strings.Replace(good, "./tools/b", "./tools/z", 1), both,
			"b is built from ./tools/z"},
		{"nothing at all", "FROM scratch\n", map[string]string{}, "measured nothing"},
	}
	for _, c := range cases {
		got := disagreements(parseDockerfile(c.src), c.declared)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: expected agreement, got %v", c.name, got)
			}
			continue
		}
		found := false
		for _, line := range got {
			if strings.Contains(line, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected a disagreement containing %q, got %v", c.name, c.want, got)
		}
	}
}

func TestADigestPinIsRecognisedOnlyWhenItIsThere(t *testing.T) {
	got := unpinned(parseDockerfile("FROM golang:1.27-alpine AS build\nFROM gcr.io/distroless/static-debian12:nonroot\nFROM x@sha256:cc\n").froms)
	if len(got) != 2 || got[0] != "golang:1.27-alpine" || got[1] != "gcr.io/distroless/static-debian12:nonroot" {
		t.Errorf("unpinned = %v, want the two tag-only references", got)
	}
}
