package diag

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/source"
)

// RenderOptions controls text rendering.
type RenderOptions struct {
	Color   bool // ANSI colors
	Verbose bool // include Go causes
}

const tabWidth = 4

// Render writes diagnostics in the human-readable format:
//
//	file.lyth:12:28: error[E0201]: type mismatch
//	   |
//	12 |     return repo.find(id)?
//	   |                      ^^ expected Int64, found String
//	   |
//	   = hint: ...
func Render(w io.Writer, diags []*Diagnostic, opts RenderOptions) {
	for i, d := range diags {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderOne(w, d, opts)
	}
}

func (o RenderOptions) paint(code, s string) string {
	if !o.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// marker is an underlined range on one source line.
type marker struct {
	start, end int // display cells
	ch         byte
	label      string
	color      string
}

func renderOne(w io.Writer, d *Diagnostic, opts RenderOptions) {
	sevColor := "1;31"
	if d.Severity == Warning {
		sevColor = "1;33"
	} else if d.Severity == Note {
		sevColor = "1;36"
	}
	head := d.Severity.String()
	if d.Code != "" {
		head += "[" + string(d.Code) + "]"
	}
	loc := Location(d.Span)
	if loc != "" {
		loc += ": "
	}
	fmt.Fprintf(w, "%s%s: %s\n", opts.paint("1", loc), opts.paint(sevColor, head), opts.paint("1", d.Message))

	gutter := 0
	if d.Span.IsValid() {
		gutter = len(strconv.Itoa(d.Span.StartPos().Line))
	}
	for _, r := range d.Related {
		if r.Span.IsValid() {
			if n := len(strconv.Itoa(r.Span.StartPos().Line)); n > gutter {
				gutter = n
			}
		}
	}
	pad := strings.Repeat(" ", gutter)
	bar := opts.paint("1;34", "|")

	// Related spans on the primary line are drawn in the same snippet.
	var others []Related
	if d.Span.IsValid() {
		markers := []marker{lineMarker(d.Span, '^', d.Label, sevColor)}
		line := d.Span.StartPos().Line
		for _, r := range d.Related {
			if r.Span.File == d.Span.File && r.Span.IsValid() && r.Span.StartPos().Line == line {
				markers = append(markers, lineMarker(r.Span, '-', r.Label, "1;34"))
			} else {
				others = append(others, r)
			}
		}
		snippet(w, d.Span.File, line, markers, gutter, opts)
	} else {
		others = d.Related
	}
	for _, r := range others {
		if !r.Span.IsValid() {
			continue
		}
		fmt.Fprintf(w, "%s %s %s\n", pad, opts.paint("1;34", ":::"), Location(r.Span))
		snippet(w, r.Span.File, r.Span.StartPos().Line, []marker{lineMarker(r.Span, '-', r.Label, "1;34")}, gutter, opts)
	}
	if len(d.Notes) > 0 || len(d.Hints) > 0 || (opts.Verbose && d.Cause != nil) {
		if d.Span.IsValid() || len(d.Related) > 0 {
			fmt.Fprintf(w, "%s %s\n", pad, bar)
		}
	}
	for _, n := range d.Notes {
		writeIndented(w, pad, opts.paint("1;34", "=")+" note: ", n)
	}
	for _, h := range d.Hints {
		writeIndented(w, pad, opts.paint("1;34", "=")+" hint: ", h.Message)
	}
	if opts.Verbose && d.Cause != nil {
		c := d.Cause
		msg := c.Message
		if c.Position != "" {
			msg += " (at " + c.Position
			if c.Mapping != "" {
				msg += ", mapping: " + c.Mapping
			}
			msg += ")"
		}
		writeIndented(w, pad, opts.paint("1;34", "=")+" go: ", msg)
	}
}

// writeIndented writes a possibly multi-line note under the gutter.
func writeIndented(w io.Writer, pad, prefix, text string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	fmt.Fprintf(w, "%s %s%s\n", pad, prefix, lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintf(w, "%s   %s\n", pad, l)
	}
}

// lineMarker computes the display-cell range of span on its first line.
func lineMarker(span source.Span, ch byte, label, color string) marker {
	f := span.File
	start := span.StartPos()
	lineStart := f.LineStart(start.Line)
	text := f.LineText(start.Line)
	lineEnd := lineStart + len(text)
	s := displayWidth(string(f.Content[lineStart:clamp(span.Start, lineStart, lineEnd)]))
	e := displayWidth(string(f.Content[lineStart:clamp(span.End, lineStart, lineEnd)]))
	if e <= s {
		e = s + 1
	}
	return marker{start: s, end: e, ch: ch, label: label, color: color}
}

// snippet renders one source line with its markers. The rightmost marker's
// label follows the underline; other labels hang below, rustc-style.
func snippet(w io.Writer, f *source.File, line int, markers []marker, gutter int, opts RenderOptions) {
	pad := strings.Repeat(" ", gutter)
	bar := opts.paint("1;34", "|")
	fmt.Fprintf(w, "%s %s\n", pad, bar)
	num := fmt.Sprintf("%*d", gutter, line)
	fmt.Fprintf(w, "%s %s %s\n", opts.paint("1;34", num), bar, expandTabs(f.LineText(line)))
	sort.SliceStable(markers, func(i, j int) bool { return markers[i].start < markers[j].start })
	var under strings.Builder
	col := 0
	for _, m := range markers {
		if m.start < col {
			continue // overlapping markers: keep the first
		}
		under.WriteString(strings.Repeat(" ", m.start-col))
		under.WriteString(opts.paint(m.color, strings.Repeat(string(m.ch), m.end-m.start)))
		col = m.end
	}
	last := markers[len(markers)-1]
	if last.label != "" {
		under.WriteString(" " + opts.paint(last.color, last.label))
	}
	fmt.Fprintf(w, "%s %s %s\n", pad, bar, under.String())
	// Hanging labels for the other markers, right to left.
	pending := markers[:len(markers)-1]
	var withLabels []marker
	for _, m := range pending {
		if m.label != "" {
			withLabels = append(withLabels, m)
		}
	}
	for i := len(withLabels) - 1; i >= 0; i-- {
		var b strings.Builder
		col := 0
		for j := 0; j < i; j++ {
			b.WriteString(strings.Repeat(" ", withLabels[j].start-col))
			b.WriteString(opts.paint(withLabels[j].color, "|"))
			col = withLabels[j].start + 1
		}
		b.WriteString(strings.Repeat(" ", max(0, withLabels[i].start-col)))
		b.WriteString(opts.paint(withLabels[i].color, withLabels[i].label))
		fmt.Fprintf(w, "%s %s %s\n", pad, bar, b.String())
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func displayWidth(s string) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r == '\t' {
			n += tabWidth - n%tabWidth
		} else {
			n++
		}
	}
	return n
}

func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '\t' {
			k := tabWidth - n%tabWidth
			b.WriteString(strings.Repeat(" ", k))
			n += k
		} else {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}

// Location formats a span start as "name:line:col".
func Location(s source.Span) string {
	if !s.IsValid() {
		return ""
	}
	p := s.StartPos()
	return fmt.Sprintf("%s:%d:%d", s.File.Name, p.Line, p.Col)
}

// jsonDiagnostic is the machine-readable form.
type jsonDiagnostic struct {
	Severity string        `json:"severity"`
	Code     string        `json:"code,omitempty"`
	Message  string        `json:"message"`
	Location *jsonLocation `json:"location,omitempty"`
	Label    string        `json:"label,omitempty"`
	Related  []jsonRelated `json:"related,omitempty"`
	Notes    []string      `json:"notes,omitempty"`
	Hints    []jsonHint    `json:"hints,omitempty"`
	Cause    *GoCause      `json:"cause,omitempty"`
}

type jsonLocation struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine"`
	EndColumn int    `json:"endColumn"`
	Offset    int    `json:"offset"`
	EndOffset int    `json:"endOffset"`
}

