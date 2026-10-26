package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/build"
)

// TestFix runs lyth fix on each testdata/fix/*.lyth, an older or Go or
// Kotlin spelling of Lyth, and compares the result with its .lyth.golden
// file. The fixed code must compile without errors. After checking an
// intended change, rewrite the golden files with
//
//	go test ./tests -run TestFix -update
func TestFix(t *testing.T) {
	files, _ := filepath.Glob("testdata/fix/*.lyth")
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no fix tests")
	}
	root, dirs := newModule(t, files, "lythfix")
	changed, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Sink.HasErrors() {
		t.Fatalf("errors remain after lyth fix:\n%s", render(s.Sink.All()))
	}
	if len(changed) != len(files) {
		t.Errorf("lyth fix changed %d files, want %d: %v", len(changed), len(files), changed)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			got, err := os.ReadFile(filepath.Join(dirs[f], filepath.Base(f)))
			if err != nil {
				t.Fatal(err)
			}
			golden := f + ".golden"
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run go test ./tests -run TestFix -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("fixed code differs from %s\n%s", golden, diffLines(string(want), string(got)))
			}
		})
	}
}

// TestFixLeavesOtherErrors checks that lyth fix applies only fixes: an
// error without one (here a Go method receiver, whose body names the
// receiver) stays, and the file is otherwise rewritten.
func TestFixLeavesOtherErrors(t *testing.T) {
	root := writeModule(t, "partial", map[string]string{
		"main.lyth": `struct Counter {
    N: Int
}

func (c *Counter) Inc() {
    c.N++
}

func main() {}
`,
	})
	changed, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed %v, want main.lyth", changed)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.lyth"))
	if !strings.Contains(string(got), "N: int") || !strings.Contains(string(got), "func (c *Counter) Inc()") {
		t.Errorf("unexpected result:\n%s", got)
	}
	if !s.Sink.HasErrors() || !strings.Contains(render(s.Sink.All()), "methods are declared func *Counter.Inc") {
		t.Errorf("the receiver error should remain:\n%s", render(s.Sink.All()))
	}
}

// TestFixSkipsRecovery checks that a fix computed while the parser recovers
// from a construct it cannot read is not applied: Go's if v := f(); c is
// reported, and lyth fix leaves the line alone.
func TestFixSkipsRecovery(t *testing.T) {
	src := `func f(): int = 1

func main() {
    if v := f(); v > 0 {
        println(v)
    }
}
`
	root := writeModule(t, "recovery", map[string]string{"main.lyth": src})
	changed, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.lyth"))
	if len(changed) != 0 || string(got) != src {
		t.Errorf("lyth fix changed %v:\n%s", changed, got)
	}
	if !s.Sink.HasErrors() {
		t.Error("the Go if statement should still be reported")
	}
}

// TestFixLeavesDollarNames checks that lyth fix does not rewrite $name in a
// string (ADR 0042): when name is a variable, $name may be the text it is
// (PostgreSQL's $body$) or Kotlin's template, so E0006 stays for the author
// to decide, and the string is unchanged. $set, naming nothing, is text.
func TestFixLeavesDollarNames(t *testing.T) {
	src := `func trigger(body: string): string {
    return "AS $body$ " + body + " $body$ LANGUAGE plpgsql"
}

func main() {
    println(trigger("BEGIN END;"), "$set")
}
`
	root := writeModule(t, "dollars", map[string]string{"main.lyth": src})
	changed, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.lyth"))
	if len(changed) != 0 || string(got) != src {
		t.Errorf("lyth fix changed the strings (%v):\n%s", changed, got)
	}
	out := render(s.Sink.All())
	if strings.Count(out, "error[E0006]") != 2 || !strings.Contains(out, `write ${body} to interpolate it, or \$body to keep the dollar sign`) {
		t.Errorf("want E0006 for both $body, with both edits as hints:\n%s", out)
	}
}

