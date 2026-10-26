// Package format formats Lyth source code in its canonical layout, as gofmt
// does for Go: four spaces of indentation per level of nesting, one space
// between tokens where Lyth's style has one and none where it has none,
// trailing comments of consecutive lines aligned, and at most one blank line
// in a row.
//
// Lyth is newline-sensitive, so the formatter changes only whitespace inside
// lines and the number of blank lines: it never adds or removes a token or a
// line break between tokens, and it keeps the spacing of the token pairs the
// parser reads differently with and without a space (Grid [3]int, outer@,
// <-chan). Before returning, it parses the result and compares its syntax
// tree with the original's; a difference is an error, so formatting cannot
// change what a program means.
package format

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
)

// Indent is one level of indentation.
const Indent = "    "

// Source formats a Lyth file. It fails when the file has syntax errors, or
// when formatting would change its syntax tree (a bug in the formatter).
func Source(name string, src []byte) ([]byte, error) {
	sink := &diag.Sink{}
	f := source.NewFile(name, name, src)
	tree := syntax.Parse(f, sink)
	if d := firstError(sink); d != nil {
		pos := d.Span.StartPos()
		return nil, fmt.Errorf("%s:%d:%d: %s", name, pos.Line, pos.Col, d.Message)
	}
	toks, comments := syntax.Lex(f, &diag.Sink{})
	names := inNames(tree)
	for i, t := range toks {
		if t.Kind == syntax.IN && names[t.Span.Start] {
			toks[i].Kind = syntax.IDENT // in as a name (ADR 0044): a parameter, a field
		}
	}
	out := (&formatter{src: src, file: f, literals: literalBraces(tree, toks)}).format(toks, comments)
	// The result must mean the same: the same syntax tree, positions aside.
	sink2 := &diag.Sink{}
	f2 := source.NewFile(name, name, out)
	tree2 := syntax.Parse(f2, sink2)
	if d := firstError(sink2); d != nil || syntax.DumpShape(tree) != syntax.DumpShape(tree2) {
		return nil, fmt.Errorf("%s: formatting would change the program; the file is left as it is (please report this)", name)
	}
	return out, nil
}

func firstError(sink *diag.Sink) *diag.Diagnostic {
	for _, d := range sink.All() {
		if d.Severity == diag.Error {
			return d
		}
	}
	return nil
}

// unit is a token, a whole string template (kept as written), or a comment.
type unit struct {
	kind    syntax.Kind
	text    string
	start   int
	end     int
	comment bool
	line    int // line of start (1-based)
	endLine int // line of end
}

// frame is an open bracket.
type frame struct {
	kind        syntax.Kind // LPAREN, LBRACK, LBRACE; LBRACE for the file itself
	opener      int         // indentation level of its closing bracket
	content     int         // level of the lines inside it
	stmt        int         // level of the statement being formatted in it
	stmtFirst   syntax.Kind // first token of that statement
	stmtLabeled bool        // the statement is a labeled loop (outer@ for)
	fresh       bool        // no line has started inside it yet
	tight       bool        // a literal's braces: no spaces inside
	index       bool        // [] after an operand: slice colons have no spaces
	header      bool        // within a declaration's header (type parameters)
	structBody  bool        // a struct's fields: their tags are aligned
}

type formatter struct {
	src  []byte
	file *source.File
	// literals holds the offsets of the { of typed literals ([]int{1, 2},
	// map[K]V{...}), which have no spaces inside or before them.
	literals map[int]bool
}

// inNames finds the identifiers spelled in: the contextual keyword used as a
// name.
func inNames(tree *syntax.File) map[int]bool {
	out := map[int]bool{}
	visit := func(n syntax.Node) {
		if id, ok := n.(*syntax.Ident); ok && id.Name == "in" {
			out[id.S.Start] = true
		}
	}
	for _, d := range tree.Decls {
		syntax.Walk(d, visit)
		if fd, ok := d.(*syntax.FunDecl); ok {
			for _, p := range fd.Params {
				visit(p.Name)
			}
		}
		if sd, ok := d.(*syntax.StructDecl); ok {
			for _, f := range sd.Fields {
				if f.Name != nil {
					visit(f.Name)
				}
			}
		}
	}
	return out
}

