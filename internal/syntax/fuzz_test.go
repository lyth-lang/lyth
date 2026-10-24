package syntax

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// FuzzParse checks that the lexer and parser never panic and always return
// a file, whatever the input.
func FuzzParse(f *testing.F) {
	seeds, _ := filepath.Glob("../../tests/testdata/run/*.lyth")
	more, _ := filepath.Glob("../../tests/testdata/check/*.lyth")
	for _, s := range append(seeds, more...) {
		if data, err := os.ReadFile(s); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte("fun main() { val x = \"${\" }"))
	f.Add([]byte("struct S { a: []Map<String, []Int> }"))
	f.Fuzz(func(t *testing.T, data []byte) {
		sink := &diag.Sink{}
		file := Parse(source.NewFile("fuzz.lyth", "fuzz.lyth", data), sink)
		if file == nil {
			t.Fatal("Parse returned nil")
		}
	})
}