// TestFixLeavesAmbiguousMembers checks that lyth fix inserts self. only
// where a bare name can mean nothing but the member (ADR 0038): a name that
// also names a package-level variable, an import before a dot, or a
// package-level function stays as written, with the edit as a hint.
func TestFixLeavesAmbiguousMembers(t *testing.T) {
	src := `import "log"

var limit = 50

func validate(n: int): bool = n > 0

struct Query {
    limit: int
    log: *log.Logger
    Count: int
}

func Query.pageSize(): int = limit

func Query.note() {
    log.Println(Count)
}

func Query.validate(): bool = validate(self.Count)

func main() {}
`
	want := strings.Replace(src, "log.Println(Count)", "log.Println(self.Count)", 1)
	root := writeModule(t, "ambiguous", map[string]string{"main.lyth": src})
	_, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.lyth"))
	if string(got) != want {
		t.Errorf("lyth fix wrote:\n%s\nwant:\n%s", got, want)
	}
	out := render(s.Sink.All())
	for _, msg := range []string{
		"limit is a field of self and also names the package-level variable limit",
		"log is a field of self and also names the import log",
		"validate is a method of self and also names the function validate",
		"hint: if the field is meant: self.limit",
	} {
		if !strings.Contains(out, msg) {
			t.Errorf("missing %q in:\n%s", msg, out)
		}
	}
	// A second run changes nothing.
	changed, _, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil || len(changed) != 0 {
		t.Errorf("second lyth fix changed %v (err %v)", changed, err)
	}
}

// TestFixUnclosedBody checks that a struct, enum, or interface missing its
// } ends at the next top-level declaration: the functions after it are not
// read as interface methods, whose fix would strip their func keyword, and
// the members before it are still fixed.
func TestFixUnclosedBody(t *testing.T) {
	src := `interface Shape {
    Area() float64

func keep(): int = 42

struct P {
    X int

func main() {
    println(keep())
}
`
	root := writeModule(t, "unclosed", map[string]string{"main.lyth": src})
	_, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.lyth"))
	for _, want := range []string{"Area(): float64", "func keep(): int = 42", "X: int", "func main() {"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("fixed file lacks %q:\n%s", want, got)
		}
	}
	if msgs := render(s.Sink.All()); !strings.Contains(msgs, "expected '}' to close the interface body before this declaration") ||
		!strings.Contains(msgs, "expected '}' to close the struct body before this declaration") || strings.Contains(msgs, "undefined") {
		t.Errorf("unexpected diagnostics:\n%s", msgs)
	}
}

// TestFixLeavesChangingBounds checks that a Go counting loop whose bound may
// change while it runs stays a hint: a range reads the bound once, Go's
// condition on every iteration. Here a call grows a package-level slice,
// and a lambda assigns a local.
func TestFixLeavesChangingBounds(t *testing.T) {
	src := `var queue: []int = [1]

func step() {
    if len(queue) < 4 {
        queue = append(queue, 0)
    }
}

func drain(): int {
    total := 0
    for i := 0; i < len(queue); i++ {
        step()
        total += 1
    }
    return total
}

func local(): int {
    m := 2
    grow := { m = m + 1 }
    c := 0
    for j := 0; j < m; j++ {
        if j < 5 {
            grow()
        }
        c += 1
    }
    return c
}

func main() {
    println(drain(), local())
}
`
	root := writeModule(t, "bounds", map[string]string{"main.lyth": src})
	changed, s, err := build.Fix(&build.Config{WorkDir: root, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "main.lyth"))
	if len(changed) != 0 || string(got) != src {
		t.Errorf("lyth fix changed %v:\n%s", changed, got)
	}
	if n := strings.Count(render(s.Sink.All()), "Lyth has no three-part for loop"); n != 2 {
		t.Errorf("want both loops reported, got %d:\n%s", n, render(s.Sink.All()))
	}
}
