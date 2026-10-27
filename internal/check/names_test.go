package check

import (
	"slices"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
)

// TestRequalify checks that only whole package paths are rewritten in the
// names go/types writes: myapp is not qualified by the package app.
func TestRequalify(t *testing.T) {
	quals := map[string]string{"app": "", "app/myapp": "myapp.", "gopkg.in/yaml.v3": "yaml."}
	for word, want := range map[string]string{
		"app.mem":               "mem",
		"app/myapp.Store":       "myapp.Store",
		"app/myapp.Store.Get":   "myapp.Store.Get",
		"gopkg.in/yaml.v3.Node": "yaml.Node",
		"myapp.Store":           "myapp.Store",
		"other/app.T":           "other/app.T",
		"...int":                "...int",
	} {
		if got := requalify(word, quals); got != want {
			t.Errorf("requalify(%q) = %q, want %q", word, got, want)
		}
	}
}

// TestSuggestionIsDeterministic checks that of several equally close names
// the "did you mean" hint names the same one in every run: the first in
// alphabetical order, and a name of an inner scope before an outer one.
func TestSuggestionIsDeterministic(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"func main() {\n    hat := 1\n    cat := 2\n    bat := 3\n    println(hat, cat, bat, xat)\n}\n", "did you mean bat?"},
		{"var mat = 1\n\nfunc main() {\n    rat := 2\n    println(rat, xat)\n}\n", "did you mean rat?"},
	} {
		for range 20 {
			sink := &diag.Sink{}
			file := syntax.Parse(source.NewFile("s.lyth", "s.lyth", []byte(tc.src)), sink)
			Check(&Config{Sink: sink, Import: func(string) (*Package, string) { return nil, "no imports" }, DirName: "main"}, "main", []*syntax.File{file})
			var hints []string
			for _, d := range sink.All() {
				for _, h := range d.Hints {
					hints = append(hints, h.Message)
				}
			}
			if !slices.Contains(hints, tc.want) {
				t.Fatalf("hints %q, want %q", hints, tc.want)
			}
		}
	}
}
