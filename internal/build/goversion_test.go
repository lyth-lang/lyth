package build

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/golang"
	"github.com/lyth-lang/lyth/internal/source"
)

// TestNewerGo checks that errors a Go release newer than lyth knows can
// cause name that cause and the fix, and that other errors, and the same
// errors with a supported Go, do not.
func TestNewerGo(t *testing.T) {
	defer func(m int) { BuiltGoMinor = m }(BuiltGoMinor)
	BuiltGoMinor = MinGoMinor
	f := source.NewFile("/m/a.lyth", "a.lyth", []byte("func main() {}\n"))
	newer := fmt.Sprintf("go1.%d.2", MinGoMinor+1)
	session := func(goVersion string) *Session {
		s := &Session{Sink: &diag.Sink{}, goEnv: &golang.Env{Vars: map[string]string{"GOVERSION": goVersion}}}
		s.Sink.Errorf(diag.EGoTypeCheck, source.Span{File: f}, "undefined: x")
		s.Sink.Errorf(diag.EImport, source.Span{File: f}, `could not load export data: internal error in importing "fmt" (unsupported version: 3)`)
		s.Sink.Errorf(diag.EImport, source.Span{File: f}, `package example.com/missing is not in std`)
		s.Sink.Errorf(diag.ETypeMismatch, source.Span{File: f}, "type mismatch")
		return s
	}

	s := session(newer)
	s.explainNewerGo()
	want := []bool{true, true, false, false}
	for i, d := range s.Sink.All() {
		notes, hints := strings.Join(d.Notes, "\n"), ""
		for _, h := range d.Hints {
			hints += h.Message + "\n"
		}
		explained := strings.Contains(notes, newer+" is newer than the Go this lyth was built with")
		if explained != want[i] {
			t.Errorf("%s %q: explained = %v, want %v (notes %q)", d.Code, d.Message, explained, want[i], notes)
		}
		if explained && !strings.Contains(hints, fmt.Sprintf("GOTOOLCHAIN=go1.%d.0", MinGoMinor)) {
			t.Errorf("%s: hint does not name a supported toolchain: %q", d.Code, hints)
		}
	}

	s = session(fmt.Sprintf("go1.%d.5", MinGoMinor))
	s.explainNewerGo()
	for _, d := range s.Sink.All() {
		if len(d.Notes) != 0 {
			t.Errorf("a supported Go explained %s: %q", d.Code, d.Notes)
		}
	}

	err := session(newer).newerGoError(errors.New("loading Go packages: boom"))
	if msg := err.Error(); !strings.HasPrefix(msg, "loading Go packages: boom\n") || !strings.Contains(msg, "lyth supports Go 1.") {
		t.Errorf("newerGoError = %q", msg)
	}
	if err := session(fmt.Sprintf("go1.%d.0", MinGoMinor)).newerGoError(errors.New("boom")); err.Error() != "boom" {
		t.Errorf("newerGoError with a supported Go = %q", err)
	}
}
