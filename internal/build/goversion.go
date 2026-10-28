package build

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
)

// BuiltGoMinor is the minor version of the Go release this lyth was built
// with, the newest Go it knows: its go/types validates the generated Go, and
// its export data reader reads the types of Go dependencies. A lyth supports
// Go 1.MinGoMinor through Go 1.BuiltGoMinor (docs/guide/compatibility.md).
var BuiltGoMinor = goMinor(strings.TrimPrefix(runtime.Version(), "go"))

// SupportedGo describes the Go releases this lyth supports, for lyth version.
func SupportedGo() string {
	if BuiltGoMinor < MinGoMinor {
		return fmt.Sprintf("Go 1.%d or newer", MinGoMinor) // a development toolchain (devel ...)
	}
	if BuiltGoMinor == MinGoMinor {
		return fmt.Sprintf("Go 1.%d", MinGoMinor)
	}
	return fmt.Sprintf("Go 1.%d to 1.%d", MinGoMinor, BuiltGoMinor)
}

// goAhead reports whether the go toolchain is a newer Go release than this
// lyth knows, and its version.
func (s *Session) goAhead() (string, bool) {
	if s.goEnv == nil || BuiltGoMinor < MinGoMinor {
		return "", false
	}
	v, minor := s.goEnv.Version()
	return v, minor > BuiltGoMinor
}

// explainNewerGo adds the cause to errors that a Go release newer than this
// lyth can produce: Go types it cannot read, or generated Go its go/types
// rejects. A newer Go that works needs no word.
func (s *Session) explainNewerGo() {
	v, ahead := s.goAhead()
	if !ahead {
		return
	}
	for _, d := range s.Sink.All() {
		if d.Severity != diag.Error || !newerGoCanCause(d) {
			continue
		}
		d.WithNote("%s is newer than the Go this lyth was built with (go1.%d); lyth supports %s", v, BuiltGoMinor, SupportedGo()).
			WithHint("build with a supported Go (GOTOOLCHAIN=go1.%d.0), or update lyth: brew upgrade lyth, or go install github.com/lyth-lang/lyth/cmd/lyth@latest with %s", BuiltGoMinor, v)
	}
}

// newerGoCanCause reports whether d is an error a newer Go can cause.
func newerGoCanCause(d *diag.Diagnostic) bool {
	switch d.Code {
	case diag.EGoTypeCheck, diag.EInternal:
		return true
	case diag.EImport:
		return strings.Contains(d.Message, "export data") || strings.Contains(d.Message, "unsupported version")
	}
	return false
}

// newerGoError adds the same explanation to an error that stopped the build
// before any diagnostic, such as a failure to load Go packages.
func (s *Session) newerGoError(err error) error {
	v, ahead := s.goAhead()
	if err == nil || !ahead {
		return err
	}
	return fmt.Errorf("%w\n\t%s is newer than the Go this lyth was built with (go1.%d); lyth supports %s\n\tbuild with a supported Go (GOTOOLCHAIN=go1.%d.0), or update lyth", err, v, BuiltGoMinor, SupportedGo(), BuiltGoMinor)
}
