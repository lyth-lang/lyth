package check

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
)

// FuzzCheck checks that the type checker never panics on arbitrary
// (usually invalid) programs. Imports fail, so only the core language is
// exercised.
func FuzzCheck(f *testing.F) {
	seeds, _ := filepath.Glob("../../tests/testdata/check/*.lyth")
	more, _ := filepath.Glob("../../tests/testdata/run/*.lyth")
	for _, s := range append(seeds, more...) {
		if data, err := os.ReadFile(s); err == nil {
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		sink := &diag.Sink{}
		file := syntax.Parse(source.NewFile("fuzz.lyth", "fuzz.lyth", data), sink)
		conf := &Config{Sink: sink, Import: func(string) (*Package, string) { return nil, "imports are disabled in fuzzing" }, DirName: "fuzz"}
		Check(conf, "fuzz", []*syntax.File{file})
	})
}