type jsonRelated struct {
	Location *jsonLocation `json:"location"`
	Label    string        `json:"label,omitempty"`
}

type jsonHint struct {
	Message string    `json:"message"`
	Edit    *jsonEdit `json:"edit,omitempty"`
}

type jsonEdit struct {
	Location *jsonLocation `json:"location"`
	NewText  string        `json:"newText"`
	Fix      bool          `json:"fix,omitempty"` // lyth fix applies it
}

func toJSONLoc(s source.Span) *jsonLocation {
	if !s.IsValid() {
		return nil
	}
	a, b := s.StartPos(), s.EndPos()
	return &jsonLocation{File: s.File.Name, Line: a.Line, Column: a.Col, EndLine: b.Line, EndColumn: b.Col, Offset: s.Start, EndOffset: s.End}
}

// RenderJSON writes one JSON object per line for each diagnostic.
func RenderJSON(w io.Writer, diags []*Diagnostic) error {
	enc := json.NewEncoder(w)
	for _, d := range diags {
		j := jsonDiagnostic{
			Severity: d.Severity.String(),
			Code:     string(d.Code),
			Message:  d.Message,
			Location: toJSONLoc(d.Span),
			Label:    d.Label,
			Notes:    d.Notes,
			Cause:    d.Cause,
		}
		for _, r := range d.Related {
			j.Related = append(j.Related, jsonRelated{Location: toJSONLoc(r.Span), Label: r.Label})
		}
		for _, h := range d.Hints {
			jh := jsonHint{Message: h.Message}
			if h.Edit != nil {
				jh.Edit = &jsonEdit{Location: toJSONLoc(h.Edit.Span), NewText: h.Edit.NewText, Fix: h.Edit.Fix}
			}
			j.Hints = append(j.Hints, jh)
		}
		if err := enc.Encode(j); err != nil {
			return err
		}
	}
	return nil
}
