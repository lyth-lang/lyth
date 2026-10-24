// Package diag defines Lyth's diagnostic model, the sink every compiler stage
// reports into, and the text and JSON renderers.
package diag

import (
	"fmt"
	"sort"

	"github.com/lyth-lang/lyth/internal/source"
)

// Severity of a diagnostic.
type Severity int

const (
	Error Severity = iota
	Warning
	Note
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "note"
	}
}

// Code is a stable diagnostic identifier such as "E0201".
type Code string

// Related is a secondary location with its own label.
type Related struct {
	Span  source.Span
	Label string
}

// Hint is a suggestion, optionally with a concrete replacement edit.
type Hint struct {
	Message string
	Edit    *Edit
}

// Edit replaces the text covered by Span with NewText.
type Edit struct {
	Span    source.Span
	NewText string
}

// GoCause records the original Go toolchain message behind a mapped diagnostic.
type GoCause struct {
	Message  string // original message from go/types or the go command
	Position string // generated file:line:col, if known
	Mapping  string // "exact", "line", or "approximate"
}

// Diagnostic is one problem report.
type Diagnostic struct {
	Severity Severity
	Code     Code
	Message  string
	Span     source.Span
	Label    string
	Related  []Related
	Notes    []string
	Hints    []Hint
	Cause    *GoCause
}

// WithLabel sets the primary label and returns d for chaining.
func (d *Diagnostic) WithLabel(format string, args ...any) *Diagnostic {
	d.Label = fmt.Sprintf(format, args...)
	return d
}

// WithNote appends a note.
func (d *Diagnostic) WithNote(format string, args ...any) *Diagnostic {
	d.Notes = append(d.Notes, fmt.Sprintf(format, args...))
	return d
}

// WithHint appends a hint without an edit.
func (d *Diagnostic) WithHint(format string, args ...any) *Diagnostic {
	d.Hints = append(d.Hints, Hint{Message: fmt.Sprintf(format, args...)})
	return d
}

// WithEdit appends a hint with a replacement edit.
func (d *Diagnostic) WithEdit(span source.Span, newText, format string, args ...any) *Diagnostic {
	d.Hints = append(d.Hints, Hint{Message: fmt.Sprintf(format, args...), Edit: &Edit{Span: span, NewText: newText}})
	return d
}

// WithRelated appends a secondary span.
func (d *Diagnostic) WithRelated(span source.Span, format string, args ...any) *Diagnostic {
	if span.IsValid() {
		d.Related = append(d.Related, Related{Span: span, Label: fmt.Sprintf(format, args...)})
	}
	return d
}

// Sink collects diagnostics from all stages. It deduplicates identical
// reports and can suppress diagnostics inside spans already known to be bad.
// A Sink is not safe for concurrent use.
type Sink struct {
	diags []*Diagnostic
	seen  map[string]bool
}

// Report adds a diagnostic and returns it for further decoration.
func (s *Sink) Report(sev Severity, code Code, span source.Span, format string, args ...any) *Diagnostic {
	d := &Diagnostic{Severity: sev, Code: code, Span: span, Message: fmt.Sprintf(format, args...)}
	key := fmt.Sprintf("%s|%p|%d|%d|%s", code, span.File, span.Start, span.End, d.Message)
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if s.seen[key] {
		// Return a detached diagnostic so callers can still chain decorations.
		return d
	}
	s.seen[key] = true
	s.diags = append(s.diags, d)
	return d
}

// Errorf reports an error.
func (s *Sink) Errorf(code Code, span source.Span, format string, args ...any) *Diagnostic {
	return s.Report(Error, code, span, format, args...)
}

// Warnf reports a warning.
func (s *Sink) Warnf(code Code, span source.Span, format string, args ...any) *Diagnostic {
	return s.Report(Warning, code, span, format, args...)
}

// Add appends an already constructed diagnostic.
func (s *Sink) Add(d *Diagnostic) {
	s.diags = append(s.diags, d)
}

// Len returns the number of diagnostics reported so far.
func (s *Sink) Len() int { return len(s.diags) }

// Since returns the diagnostics reported after the first n, sorted.
func (s *Sink) Since(n int) []*Diagnostic {
	if n >= len(s.diags) {
		return nil
	}
	tmp := &Sink{diags: s.diags[n:]}
	return tmp.All()
}

// HasErrors reports whether any error-severity diagnostic was reported.
func (s *Sink) HasErrors() bool {
	for _, d := range s.diags {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// ErrorCount returns the number of errors.
func (s *Sink) ErrorCount() int {
	n := 0
	for _, d := range s.diags {
		if d.Severity == Error {
			n++
		}
	}
	return n
}

// All returns the diagnostics sorted by file, position, and severity.
func (s *Sink) All() []*Diagnostic {
	out := append([]*Diagnostic(nil), s.diags...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		an, bn := fileName(a.Span), fileName(b.Span)
		if an != bn {
			return an < bn
		}
		if a.Span.Start != b.Span.Start {
			return a.Span.Start < b.Span.Start
		}
		return a.Severity < b.Severity
	})
	return out
}

func fileName(s source.Span) string {
	if s.File == nil {
		return ""
	}
	return s.File.Name
}
