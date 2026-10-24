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
	if !s.Sink.HasErrors() || !strings.Contains(render(s.Sink.All()), "methods are declared func *Counter.Name") {
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