// literalBraces finds the { of the file's typed literals.
func literalBraces(tree *syntax.File, toks []syntax.Token) map[int]bool {
	out := map[int]bool{}
	brace := func(after int) {
		for _, t := range toks {
			if t.Span.Start >= after && t.Kind == syntax.LBRACE {
				out[t.Span.Start] = true
				return
			}
		}
	}
	visit := func(n syntax.Node) {
		switch n := n.(type) {
		case *syntax.ListLit:
			if n.Type != nil {
				brace(n.Type.Span().End)
			}
		case *syntax.MapLit:
			if n.Type != nil {
				brace(n.Type.Span().End)
			}
		}
	}
	for _, d := range tree.Decls {
		switch d := d.(type) {
		case *syntax.PropDecl:
			if d.Value != nil {
				syntax.Walk(d.Value, visit)
			}
		case *syntax.ConstDecl:
			syntax.Walk(d.Value, visit)
		case *syntax.FunDecl:
			for _, p := range d.Params {
				if p.Default != nil {
					syntax.Walk(p.Default, visit)
				}
			}
			syntax.Walk(d, visit)
		default:
			syntax.Walk(d, visit)
		}
	}
	return out
}

// line is one output line in the making.
type line struct {
	level    int
	text     string // the code, without indentation
	tag      string // a struct field's tag, aligned with the tags around it
	comment  string // a trailing // comment
	verbatim bool   // the line belongs to a multi-line token: kept as is
	blank    bool
}

func (fm *formatter) format(toks []syntax.Token, comments []syntax.Comment) []byte {
	units := fm.units(toks, comments)
	lines := fm.layout(units)
	return render(lines)
}

// units merges tokens and comments in source order, without NEWLINE and EOF
// tokens, folding each string template into one unit.
func (fm *formatter) units(toks []syntax.Token, comments []syntax.Comment) []unit {
	var out []unit
	ci := 0
	addComment := func(c syntax.Comment) {
		out = append(out, unit{kind: syntax.ILLEGAL, text: strings.TrimRight(c.Text, " \t\r"), start: c.Span.Start, end: c.Span.End, comment: true})
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.Kind == syntax.NEWLINE || t.Kind == syntax.EOF {
			continue
		}
		for ci < len(comments) && comments[ci].Span.Start < t.Span.Start {
			addComment(comments[ci])
			ci++
		}
		if t.Kind == syntax.TEMPLATE_START {
			depth := 0
			j := i
			for ; j < len(toks); j++ {
				switch toks[j].Kind {
				case syntax.TEMPLATE_START:
					depth++
				case syntax.TEMPLATE_END:
					depth--
				}
				if depth == 0 {
					break
				}
			}
			if j >= len(toks) {
				j = len(toks) - 1
			}
			end := toks[j].Span.End
			out = append(out, unit{kind: syntax.STRING, text: string(fm.src[t.Span.Start:end]), start: t.Span.Start, end: end})
			// Comments cannot be inside a string.
			i = j
			continue
		}
		out = append(out, unit{kind: t.Kind, text: t.Text, start: t.Span.Start, end: t.Span.End})
	}
	for ; ci < len(comments); ci++ {
		addComment(comments[ci])
	}
	for i := range out {
		out[i].line = fm.file.Position(out[i].start).Line
		out[i].endLine = fm.file.Position(max(out[i].start, out[i].end-1)).Line
		if out[i].end == out[i].start {
			out[i].endLine = out[i].line
		}
	}
	return out
}

func isOpener(k syntax.Kind) bool {
	return k == syntax.LPAREN || k == syntax.LBRACK || k == syntax.LBRACE
}

func isCloser(k syntax.Kind) bool {
	return k == syntax.RPAREN || k == syntax.RBRACK || k == syntax.RBRACE
}

