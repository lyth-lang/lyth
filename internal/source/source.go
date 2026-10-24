// Package source holds Lyth source files and positions within them.
//
// All compiler stages track locations as byte-offset spans into a File.
// Line and column numbers are computed only for display: lines are 1-based,
// columns are 1-based counts of Unicode code points.
package source

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// File is one source file.
type File struct {
	Path    string // absolute path on disk (or a synthetic name for tests)
	Name    string // display name, usually relative to the working directory
	Content []byte
	lines   []int // byte offsets of the start of each line
}

// NewFile creates a File and indexes its lines.
func NewFile(path, name string, content []byte) *File {
	f := &File{Path: path, Name: name, Content: content}
	f.lines = append(f.lines, 0)
	for i, b := range content {
		if b == '\n' {
			f.lines = append(f.lines, i+1)
		}
	}
	return f
}

// Base returns the file's base name.
func (f *File) Base() string { return filepath.Base(f.Path) }

// Position is a human-readable location.
type Position struct {
	Line int // 1-based
	Col  int // 1-based, in code points
}

// LineCount returns the number of lines in the file.
func (f *File) LineCount() int { return len(f.lines) }

// Position converts a byte offset to a line and column.
func (f *File) Position(off int) Position {
	if off < 0 {
		off = 0
	}
	if off > len(f.Content) {
		off = len(f.Content)
	}
	line := sort.Search(len(f.lines), func(i int) bool { return f.lines[i] > off }) - 1
	start := f.lines[line]
	col := utf8.RuneCount(f.Content[start:off]) + 1
	return Position{Line: line + 1, Col: col}
}

// LineStart returns the byte offset of the start of the 1-based line.
func (f *File) LineStart(line int) int {
	if line < 1 {
		return 0
	}
	if line > len(f.lines) {
		return len(f.Content)
	}
	return f.lines[line-1]
}

// LineText returns the text of a 1-based line without its newline.
func (f *File) LineText(line int) string {
	if line < 1 || line > len(f.lines) {
		return ""
	}
	start := f.lines[line-1]
	end := len(f.Content)
	if line < len(f.lines) {
		end = f.lines[line] - 1
	}
	if end > start && f.Content[end-1] == '\r' {
		end--
	}
	if end < start {
		end = start
	}
	return string(f.Content[start:end])
}

// Span is a half-open byte range [Start, End) in a File.
type Span struct {
	File  *File
	Start int
	End   int
}

// IsValid reports whether the span refers to a file.
func (s Span) IsValid() bool { return s.File != nil }

// Len returns the span length in bytes.
func (s Span) Len() int { return s.End - s.Start }

// Text returns the source text covered by the span.
func (s Span) Text() string {
	if s.File == nil || s.Start < 0 || s.End > len(s.File.Content) || s.Start > s.End {
		return ""
	}
	return string(s.File.Content[s.Start:s.End])
}

// Short returns the span text abbreviated for use inside a one-line
// message: the first line only, at most 40 characters.
func (s Span) Short() string {
	t := s.Text()
	cut := false
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = strings.TrimRight(t[:i], " \t\r")
		cut = true
	}
	if utf8.RuneCountInString(t) > 40 {
		r := []rune(t)
		t = string(r[:37])
		cut = true
	}
	if cut {
		t += "..."
	}
	return t
}

// StartPos returns the line and column of the span start.
func (s Span) StartPos() Position {
	if s.File == nil {
		return Position{}
	}
	return s.File.Position(s.Start)
}

// EndPos returns the line and column of the span end.
func (s Span) EndPos() Position {
	if s.File == nil {
		return Position{}
	}
	return s.File.Position(s.End)
}

// Join returns the smallest span covering both s and o. If either is invalid,
// the other is returned.
func (s Span) Join(o Span) Span {
	if !s.IsValid() {
		return o
	}
	if !o.IsValid() || o.File != s.File {
		return s
	}
	r := s
	if o.Start < r.Start {
		r.Start = o.Start
	}
	if o.End > r.End {
		r.End = o.End
	}
	return r
}

// At returns an empty span at the start of s.
func (s Span) At() Span { return Span{File: s.File, Start: s.Start, End: s.Start} }

// EndSpan returns an empty span at the end of s.
func (s Span) EndSpan() Span { return Span{File: s.File, Start: s.End, End: s.End} }

// Contains reports whether o lies within s.
func (s Span) Contains(o Span) bool {
	return s.File != nil && s.File == o.File && s.Start <= o.Start && o.End <= s.End
}

// LineBlank reports whether a 1-based line contains only white space.
func (f *File) LineBlank(line int) bool {
	return strings.TrimSpace(f.LineText(line)) == ""
}
