package tests

import (
	"bytes"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/lower"
)

// writeModule creates a module with the given files (path -> content).
func writeModule(t *testing.T, modPath string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	files["go.mod"] = "module " + modPath + "\n\ngo 1.26\n"
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func compileModule(t *testing.T, root string, inTree bool) *build.Session {
	t.Helper()
	cfg := &build.Config{WorkDir: root, InTree: inTree, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{})
	if s != nil && s.Sink.HasErrors() {
		t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPanicStackTraceShowsLythLines(t *testing.T) {
	root := writeModule(t, "trace", map[string]string{
		"app/main.lyth": `func pick(xs: []int, i: int): int {
    return xs[i]
}

func main() {
    xs := [1, 2, 3]
    println(pick(xs, 10))
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"trace/app"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected a panic")
	}
	out := stderr.String()
	for _, want := range []string{"index out of range", "app/main.lyth:2", "app/main.lyth:7"} {
		if !strings.Contains(out, want) {
			t.Errorf("stack trace does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".lyth.go:") {
		t.Errorf("stack trace mentions generated Go files:\n%s", out)
	}
}

func TestVetFindingsMapToLyth(t *testing.T) {
	root := writeModule(t, "vetmap", map[string]string{
		"main.lyth": `import "fmt"

func main() {
    name := "lyth"
    fmt.Printf("%d\n", name)
}
`,
	})
	s := compileModule(t, root, false)
	ok, err := s.GoVet([]string{"vetmap"})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected go vet to report the format mismatch")
	}
	found := false
	for _, d := range s.Sink.All() {
		if d.Code == diag.EGoCompile && d.Span.File != nil && strings.HasSuffix(d.Span.File.Path, "main.lyth") && d.Span.StartPos().Line == 5 {
			found = true
			if !strings.Contains(d.Message, "Printf") {
				t.Errorf("unexpected message: %s", d.Message)
			}
		}
	}
	if !found {
		t.Errorf("no vet diagnostic at main.lyth:5:\n%s", render(s.Sink.All()))
	}
}

func TestOverlayBuildLeavesSourceTreeUntouched(t *testing.T) {
	root := writeModule(t, "clean", map[string]string{
		"main.lyth":     "func main() { println(\"ok\") }\n",
		"util/str.lyth": "func Shout(s: string): string = \"${s}!\"\n",
	})
	s := compileModule(t, root, false)
	if ok, err := s.GoBuild(filepath.Join(root, ".lyth", "out"), []string{"clean"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var goFiles []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if d.IsDir() && d.Name() == ".lyth" {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, ".go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})
	if len(goFiles) > 0 {
		t.Errorf("overlay build wrote Go files into the source tree: %v", goFiles)
	}
}

func TestInTreeGenerationBuildsWithPlainGo(t *testing.T) {
	root := writeModule(t, "intree", map[string]string{
		"main.lyth": `import "intree/greet"

func main() {
    println(greet.Hello("go"))
}
`,
		"greet/greet.lyth": `func Hello(name: string): string = "hello, ${name}"
`,
	})
	s := compileModule(t, root, true)
	if _, err := s.WriteInTree(); err != nil {
		t.Fatal(err)
	}
	// No lyth involvement from here on: plain go build and run.
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "hello, go" {
		t.Errorf("unexpected output %q", out)
	}
	// Committed output uses bare file names in //line directives.
	data, _ := os.ReadFile(filepath.Join(root, "greet", "greet.lyth.go"))
	if !strings.Contains(string(data), "//line greet.lyth:1") || strings.Contains(string(data), root) {
		t.Errorf("in-tree output should use bare file names in //line directives:\n%s", data)
	}
}

func TestGeneratedFileNames(t *testing.T) {
	cases := map[string]string{
		"users.lyth":          "users.lyth.go",
		"users_test.lyth":     "users.lyth_test.go",
		"net_linux.lyth":      "net_linux.lyth.go",
		"net_linux_test.lyth": "net_linux.lyth_test.go",
	}
	for in, want := range cases {
		if got := lower.GoFileName(in); got != want {
			t.Errorf("GoFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestThirdPartyModule uses github.com/google/uuid: a named array type with
// methods, (T, error) parsing, package variables, and generic-free APIs.
// It resolves the module from the local module cache (GOPROXY=off) and is
// skipped when the module is not cached.
func TestThirdPartyModule(t *testing.T) {
	root := writeModule(t, "thirdparty", map[string]string{
		"main.lyth": `import "github.com/google/uuid"

func main() throws {
    id := uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")
    println(id.String(), id.Version())

    parsed := uuid.Parse("not-a-uuid") catch { uuid.Nil }
    println(parsed == uuid.Nil)

    named := uuid.NewSHA1(uuid.NameSpaceDNS, []byte("lyth.dev"))
    println(named.Version(), len(named.String()))

    strict := uuid.Parse("also-bad")?
    println(strict)
}
`,
	})
	// lyth mod tidy must add the requirement even though no .go file imports
	// the module (plain go mod tidy would not see the import).
	modCache, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	proxy := "GOPROXY=file://" + filepath.ToSlash(filepath.Join(strings.TrimSpace(string(modCache)), "cache", "download"))
	var tidyOut bytes.Buffer
	if _, err := build.ModTidy(&build.Config{WorkDir: root, Stdout: &tidyOut, Stderr: &tidyOut}, []string{proxy, "GOFLAGS=-mod=mod", "GOSUMDB=off"}); err != nil {
		t.Skipf("github.com/google/uuid not available offline: %v\n%s", err, tidyOut.String())
	}
	if data, _ := os.ReadFile(filepath.Join(root, "go.mod")); !strings.Contains(string(data), "github.com/google/uuid") {
		t.Fatalf("lyth mod tidy did not add the requirement:\n%s", data)
	}
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"thirdparty"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("expected exit status 1 from the propagated parse error, got %v\nstderr: %s", err, stderr.String())
	}
	want := "f47ac10b-58cc-4372-a567-0e02b2c3d479 VERSION_4\ntrue\nVERSION_5 36\n"
	if stdout.String() != want {
		t.Errorf("output %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "error: invalid UUID length") {
		t.Errorf("stderr %q does not report the uuid error", stderr.String())
	}
}

// TestExamples builds and runs every program under examples/.
// TestExamples checks every example. Single-file examples are built and
// run. An example with its own go.mod is an application module (the todo
// API needs PostgreSQL to run), so its test suite runs through GoTest, as
// lyth test would; database-backed tests skip unless DATABASE_URL is set.
func TestExamples(t *testing.T) {
	dirs, _ := filepath.Glob("../examples/*")
	if len(dirs) == 0 {
		t.Skip("no examples")
	}
	for _, dir := range dirs {
		dir, _ := filepath.Abs(dir)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			var out bytes.Buffer
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				cfg := &build.Config{WorkDir: dir, Tests: true, Stdout: &out, Stderr: &out}
				s, err := build.Compile(cfg, build.Target{Patterns: []string{"./..."}})
				if s != nil && s.Sink.HasErrors() {
					t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range s.Packages {
					for _, g := range p.Generated {
						if formatted, err := format.Source(g.Code); err != nil || !bytes.Equal(formatted, g.Code) {
							t.Errorf("%s/%s is not gofmt-clean (%v)", p.Dir.Rel, g.Name, err)
						}
					}
				}
				code, err := s.GoTest([]string{"./..."}, []string{"-count=1"})
				if err != nil || code != 0 {
					t.Fatalf("tests failed (exit %d): %v\n%s", code, err, out.String())
				}
				return
			}
			files, _ := filepath.Glob(filepath.Join(dir, "*.lyth"))
			for _, f := range files {
				wd, _ := os.Getwd()
				cfg := &build.Config{WorkDir: wd, Stdout: &out, Stderr: &out}
				s, err := build.Compile(cfg, build.Target{File: f})
				if s != nil && s.Sink.HasErrors() {
					t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
				}
				if err != nil {
					t.Fatal(err)
				}
				mains := s.MainPackages()
				if len(mains) != 1 {
					t.Fatalf("%s: want one main package, got %d", f, len(mains))
				}
				code, err := s.GoRun(mains[0].Dir.ImportPath, nil, nil)
				if err != nil || code != 0 {
					t.Fatalf("run failed (exit %d): %v\n%s\n%s", code, err, out.String(), render(s.Sink.All()))
				}
			}
		})
	}
}

// TestTargetedBuildKeepsOtherPackages checks that building one package does
// not drop the generated code of the module's other Lyth packages.
func TestTargetedBuildKeepsOtherPackages(t *testing.T) {
	root := writeModule(t, "targeted", map[string]string{
		"a/a.lyth": "func main() { println(\"a\") }\n",
		"b/b.lyth": "func main() { println(\"b\") }\n",
	})
	compileModule(t, root, false)
	cfg := &build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}
	s, err := build.Compile(cfg, build.Target{Patterns: []string{"./a"}})
	if err != nil || s.Sink.HasErrors() {
		t.Fatalf("targeted compile failed: %v\n%s", err, render(s.Sink.All()))
	}
	if len(s.Targets) != 1 || len(s.Packages) != 1 {
		t.Errorf("targeted compile should only compile ./a, got %d targets and %d packages", len(s.Targets), len(s.Packages))
	}
	data, _ := os.ReadFile(filepath.Join(root, ".lyth", "overlay.json"))
	for _, want := range []string{"a.lyth.go", "b.lyth.go"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("overlay lost %s after a targeted build:\n%s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".lyth", "gen", "b", "b.lyth.go")); err != nil {
		t.Errorf("generated code of b was removed: %v", err)
	}
}

// TestGoTypeCache checks that warm builds load Go type information from the
// persistent cache, and that editing a local Go package invalidates it.
func TestGoTypeCache(t *testing.T) {
	root := writeModule(t, "cached", map[string]string{
		"util/util.go": "package util\n\nfunc Double(x int) int { return 2 * x }\n",
		"main.lyth": `import "cached/util"
import "strings"

func main() {
    println(strings.ToUpper("x"), util.Double(21))
}
`,
	})
	first := compileModule(t, root, false)
	if first.TypesCached() {
		t.Fatal("first build cannot be served from the cache")
	}
	second := compileModule(t, root, false)
	if !second.TypesCached() {
		t.Fatal("second build should load Go types from the cache")
	}
	// Edit the local Go package: the cache must not be used, and the new
	// function must be visible.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "util", "util.go"), []byte("package util\n\nfunc Double(x int) int { return 2 * x }\n\nfunc Triple(x int) int { return 3 * x }\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main.lyth"), []byte(`import "cached/util"
import "strings"

func main() {
    println(strings.ToUpper("x"), util.Double(21), util.Triple(2))
}
`), 0o644)
	third := compileModule(t, root, false)
	if third.TypesCached() {
		t.Fatal("editing a local package must invalidate the cache")
	}
	bin := filepath.Join(root, "app.bin")
	if ok, err := third.GoBuild(bin, []string{"cached"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(third.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "X 42 6" {
		t.Fatalf("output %q, err %v", out, err)
	}
}

// testModule compiles a module with its tests and runs go test on it.
func testModule(t *testing.T, root string, flags ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	cfg := &build.Config{WorkDir: root, Tests: true, Stdout: &out, Stderr: &out}
	s, err := build.Compile(cfg, build.Target{Patterns: []string{"./..."}})
	if s != nil && s.Sink.HasErrors() {
		t.Fatalf("compilation failed:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.GoTest([]string{"./..."}, append([]string{"-count=1"}, flags...))
	if err != nil {
		t.Fatal(err)
	}
	return code, out.String()
}

const calcLyth = `import "strconv"

func Add(a: int, b: int): int = a + b

func Parse(s: string): int throws = strconv.Atoi(s)?
`

// TestLythTest covers lyth test: Lyth tests (plain and throwing), a Go test
// file beside the Lyth code, and exclusion of tests from ordinary builds.
func TestLythTest(t *testing.T) {
	root := writeModule(t, "testing", map[string]string{
		"calc/calc.lyth": calcLyth,
		"calc/calc_test.lyth": `import "testing"

func TestAdd(t: *testing.T) {
    if Add(2, 3) != 5 {
        t.Errorf("Add(2, 3) = %d", Add(2, 3))
    }
}

func TestParse(t: *testing.T) throws {
    n := Parse("42")?
    if n != 42 {
        t.Fatalf("parse = %d", n)
    }
}

func helper(): int = 1
`,
		"calc/calc_go_test.go": `package calc

import "testing"

func TestFromGo(t *testing.T) {
	if Add(1, helper()) != 2 {
		t.Fatal("Add from Go")
	}
}
`,
	})
	code, out := testModule(t, root, "-v")
	if code != 0 {
		t.Fatalf("go test failed (exit %d):\n%s", code, out)
	}
	for _, want := range []string{"--- PASS: TestAdd", "--- PASS: TestParse", "--- PASS: TestFromGo"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// An ordinary build ignores test files, even broken ones.
	os.WriteFile(filepath.Join(root, "calc", "broken_test.lyth"), []byte("func TestBroken(t: *testing.T) { undefinedThing() }\n"), 0o644)
	compileModule(t, root, false)
}

// TestLythTestFailuresPointAtLythLines checks that t.Errorf and a thrown
// error report .lyth file lines.
func TestLythTestFailuresPointAtLythLines(t *testing.T) {
	root := writeModule(t, "failing", map[string]string{
		"calc/calc.lyth": calcLyth,
		"calc/calc_test.lyth": `import "testing"

func TestWrong(t: *testing.T) {
    t.Errorf("wrong sum %d", Add(1, 1))
}

func TestThrows(t: *testing.T) throws {
    Parse("x")?
}
`,
	})
	code, out := testModule(t, root)
	if code == 0 {
		t.Fatalf("failing tests passed:\n%s", out)
	}
	for _, want := range []string{"calc_test.lyth:4: wrong sum 2", "calc_test.lyth:7:", `parsing "x": invalid syntax`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestLythTestSignatures checks the signature rules for test functions.
func TestLythTestSignatures(t *testing.T) {
	root := writeModule(t, "sigs", map[string]string{
		"calc/calc.lyth": calcLyth,
		"calc/calc_test.lyth": `import "testing"

func TestNoParam() {}

func BenchmarkWrong(t: *testing.T) {}

func ExampleThrows() throws {}

func testHelper(x: int): int = x
`,
		"calc/ext_test.lyth": "package calc_test\n\nfunc TestExt() {}\n",
	})
	cfg := &build.Config{WorkDir: root, Tests: true, Stdout: os.Stdout, Stderr: os.Stderr}
	s, _ := build.Compile(cfg, build.Target{Patterns: []string{"./..."}})
	got := render(s.Sink.All())
	for _, want := range []string{
		"TestNoParam is run by go test, so it must be declared func TestNoParam(t: *testing.T)",
		"BenchmarkWrong is run by go test, so it must be declared func BenchmarkWrong(b: *testing.B)",
		"ExampleThrows is run by go test, so it must be declared func ExampleThrows()",
		"external test packages (package calc_test) are not supported yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostics lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "testHelper") {
		t.Errorf("testHelper is not a test function:\n%s", got)
	}
}

// TestDefaultsAcrossPackages checks that a default value written in one
// package expands correctly at a call site in another: its public names are
// qualified and its imports are added.
func TestDefaultsAcrossPackages(t *testing.T) {
	root := writeModule(t, "defaults", map[string]string{
		"db/db.lyth": `import "time"

const DefaultPort = 5432

func Connect(host: string, port: int = DefaultPort, timeout: time.Duration = 5 * time.Second): string = "${host}:${port} ${timeout}"
`,
		"main.lyth": `import "defaults/db"

func main() {
    println(db.Connect("a"), db.Connect("b", timeout = 0))
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"defaults"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "a:5432 5s b:5432 0s" {
		t.Fatalf("output %q, err %v", out, err)
	}
}

// TestExtensionsAcrossPackages calls the exported extension functions of
// another Lyth package, which lower to qualified Go calls; unexported ones are
// not visible, and two packages declaring the same extension are ambiguous.
func TestExtensionsAcrossPackages(t *testing.T) {
	root := writeModule(t, "exts", map[string]string{
		"text/text.lyth": `func string.Shout(): string = self + "!"

func []T.Second[T](): T = self[1]

func string.whisper(): string = self
`,
		"main.lyth": `import "exts/text"

func main() {
    println("hi".Shout(), [1, 2, 3].Second())
}
`,
	})
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"exts"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "hi! 2" {
		t.Fatalf("output %q, err %v", out, err)
	}
	for _, g := range s.Packages {
		for _, f := range g.Generated {
			if g.Dir.ImportPath == "exts" && !strings.Contains(string(f.Code), "text.Shout(\"hi\")") {
				t.Errorf("expected a qualified Go call text.Shout(\"hi\"):\n%s", f.Code)
			}
		}
	}

	bad := writeModule(t, "exts2", map[string]string{
		"a/a.lyth":  "func string.Shout(): string = self\n\nfunc string.quiet(): string = self\n",
		"b/b.lyth":  "func string.Shout(): string = self\n",
		"main.lyth": "import \"exts2/a\"\nimport \"exts2/b\"\n\nfunc main() {\n    println(\"x\".Shout(), \"y\".quiet())\n}\n",
	})
	cfg := &build.Config{WorkDir: bad, Stdout: io.Discard, Stderr: io.Discard}
	s2, _ := build.Compile(cfg, build.Target{})
	msgs := render(s2.Sink.All())
	for _, want := range []string{"Shout is ambiguous: packages a and b both declare it for string", "has no field or method quiet"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing diagnostic %q in:\n%s", want, msgs)
		}
	}
}

// TestEnumsAndSealedAcrossPackages uses an enum and a sealed interface from
// another package: entries are qualified Go constants, an exhaustive when
// sees the other package's members, and only that package can add members.
func TestEnumsAndSealedAcrossPackages(t *testing.T) {
	files := map[string]string{
		"shapes/shapes.lyth": `enum Kind { Round, Square }

sealed interface Shape {
    Kind(): Kind
}

struct Circle : Shape {
    R: int
}

func Circle.Kind(): Kind = Kind.Round

struct Box : Shape {
    W: int
}

func Box.Kind(): Kind = Kind.Square
`,
		"main.lyth": `import "sealed/shapes"

func describe(s: shapes.Shape): string = when s {
    is shapes.Circle -> "circle ${s.R}"
    is shapes.Box -> "box ${s.W}"
}

func label(k: shapes.Kind): string = when k {
    Round -> "round"
    shapes.Kind.Square -> "square"
}

func main() {
    s: shapes.Shape = shapes.Box(W = 2)
    println(describe(shapes.Circle(R = 1)), describe(s), label(s.Kind()), shapes.Kind.Round)
}
`,
	}
	root := writeModule(t, "sealed", files)
	s := compileModule(t, root, false)
	bin := filepath.Join(root, "app.bin")
	if ok, err := s.GoBuild(bin, []string{"sealed"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).Output()
	if err != nil || strings.TrimSpace(string(out)) != "circle 1 box 2 square Round" {
		t.Fatalf("output %q, err %v", out, err)
	}

	// Another package cannot join the sealed interface.
	files["main.lyth"] = `import "sealed/shapes"

struct Tri : shapes.Shape {
    A: int
}

func Tri.Kind(): shapes.Kind = shapes.Kind.Round

func main() {}
`
	root = writeModule(t, "sealed", files)
	s, err = build.Compile(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(s.Sink.All()); !strings.Contains(got, "shapes.Shape is sealed: only types declared in package shapes can implement it") {
		t.Fatalf("missing sealed error:\n%s", got)
	}
}