// endsOperand reports whether a unit of kind k can end an operand, so that
// a following - or * is binary.
func endsOperand(k syntax.Kind) bool {
	switch k {
	case syntax.IDENT, syntax.INT, syntax.FLOAT, syntax.RUNE, syntax.STRING, syntax.RAWSTRING,
		syntax.RPAREN, syntax.RBRACK, syntax.RBRACE, syntax.THIS, syntax.NULL, syntax.TRUE, syntax.FALSE,
		syntax.NOT_NOT, syntax.QUESTION, syntax.INC, syntax.DEC:
		return true
	}
	return false
}

// continuesAfter reports whether a line ending in k continues on the next
// line, as the parser reads it: after a binary operator, a dot, or an arrow.
func continuesAfter(k syntax.Kind) bool {
	switch k {
	case syntax.ADD, syntax.SUB, syntax.MUL, syntax.QUO, syntax.REM, syntax.AND, syntax.OR, syntax.XOR,
		syntax.SHL, syntax.SHR, syntax.AND_NOT, syntax.LAND, syntax.LOR, syntax.NOT,
		syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ,
		syntax.ASSIGN, syntax.ADD_ASSIGN, syntax.SUB_ASSIGN, syntax.MUL_ASSIGN, syntax.QUO_ASSIGN,
		syntax.REM_ASSIGN, syntax.AND_ASSIGN, syntax.OR_ASSIGN, syntax.XOR_ASSIGN, syntax.SHL_ASSIGN,
		syntax.SHR_ASSIGN, syntax.AND_NOT_ASSIGN, syntax.DEFINE,
		syntax.DOT, syntax.SAFE_DOT, syntax.ELVIS, syntax.RANGE, syntax.RANGE_EXCL, syntax.ARROW, syntax.COLON,
		syntax.IN, syntax.IS, syntax.AS:
		return true
	}
	return false
}

// continuesBefore reports whether a line starting with k continues the
// previous one: chains and operators placed at the start of a line.
func continuesBefore(k syntax.Kind) bool {
	switch k {
	case syntax.DOT, syntax.SAFE_DOT, syntax.ELVIS, syntax.LAND, syntax.LOR, syntax.CATCH:
		return true
	}
	return false
}

// startsHeader reports whether a statement starting with k is followed by a
// block whose body Go indents at the statement's level + 1 even when the
// header spans lines: if, loops, when, select, and declarations.
func startsHeader(k syntax.Kind) bool {
	switch k {
	case syntax.IF, syntax.ELSE, syntax.FOR, syntax.WHILE, syntax.WHEN, syntax.SELECT, syntax.FUN,
		syntax.STRUCT, syntax.INTERFACE, syntax.SEALED, syntax.ENUM:
		return true
	}
	return false
}

