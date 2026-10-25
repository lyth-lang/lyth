package build

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/lyth-lang/lyth/internal/diag"
)

// maxFixPasses bounds lyth fix: each pass applies the fixes that do not
// overlap, and a fix inside a fixed construct is reported again by the next
// compilation.
const maxFixPasses = 10

// Fix applies the fixes of the target's diagnostics (diag.Edit.Fix: an
// older, Kotlin, or Go spelling of a Lyth construct) to its source files,
// test files included, compiling again until none is left. It returns the
// files it changed and the last session, whose diagnostics are what the
// fixes could not settle.
func Fix(cfg *Config, target Target) ([]string, *Session, error) {
	c := *cfg
	c.NoWrite = true
	c.Tests = true
	changed := map[string]bool{}
	for pass := 0; ; pass++ {
		s, err := Compile(&c, target)
		if err != nil || pass == maxFixPasses {
			return sortedKeys(changed), s, err
		}
		wrote := false
		for path, edits := range s.fixes() {
			info, err := os.Stat(path)
			if err != nil {
				return sortedKeys(changed), s, err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return sortedKeys(changed), s, err
			}
			if err := os.WriteFile(path, applyEdits(content, edits), info.Mode().Perm()); err != nil {
				return sortedKeys(changed), s, err
			}
			changed[path] = true
			wrote = true
		}
		if !wrote {
			return sortedKeys(changed), s, nil
		}
	}
}

// fixes collects the fix edits in the target packages' files, by path. A
// file whose compiled content differs from the disk (an editor overlay) is
// left alone.
func (s *Session) fixes() map[string][]*diag.Edit {
	files := map[string]bool{}
	for _, p := range s.Targets {
		if p.Dir.Std {
			continue
		}
		for _, f := range p.Files {
			if _, overlaid := s.cfg.Overlay[f.Path]; !overlaid {
				files[f.Path] = true
			}
		}
	}
	out := map[string][]*diag.Edit{}
	for _, e := range ApplicableFixes(s.Sink.All()) {
		if path := e.Span.File.Path; files[path] {
			out[path] = append(out[path], e)
		}
	}
	return out
}

// ApplicableFixes returns the fix edits of ds that lyth fix applies. A
// syntax error without a fix where a fix would apply means the parser was
// recovering there, and the fix may be computed from a misreading, so such
// fixes wait for the next pass.
func ApplicableFixes(ds []*diag.Diagnostic) []*diag.Edit {
	var unfixed []*diag.Diagnostic
	for _, d := range ds {
		if d.Severity == diag.Error && len(d.Fixes()) == 0 && d.Span.IsValid() && d.Code.IsSyntax() {
			unfixed = append(unfixed, d)
		}
	}
	var out []*diag.Edit
	for _, d := range ds {
		for _, e := range d.Fixes() {
			if !overlapsAny(e, unfixed) {
				out = append(out, e)
			}
		}
	}
	return out
}

// overlapsAny reports whether the edit touches the span of one of ds.
func overlapsAny(e *diag.Edit, ds []*diag.Diagnostic) bool {
	for _, d := range ds {
		if d.Span.File == e.Span.File && d.Span.Start <= e.Span.End && e.Span.Start <= d.Span.End {
			return true
		}
	}
	return false
}

// applyEdits applies edits to content in order of position. An edit that
// overlaps one already applied is dropped; the next pass reports it again
// if it still applies.
func applyEdits(content []byte, edits []*diag.Edit) []byte {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].Span.Start < edits[j].Span.Start })
	var out []byte
	pos := 0
	var last *diag.Edit
	for _, e := range edits {
		if last != nil && e.Span.Start == last.Span.Start && e.Span.End == last.Span.End && e.NewText == last.NewText {
			continue // the same fix, reported twice
		}
		if e.Span.Start < pos || e.Span.End > len(content) {
			continue
		}
		out = append(out, content[pos:e.Span.Start]...)
		out = append(out, e.NewText...)
		pos = e.Span.End
		last = e
	}
	return append(out, content[pos:]...)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, filepath.Clean(k))
	}
	sort.Strings(out)
	return out
}