// layout assigns each source line its indentation level and spacing.
func (fm *formatter) layout(units []unit) []line {
	nlines := fm.file.LineCount()
	byLine := make([][]int, nlines+2) // unit indexes starting on each line
	for i, u := range units {
		byLine[u.line] = append(byLine[u.line], i)
	}
	// Lines covered by a multi-line token or comment after its first line.
	inside := make([]int, nlines+2) // index+1 of the unit covering the line
	for i, u := range units {
		for l := u.line + 1; l <= u.endLine && l <= nlines; l++ {
			inside[l] = i + 1
		}
	}
	stack := []*frame{{kind: syntax.LBRACE, fresh: true}}
	var out []line
	prev := -1 // index of the last code unit before the current line
	sp := &spacer{fm: fm, units: units}
	pendingComments := []int{} // indexes in out of comment-only lines awaiting a level
	lastLevel := 0             // level of the last formatted line
	for ln := 1; ln <= nlines; ln++ {
		idx := byLine[ln]
		if c := inside[ln]; c > 0 {
			// The line starts inside a multi-line raw string or comment: it
			// is kept as written up to that token's end.
			u := units[c-1]
			lineStart := fm.file.LineStart(ln)
			cut := lineStart + len(fm.lineText(ln))
			if u.endLine == ln {
				cut = u.end
			}
			text := string(fm.src[lineStart:cut])
			var rest []int
			for _, i := range idx {
				rest = append(rest, i)
			}
			if len(rest) > 0 {
				code, comment := sp.join(rest, stack, c-1)
				text += code
				if comment != "" {
					text += " " + comment
				}
				for _, i := range rest {
					if !units[i].comment {
						prev = i
					}
				}
				stack = sp.push(rest, stack, lastLevel)
			} else if !u.comment && u.endLine == ln {
				prev = c - 1
			}
			// Only a carriage return is dropped: Lyth, like Go, removes it
			// from raw strings, while spaces inside one are its value.
			out = append(out, line{verbatim: true, text: strings.TrimSuffix(text, "\r")})
			continue
		}
		if len(idx) == 0 {
			out = append(out, line{blank: true})
			continue
		}
		first := units[idx[0]]
		top := stack[len(stack)-1]
		if first.comment && allComments(units, idx) {
			out = append(out, line{text: joinComments(units, idx)})
			pendingComments = append(pendingComments, len(out)-1)
			continue
		}
		// The first code unit of the line decides its level.
		fi := idx[0]
		for units[fi].comment {
			fi++
		}
		first = units[fi]
		var level int
		switch {
		case isCloser(first.kind):
			level = top.opener
		case top.kind != syntax.LBRACE:
			level = top.content
		case top.fresh || prev < 0:
			level = top.content
			startStmt(top, level, units, fi)
		case prev >= 0 && (units[prev].kind == syntax.COMMA || units[prev].kind == syntax.SEMI):
			level = top.content
		case first.kind == syntax.ELSE:
			level = top.stmt
		case prev >= 0 && continuesAfter(units[prev].kind) || continuesBefore(first.kind):
			level = top.stmt + 1
		default:
			level = top.content
			startStmt(top, level, units, fi)
		}
		top.fresh = false
		for _, pi := range pendingComments {
			// A comment above a line goes where the line goes, but one above
			// a closing bracket stays inside the block.
			out[pi].level = level
			if isCloser(first.kind) {
				out[pi].level = top.content
			}
		}
		pendingComments = pendingComments[:0]
		code, comment := sp.join(idx, stack, -1)
		l := line{level: level, text: code, comment: comment}
		if top.structBody {
			// Name: Type `json:"name"`: the tag goes in a column.
			last := -1
			for _, i := range idx {
				if !units[i].comment {
					last = i
				}
			}
			if last > idx[0] && units[last].kind == syntax.RAWSTRING && units[last].endLine == units[last].line &&
				strings.HasSuffix(code, " "+units[last].text) {
				l.text = strings.TrimSuffix(code, " "+units[last].text)
				l.tag = units[last].text
			}
		}
		out = append(out, l)
		lastLevel = level
		for _, i := range idx {
			if !units[i].comment {
				prev = i
			}
		}
		stack = sp.push(idx, stack, level)
	}
	for _, pi := range pendingComments {
		out[pi].level = stack[len(stack)-1].content
	}
	return out
}

func startStmt(f *frame, level int, units []unit, fi int) {
	f.stmt = level
	f.stmtFirst = units[fi].kind
	f.stmtLabeled = units[fi].kind == syntax.IDENT && fi+1 < len(units) && units[fi+1].kind == syntax.AT && units[fi+1].start == units[fi].end
}

func allComments(units []unit, idx []int) bool {
	for _, i := range idx {
		if !units[i].comment {
			return false
		}
	}
	return true
}

func joinComments(units []unit, idx []int) string {
	var parts []string
	for _, i := range idx {
		parts = append(parts, units[i].text)
	}
	return strings.Join(parts, " ")
}

// lineText returns the source text of line ln without its newline.
func (fm *formatter) lineText(ln int) string {
	start := fm.file.LineStart(ln)
	end := len(fm.src)
	if ln < fm.file.LineCount() {
		end = fm.file.LineStart(ln+1) - 1
	}
	if end < start {
		end = start
	}
	return strings.TrimSuffix(string(fm.src[start:end]), "\r")
}

// spacer computes the spacing between the units of a line.
type spacer struct {
	fm    *formatter
	units []unit
}

// headerBrackets returns the [ of a line that open type parameters: after
// func, struct, interface, type, or enum, a [ written directly after a name
// before the declaration's ( or body (func Max[T: C], struct Page[T]).
func (sp *spacer) headerBrackets(idx []int) map[int]bool {
	units := sp.units
	out := map[int]bool{}
	header, depth := false, 0
	for _, i := range idx {
		u := units[i]
		if u.comment {
			continue
		}
		switch u.kind {
		case syntax.FUN, syntax.STRUCT, syntax.INTERFACE, syntax.TYPE, syntax.ENUM:
			header, depth = true, 0
		case syntax.LBRACK:
			if header && depth == 0 && i > 0 && units[i-1].kind == syntax.IDENT && units[i-1].end == u.start {
				out[i] = true
			}
			depth++
		case syntax.RBRACK:
			depth--
		case syntax.LPAREN, syntax.LBRACE, syntax.ASSIGN:
			if depth == 0 {
				header = false
			}
		}
	}
	return out
}

// newFrame opens the bracket units[i] inside top.
func (sp *spacer) newFrame(i int, top *frame, typeParams bool) *frame {
	units := sp.units
	u := units[i]
	f := &frame{kind: u.kind}
	switch u.kind {
	case syntax.LBRACE:
		f.tight = sp.tightBrace(i)
	case syntax.LBRACK:
		f.header = typeParams
		f.index = !typeParams && i > 0 && endsOperand(units[i-1].kind)
	}
	return f
}

// push updates the bracket stack with the units of a line at level.
func (sp *spacer) push(idx []int, stack []*frame, level int) []*frame {
	units := sp.units
	tps := sp.headerBrackets(idx)
	for _, i := range idx {
		u := units[i]
		if u.comment {
			continue
		}
		switch {
		case isOpener(u.kind):
			top := stack[len(stack)-1]
			opener := level
			if u.kind == syntax.LBRACE && top.kind == syntax.LBRACE &&
				(startsHeader(top.stmtFirst) || top.stmtLabeled) && !top.fresh {
				// if a &&
				//     b {
				//     body          <- the statement's level + 1, as gofmt does
				// }
				opener = top.stmt
			}
			f := sp.newFrame(i, top, tps[i])
			f.opener, f.content, f.stmt, f.fresh = opener, opener+1, opener+1, true
			f.structBody = u.kind == syntax.LBRACE && top.stmtFirst == syntax.STRUCT && !f.tight
			stack = append(stack, f)
		case isCloser(u.kind):
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return stack
}

// tightBrace reports whether the { units[i] opens a literal, whose braces
// have no spaces inside: a typed literal ([]int{1, 2}), struct{}, or the
// value struct{}{}.
func (sp *spacer) tightBrace(i int) bool {
	units := sp.units
	if sp.fm.literals[units[i].start] {
		return true
	}
	if i > 0 && units[i-1].kind == syntax.STRUCT {
		return true
	}
	return i >= 3 && units[i-1].kind == syntax.RBRACE && units[i-2].kind == syntax.LBRACE && units[i-3].kind == syntax.STRUCT
}

// join renders the units idx of one line with Lyth's spacing; skip is the
// index of a unit already written (a multi-line token the line starts in),
// or -1. It returns the code and a trailing // comment.
func (sp *spacer) join(idx []int, stack []*frame, skip int) (string, string) {
	units := sp.units
	var b strings.Builder
	trailing := ""
	tps := sp.headerBrackets(idx)
	local := append([]*frame(nil), stack...) // brackets opened on this line too
	prevIdx := skip
	structHeader := false // struct Circle : Shape
	for n, i := range idx {
		u := units[i]
		if u.comment && n == len(idx)-1 && strings.HasPrefix(u.text, "//") && (n > 0 || skip >= 0) {
			trailing = u.text
			break
		}
		if prevIdx >= 0 && (n > 0 || skip >= 0) {
			b.WriteString(sp.gap(&units[prevIdx], &u, prevIdx, i, local, structHeader))
		}
		text := u.text
		if u.endLine > u.line {
			// A raw string or comment spanning lines: its other lines are
			// written as they are (layout).
			text = strings.TrimSuffix(text[:strings.IndexByte(text, '\n')], "\r")
		}
		b.WriteString(text)
		if !u.comment {
			switch u.kind {
			case syntax.STRUCT:
				structHeader = true
			case syntax.LBRACE, syntax.ASSIGN:
				structHeader = false
			}
			switch {
			case isOpener(u.kind):
				local = append(local, sp.newFrame(i, local[len(local)-1], tps[i]))
			case isCloser(u.kind):
				if len(local) > 1 {
					local = local[:len(local)-1]
				}
			}
		}
		prevIdx = i
	}
	return b.String(), trailing
}

// gap returns the whitespace between units a and b of one line.
func (sp *spacer) gap(a, b *unit, ai, bi int, stack []*frame, structHeader bool) string {
	units := sp.units
	orig := sp.fm.src[a.end:b.start]
	adjacent := len(orig) == 0
	keep := func() string {
		if adjacent {
			return ""
		}
		return " "
	}
	if a.comment || b.comment {
		if b.comment && b.kind == syntax.ILLEGAL && (strings.HasPrefix(b.text, "/*")) && adjacent {
			return ""
		}
		return " "
	}
	ak, bk := a.kind, b.kind
	top := stack[len(stack)-1]
	// Pairs the parser reads differently with and without a space keep
	// their spacing: Grid [3]int and Set[T], Point{ and a lambda's {, the
	// label outer@ and break@outer, as?, and <-chan.
	switch {
	case bk == syntax.LBRACE && sp.tightBrace(bi):
		return "" // []int{1, 2}, struct{}{}
	case bk == syntax.LBRACK && endsOperand(ak):
		return keep()
	case ak == syntax.RBRACK && startsType(bk):
		return keep()
	case bk == syntax.AT || ak == syntax.AT && bk == syntax.IDENT:
		return keep()
	case ak == syntax.AS && bk == syntax.QUESTION:
		return keep()
	case sp.arrowPart(ai) || sp.arrowPart(bi) && bk == syntax.LSS:
		return keep()
	}
	// Never join two tokens into one: - - x is not --x.
	merge := func(s string) string {
		if s == "" && !adjacent && joins(a.text, b.text) {
			return " "
		}
		return s
	}
	switch {
	case bk == syntax.COMMA || bk == syntax.SEMI:
		return merge("")
	case ak == syntax.COMMA || ak == syntax.SEMI:
		if bk == syntax.RPAREN || bk == syntax.RBRACK {
			return ""
		}
		return " "
	case ak == syntax.LPAREN || ak == syntax.LBRACK:
		return merge("")
	case bk == syntax.RPAREN || bk == syntax.RBRACK:
		return merge("")
	case ak == syntax.LBRACE:
		if bk == syntax.RBRACE || top.tight {
			return merge("")
		}
		return " "
	case bk == syntax.RBRACE:
		if ak == syntax.LBRACE || top.tight {
			return merge("")
		}
		return " "
	case bk == syntax.DOT || bk == syntax.SAFE_DOT || ak == syntax.DOT || ak == syntax.SAFE_DOT:
		return merge("")
	case bk == syntax.QUESTION || bk == syntax.NOT_NOT || bk == syntax.INC || bk == syntax.DEC:
		return merge("")
	case bk == syntax.ELLIPSIS:
		if endsOperand(ak) {
			return merge("") // xs...
		}
		return " "
	case ak == syntax.ELLIPSIS:
		return merge("") // ...int
	case bk == syntax.RANGE || bk == syntax.RANGE_EXCL || ak == syntax.RANGE || ak == syntax.RANGE_EXCL:
		return merge("")
	case bk == syntax.COLON:
		if structHeader && top.kind == syntax.LBRACE && !top.header {
			return " " // struct Circle : Shape
		}
		if top.index {
			return merge("") // xs[a:b]
		}
		return merge("")
	case ak == syntax.COLON:
		if top.index {
			return merge("") // xs[a:b]
		}
		if bk == syntax.RBRACK {
			return ""
		}
		return " "
	case bk == syntax.LPAREN:
		if ak == syntax.IDENT && ai > 0 && units[ai-1].kind == syntax.TYPE {
			return " " // type Handler (Ctx) -> unit
		}
		if endsOperand(ak) || ak == syntax.CHAN {
			return merge("") // f(x), xs[0](x)
		}
		if isUnary(units, ai) {
			return merge("")
		}
		return " "
	case bk == syntax.LBRACK:
		if ak == syntax.MAP {
			return ""
		}
		if isUnary(units, ai) {
			return merge("") // *[]int
		}
		return " "
	case bk == syntax.LBRACE:
		if ak == syntax.STRUCT {
			return keep()
		}
		return " "
	case ak == syntax.NOT && (bk == syntax.IN || bk == syntax.IS):
		return merge("") // !in
	case bk == syntax.NOT && bi+1 < len(units) && (units[bi+1].kind == syntax.IN || units[bi+1].kind == syntax.IS):
		return " "
	}
	// Unary operators touch their operand: -x, !ok, *T, &v.
	if isUnary(units, ai) {
		return merge("")
	}
	// Arithmetic keeps the author's spacing, the same on both sides of the
	// operator: a*b + c groups by precedence, as gofmt writes it.
	if isArith(bk) && !isUnary(units, bi) {
		return sp.arithGap(bi)
	}
	if isArith(ak) {
		return sp.arithGap(ai)
	}
	if isOperator(ak) || isOperator(bk) {
		return " "
	}
	return " "
}

func isArith(k syntax.Kind) bool {
	switch k {
	case syntax.ADD, syntax.SUB, syntax.MUL, syntax.QUO, syntax.REM, syntax.AND, syntax.OR, syntax.XOR,
		syntax.SHL, syntax.SHR, syntax.AND_NOT:
		return true
	}
	return false
}

// arithGap is the space on both sides of the binary operator units[i]: one
// when the author put one on either side, none otherwise.
func (sp *spacer) arithGap(i int) string {
	units, src := sp.units, sp.fm.src
	if i > 0 && units[i-1].end < units[i].start || i+1 < len(units) && units[i].end < units[i+1].start {
		return " "
	}
	_ = src
	return ""
}

// arrowPart reports whether unit i is the < or - of a <- written as one
// (<-chan T, chan<- T, or the comparison x<-1): its spacing is kept.
func (sp *spacer) arrowPart(i int) bool {
	units := sp.units
	if i < 0 || i >= len(units) {
		return false
	}
	switch units[i].kind {
	case syntax.LSS:
		return i+1 < len(units) && units[i+1].kind == syntax.SUB && units[i+1].start == units[i].end
	case syntax.SUB:
		return i > 0 && units[i-1].kind == syntax.LSS && units[i-1].end == units[i].start
	}
	return false
}

// isUnary reports whether unit i is a prefix operator: an operator that
// can be prefix, not preceded by an operand.
func isUnary(units []unit, i int) bool {
	if i < 0 {
		return false
	}
	switch units[i].kind {
	case syntax.SUB, syntax.ADD, syntax.MUL, syntax.AND, syntax.XOR:
		if i == 0 || units[i-1].endLine < units[i].line && !continuesAfter(units[i-1].kind) {
			return true // the start of a statement: -2, *p = v
		}
		prev := units[i-1]
		for j := i - 1; prev.comment && j > 0; j-- {
			prev = units[j-1]
		}
		if prev.kind == syntax.QUESTION && i >= 2 && units[i-2].kind == syntax.AS && units[i-2].end == prev.start {
			return true // as? *T
		}
		if prev.kind == syntax.RBRACK && prev.end == units[i].start && (units[i].kind == syntax.MUL || units[i].kind == syntax.AND) {
			return true // []*T, map[K]*V
		}
		return !endsOperand(prev.kind)
	case syntax.NOT:
		return true
	}
	return false
}

func isOperator(k syntax.Kind) bool {
	return k > syntax.ILLEGAL && k.String() != "token" && !k.IsKeyword() && k != syntax.IDENT &&
		k != syntax.INT && k != syntax.FLOAT && k != syntax.RUNE && k != syntax.STRING && k != syntax.RAWSTRING
}

// startsType reports whether k can start a type written directly after ]:
// []int, [3]*T, map[K]V, [][]int.
func startsType(k syntax.Kind) bool {
	switch k {
	case syntax.IDENT, syntax.MUL, syntax.LPAREN, syntax.LBRACK, syntax.MAP, syntax.CHAN, syntax.STRUCT,
		syntax.INTERFACE, syntax.FUN, syntax.LSS:
		return true
	}
	return false
}

// joins reports whether two tokens written without a space between them
// would read differently: as one identifier or number, or as a longer
// operator (- - becomes --).
func joins(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, _ := utf8.DecodeLastRuneInString(a)
	rb, _ := utf8.DecodeRuneInString(b)
	word := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	if word(ra) && word(rb) {
		return true
	}
	op := func(r rune) bool { return strings.ContainsRune("+-*/%&|^<>=!.?:@~", r) }
	if op(ra) && op(rb) {
		return true
	}
	if ra == '/' && (rb == '/' || rb == '*') {
		return true
	}
	return false
}

// render writes the lines: indentation, trailing comments aligned within
// runs of consecutive lines at one level, blank lines collapsed, and one
// final newline.
func render(lines []line) []byte {
	// Collapse blank lines: none at the start, at most one in a row.
	var kept []line
	for _, l := range lines {
		if l.blank && (len(kept) == 0 || kept[len(kept)-1].blank) {
			continue
		}
		kept = append(kept, l)
	}
	for len(kept) > 0 && kept[len(kept)-1].blank {
		kept = kept[:len(kept)-1]
	}
	// Align the tags of consecutive fields, then trailing comments.
	for i := 0; i < len(kept); {
		if kept[i].tag == "" {
			i++
			continue
		}
		j, width := i, 0
		for j < len(kept) && kept[j].tag != "" && kept[j].level == kept[i].level {
			width = max(width, utf8.RuneCountInString(kept[j].text))
			j++
		}
		for k := i; k < j; k++ {
			kept[k].text += strings.Repeat(" ", width-utf8.RuneCountInString(kept[k].text)+1) + kept[k].tag
			kept[k].tag = ""
		}
		i = j
	}
	// Align trailing comments of consecutive lines at the same level.
	for i := 0; i < len(kept); {
		if kept[i].comment == "" || kept[i].verbatim {
			i++
			continue
		}
		j := i
		width := 0
		for j < len(kept) && kept[j].comment != "" && !kept[j].verbatim && kept[j].level == kept[i].level && kept[j].text != "" {
			width = max(width, utf8.RuneCountInString(kept[j].text))
			j++
		}
		for k := i; k < j; k++ {
			pad := width - utf8.RuneCountInString(kept[k].text) + 1
			kept[k].text += strings.Repeat(" ", pad) + kept[k].comment
			kept[k].comment = ""
		}
		if j == i {
			j++
		}
		i = j
	}
	var b bytes.Buffer
	for _, l := range kept {
		switch {
		case l.blank:
		case l.verbatim:
			b.WriteString(l.text)
		default:
			b.WriteString(strings.Repeat(Indent, l.level))
			b.WriteString(l.text)
			if l.comment != "" {
				b.WriteString(" " + l.comment)
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}
