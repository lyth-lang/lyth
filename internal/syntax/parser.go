package syntax

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// Parse lexes and parses one file. It always returns a File; syntax errors
// are reported to sink and represented by Bad* nodes.
func Parse(file *source.File, sink *diag.Sink) *File {
	toks, comments := Lex(file, sink)
	p := &parser{file: file, toks: toks, sink: sink, comments: comments, lastErr: -1}
	return p.parseFile()
}

// ParseExpr parses a standalone expression (used by tests and tools).
func ParseExpr(file *source.File, sink *diag.Sink) Expr {
	toks, comments := Lex(file, sink)
	p := &parser{file: file, toks: toks, sink: sink, comments: comments, lastErr: -1}
	p.skipNL()
	x := p.parseExpr()
	p.skipNL()
	if !p.at(EOF) {
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "unexpected %s after expression", describe(p.tok()))
	}
	return x
}

type parser struct {
	file     *source.File
	toks     []Token
	pos      int
	sink     *diag.Sink
	comments []Comment
	lastErr  int // token index of the last reported error (cascade suppression)
	noLambda int // >0 disables trailing lambdas (in a header: if, while, for, when)
	damaged  []source.Span
	// receiverType is set while parsing a method's receiver type, where
	// []T.Map[T, R] puts the function's type parameters after a qualified
	// name rather than type arguments.
	receiverType bool
	// recoveredTPs holds type parameters written func k<T>(...), reported
	// and kept for the function being parsed.
	recoveredTPs []*TypeParam
}

// afterBrackets returns the token after the ']' matching the '[' at index i.
func (p *parser) afterBrackets(i int) Token {
	depth := 0
	for ; i < len(p.toks); i++ {
		switch p.toks[i].Kind {
		case LBRACK:
			depth++
		case RBRACK:
			depth--
			if depth == 0 {
				if i+1 < len(p.toks) {
					return p.toks[i+1]
				}
				return p.toks[len(p.toks)-1]
			}
		case NEWLINE, LBRACE, EOF:
			return p.toks[i]
		}
	}
	return p.toks[len(p.toks)-1]
}

// nested runs f with trailing lambdas allowed again, inside brackets of a
// header expression, and returns its result.
func nested[T any](p *parser, f func() T) T {
	saved := p.noLambda
	p.noLambda = 0
	defer func() { p.noLambda = saved }()
	return f()
}

// ---------------------------------------------------------------------------
// Token helpers

func (p *parser) tok() Token { return p.toks[p.pos] }

func (p *parser) peek(n int) Token {
	if p.pos+n < len(p.toks) {
		return p.toks[p.pos+n]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) at(k Kind) bool { return p.toks[p.pos].Kind == k }

func (p *parser) next() Token {
	t := p.toks[p.pos]
	if t.Kind != EOF {
		p.pos++
	}
	return t
}

func (p *parser) accept(k Kind) bool {
	if p.at(k) {
		p.next()
		return true
	}
	return false
}

func (p *parser) skipNL() {
	for p.at(NEWLINE) {
		p.pos++
	}
}

func (p *parser) skipNLSemi() {
	for p.at(NEWLINE) || p.at(SEMI) {
		p.pos++
	}
}

// peekPastNL returns the first non-NEWLINE token at or after the cursor.
func (p *parser) peekPastNL() Token {
	i := p.pos
	for i < len(p.toks)-1 && p.toks[i].Kind == NEWLINE {
		i++
	}
	return p.toks[i]
}

// continueLine skips newlines when the next line starts with one of kinds.
func (p *parser) continueLine(kinds ...Kind) {
	if !p.at(NEWLINE) {
		return
	}
	next := p.peekPastNL()
	for _, k := range kinds {
		if next.Kind == k {
			p.skipNL()
			return
		}
	}
}

func (p *parser) errorf(code diag.Code, span source.Span, format string, args ...any) *diag.Diagnostic {
	if p.pos == p.lastErr {
		// Suppress cascades at the same token; return a detached diagnostic.
		return &diag.Diagnostic{}
	}
	p.lastErr = p.pos
	return p.sink.Errorf(code, span, format, args...)
}

func describe(t Token) string {
	switch t.Kind {
	case IDENT:
		return fmt.Sprintf("identifier %s", t.Text)
	case NEWLINE:
		return "newline"
	case EOF:
		return "end of file"
	case INT, FLOAT, RUNE, STRING, RAWSTRING, TEMPLATE_START:
		return t.Kind.String()
	}
	if t.Kind.IsKeyword() {
		return fmt.Sprintf("keyword %s", t.Text)
	}
	return fmt.Sprintf("'%s'", t.Kind)
}

func (p *parser) expect(k Kind, context string) Token {
	if p.at(k) {
		return p.next()
	}
	t := p.tok()
	msg := fmt.Sprintf("expected '%s'", k)
	if context != "" {
		msg += " " + context
	}
	p.errorf(diag.EUnexpectedToken, t.Span, "%s, found %s", msg, describe(t))
	return Token{Kind: k, Span: t.Span.At()}
}

func (p *parser) parseIdent(context string) *Ident {
	t := p.tok()
	if t.Kind == IDENT {
		p.next()
		return &Ident{Name: t.Text, S: t.Span}
	}
	if t.Kind == THIS {
		p.errorf(diag.EUnexpectedToken, t.Span, "self is the method receiver; it cannot be declared or redeclared").
			WithHint("choose another name")
		p.next()
		return &Ident{Name: "_", S: t.Span}
	}
	if t.Kind.IsKeyword() && p.peek(1).Kind == COLON {
		// func f(in: int), struct fields, typed locals: the keyword names
		// what is declared; report it and read on.
		p.errorf(diag.EUnexpectedToken, t.Span, "%s is a keyword and cannot be a name", t.Text).
			WithHint("choose another name, such as %sValue", t.Text)
		p.next()
		return &Ident{Name: t.Text, S: t.Span}
	}
	d := p.errorf(diag.EUnexpectedToken, t.Span, "expected identifier %s, found %s", context, describe(t))
	if t.Kind.IsKeyword() {
		d.WithNote("'%s' is a keyword in Lyth", t.Text)
	}
	return &Ident{Name: "_", S: t.Span.At()}
}

func (p *parser) spanFrom(start source.Span) source.Span {
	end := p.toks[p.pos-1].Span
	if p.pos == 0 {
		end = start
	}
	return start.Join(end)
}

// ---------------------------------------------------------------------------
// Recovery

// markDamaged records the statement or declaration parsed from token start
// as damaged when a syntax error was reported in it (diagnostics after the
// first n) and no statement inside it was already recorded for that error.
// Rejected syntax the parser understands (E0005, E0008, ...) is not damage.
func (p *parser) markDamaged(start, n, inner int) {
	if len(p.damaged) == inner && p.syntaxErrorSince(n) {
		p.damaged = append(p.damaged, p.toks[start].Span.Join(p.toks[p.pos-1].Span))
	}
}

// syntaxErrorSince reports whether a syntax error the parser had to recover
// from is among the diagnostics after the first n.
func (p *parser) syntaxErrorSince(n int) bool {
	for _, d := range p.sink.Since(n) {
		switch d.Code {
		case diag.EInvalidChar, diag.EUnterminated, diag.EUnexpectedToken:
			return true
		}
	}
	return false
}

// syncStmt skips to the end of the current statement.
func (p *parser) syncStmt() {
	depth := 0
	for !p.at(EOF) {
		switch p.tok().Kind {
		case LBRACE:
			depth++
		case RBRACE:
			if depth == 0 {
				return
			}
			depth--
		case NEWLINE, SEMI:
			if depth == 0 {
				return
			}
		}
		p.next()
	}
}

func isDeclStart(k Kind) bool {
	switch k {
	case FUN, STRUCT, INTERFACE, ENUM, SEALED, VAL, VAR, CONST, PUB, PRIVATE, IMPORT, PACKAGE, TYPEALIAS:
		return true
	}
	return false
}

// syncDecl skips to the next top-level declaration.
func (p *parser) syncDecl() {
	depth := 0
	for !p.at(EOF) {
		k := p.tok().Kind
		switch k {
		case LBRACE:
			depth++
		case RBRACE:
			if depth > 0 {
				depth--
			}
		case NEWLINE:
			if depth == 0 && isDeclStart(p.peek(1).Kind) {
				p.next()
				return
			}
		}
		p.next()
	}
}

// ---------------------------------------------------------------------------
// Files

func (p *parser) parseFile() *File {
	f := &File{Src: p.file, Comments: p.comments, S: source.Span{File: p.file, Start: 0, End: len(p.file.Content)}}
	p.skipNLSemi()
	firstTok := p.tok().Span.Start
	for _, c := range p.comments {
		if c.Span.Start < firstTok && strings.HasPrefix(c.Text, "//go:build ") {
			f.BuildConstraint = c.Text
		}
	}
	if p.at(PACKAGE) {
		p.next()
		f.Package = p.parseIdent("after 'package'")
		p.endDecl()
	}
	for {
		p.skipNLSemi()
		if !p.at(IMPORT) {
			break
		}
		f.Imports = append(f.Imports, p.parseImport()...)
	}
	for {
		p.skipNLSemi()
		if p.at(EOF) {
			break
		}
		if p.at(IMPORT) {
			imps := p.parseImport()
			for _, imp := range imps {
				p.sink.Errorf(diag.EUnexpectedToken, imp.S, "imports must come before declarations")
			}
			f.Imports = append(f.Imports, imps...)
			continue
		}
		start, n, inner := p.pos, p.sink.Len(), len(p.damaged)
		d := p.parseDecl()
		if d != nil {
			f.Decls = append(f.Decls, d)
		}
		if p.pos == start {
			p.next() // guarantee progress
		}
		p.markDamaged(start, n, inner)
	}
	f.Damaged = p.damaged
	return f
}

func (p *parser) endDecl() {
	if p.at(NEWLINE) || p.at(SEMI) || p.at(EOF) {
		p.skipNLSemi()
		return
	}
	p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected newline after declaration, found %s", describe(p.tok()))
	p.syncDecl()
}

// parseConst parses `const Name [: Type] = value` from the const keyword.
func (p *parser) parseConst(start source.Span) *ConstDecl {
	p.next() // const
	d := &ConstDecl{}
	d.Name = p.parseIdent("in const declaration")
	if p.accept(COLON) {
		d.Type = p.parseType()
	}
	p.expect(ASSIGN, "in const declaration")
	p.skipNL()
	d.Value = p.parseExpr()
	d.S = p.spanFrom(start)
	return d
}

// parseImport parses `import "path" [as name]` or a group
// `import ( "a"; "b" as c )`, one import per line.
func (p *parser) parseImport() []*ImportDecl {
	start := p.next().Span // import
	if !p.at(LPAREN) {
		d := p.parseImportSpec(start, false)
		p.endDecl()
		return []*ImportDecl{d}
	}
	p.next()
	var out []*ImportDecl
	for {
		p.skipNLSemi()
		if p.at(RPAREN) || p.at(EOF) {
			break
		}
		d := p.parseImportSpec(p.tok().Span, true)
		out = append(out, d)
		// The lexer emits no newlines inside parentheses: one import per
		// line means the next one starts on a later line.
		prev := p.toks[p.pos-1].Span
		if !p.at(SEMI) && !p.at(RPAREN) && !p.at(EOF) && p.file.Position(p.tok().Span.Start).Line == p.file.Position(prev.End).Line {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "one import per line, found %s", describe(p.tok()))
			for line := p.file.Position(prev.End).Line; !p.at(RPAREN) && !p.at(EOF) && p.file.Position(p.tok().Span.Start).Line == line; {
				p.next()
			}
		}
	}
	p.expect(RPAREN, "to close the import group")
	p.endDecl()
	return out
}

// parseImportSpec parses `"path" [as name]` starting at the path. In a
// group, recovery stops at the end of the line or the group.
func (p *parser) parseImportSpec(start source.Span, inGroup bool) *ImportDecl {
	d := &ImportDecl{}
	t := p.tok()
	sync := func() {
		if !inGroup {
			p.syncStmt()
			return
		}
		line := p.file.Position(t.Span.Start).Line
		for !p.at(RPAREN) && !p.at(EOF) && p.file.Position(p.tok().Span.Start).Line == line {
			p.next()
		}
	}
	switch t.Kind {
	case STRING, RAWSTRING:
		p.next()
		d.Path = t.Value
		d.PathSpan = t.Span
	case IDENT:
		if pt := p.peek(1); pt.Kind == STRING && pt.Value != "" && p.file.Position(pt.Span.Start).Line == p.file.Position(t.Span.Start).Line {
			// Go's renamed import: import h "net/http".
			p.next()
			p.next()
			d.Path, d.PathSpan = pt.Value, pt.Span
			d.Alias = &Ident{Name: t.Text, S: t.Span}
			p.errorf(diag.EUnexpectedToken, t.Span, "a renamed import is written \"path\" as name").
				WithFix(t.Span.Join(pt.Span), pt.Text+" as "+t.Text, "write %s as %s", pt.Text, t.Text)
			d.S = p.spanFrom(start)
			return d
		}
		dg := p.errorf(diag.EUnexpectedToken, t.Span, "import paths are quoted Go import paths")
		if p.peek(1).Kind == DOT {
			// Kotlin-style dotted import.
			dg.WithHint("write import \"net/http\" (use 'as name' to rename)")
		} else {
			dg.WithFix(t.Span, strconv.Quote(t.Text), "write %q", t.Text)
		}
		sync()
		d.S = p.spanFrom(start)
		return d
	case DOT:
		p.errorf(diag.EUnsupported, t.Span, "Lyth has no dot imports").
			WithHint("import the package and qualify its names: import \"math\", then math.Pi")
		sync()
		d.S = p.spanFrom(start)
		return d
	default:
		p.errorf(diag.EUnexpectedToken, t.Span, "expected import path string, found %s", describe(t))
		sync()
		d.S = p.spanFrom(start)
		return d
	}
	if p.accept(AS) {
		d.Alias = p.parseIdent("after 'as'")
	}
	d.S = p.spanFrom(start)
	return d
}

// docFor returns the doc comment immediately preceding offset off.
func (p *parser) docFor(off int) string {
	declLine := p.file.Position(off).Line
	if strings.TrimSpace(string(p.file.Content[p.file.LineStart(declLine):off])) != "" {
		return "" // not first on its line (struct T { N: int }): the comment above is not its doc
	}
	var lines []string
	expect := declLine - 1
	for i := len(p.comments) - 1; i >= 0; i-- {
		c := p.comments[i]
		if c.Span.Start >= off {
			continue
		}
		pos := p.file.Position(c.Span.Start)
		// a comment after code on its line (`ID: int64 // ...`) belongs to that code
		trailing := strings.TrimSpace(string(p.file.Content[p.file.LineStart(pos.Line):c.Span.Start])) != ""
		if pos.Line != expect || trailing || !strings.HasPrefix(c.Text, "//") || strings.HasPrefix(c.Text, "//go:") {
			if pos.Line < expect {
				break
			}
			continue
		}
		lines = append([]string{c.Text}, lines...)
		expect--
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Declarations

func (p *parser) parseDecl() (decl Decl) {
	startTok := p.tok()
	start := startTok.Span
	doc := p.docFor(start.Start)
	if p.at(PUB) || p.at(PRIVATE) {
		kw := p.next()
		defer func() { p.visibilityKeyword(kw, declName(decl)) }()
	}
	p.oldFun()
	switch p.tok().Kind {
	case FUN:
		d := p.parseFunDecl(start, doc)
		p.endDecl()
		return d
	case STRUCT:
		d := p.parseStructDecl(start, doc)
		p.endDecl()
		return d
	case INTERFACE:
		d := p.parseInterfaceDecl(start, doc)
		p.endDecl()
		return d
	case SEALED:
		kw := p.next()
		if !p.at(INTERFACE) {
			d := p.errorf(diag.EUnsupportedSyntax, kw.Span, "only interfaces can be sealed")
			if p.at(STRUCT) || p.at(IDENT) && p.tok().Text == "class" {
				d.WithHint("declare a sealed interface and let structs implement it: struct Circle : Shape")
			}
			p.syncDecl()
			return &BadDecl{S: p.spanFrom(start)}
		}
		d := p.parseInterfaceDecl(start, doc)
		d.Sealed = true
		p.endDecl()
		return d
	case ENUM:
		d := p.parseEnumDecl(start, doc)
		p.endDecl()
		return d
	case VAL, VAR:
		if kw := p.next(); kw.Kind == VAL {
			p.errorf(diag.EUnsupportedSyntax, kw.Span, "package-level variables are declared with var").
				WithFix(kw.Span, "var", "write var")
		}
		d := &PropDecl{Doc: doc, Mutable: true}
		d.Name = p.parseIdent("in variable declaration")
		if p.accept(COLON) {
			d.Type = p.parseType()
		}
		if p.accept(ASSIGN) {
			p.skipNL()
			d.Value = p.parseExpr()
		} else {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "a package-level var requires an initializer")
		}
		d.S = p.spanFrom(start)
		p.endDecl()
		return d
	case TYPEALIAS:
		p.next()
		d := &TypeAliasDecl{Doc: doc}
		d.Name = p.parseIdent("after 'typealias'")
		if p.at(LBRACK) || p.at(LSS) {
			p.errorf(diag.EUnsupportedSyntax, p.tok().Span, "generic type aliases are not supported yet")
			p.parseTypeParams()
		}
		p.expect(ASSIGN, "in type alias")
		p.skipNL()
		d.Type = p.parseType()
		d.S = p.spanFrom(start)
		p.endDecl()
		return d
	case CONST:
		if p.peek(1).Kind == LPAREN {
			// Go's const ( ... ) group.
			p.next()
			open := p.tok().Span
			p.skipBraces()
			span := start.Join(p.toks[p.pos-1].Span)
			p.errorf(diag.EUnsupportedSyntax, open, "Lyth declares one constant per line: const A = 1").
				WithHint("write each constant as const Name = value; for iota, declare an enum: enum Color { Red, Green }")
			p.damaged = append(p.damaged, span)
			p.endDecl()
			return nil
		}
		d := p.parseConst(start)
		d.Doc = doc
		p.endDecl()
		return d
	}
	t := p.tok()
	d := p.errorf(diag.EUnexpectedToken, t.Span, "expected declaration, found %s", describe(t))
	if t.Kind == IDENT {
		switch t.Text {
		case "type":
			d.WithHint("declare types with 'struct Name { ... }', 'interface Name { ... }', or 'typealias Name = Type'")
		case "class", "data":
			d.WithHint("Lyth uses 'struct' for data types")
		}
		if p.peek(1).Kind == DEFINE {
			d.WithHint("package-level variables are declared with var: var %s = ...", t.Text)
		}
	}
	p.syncDecl()
	return &BadDecl{S: p.spanFrom(start)}
}

// visibilityKeyword reports pub or private, which Lyth does not have: as in
// Go, a name that starts with an uppercase letter is exported.
func (p *parser) visibilityKeyword(kw Token, name string) {
	d := p.errorf(diag.EUnsupportedSyntax, kw.Span, "Lyth has no '%s': a name that starts with an uppercase letter is exported, as in Go", kw.Text)
	r, size := utf8.DecodeRuneInString(name)
	switch {
	case name == "" || name == "main":
		d.WithHint("remove '%s'", kw.Text)
	case kw.Kind == PUB && !unicode.IsUpper(r):
		d.WithHint("remove 'pub' and name it %s", string(unicode.ToUpper(r))+name[size:])
	case kw.Kind == PRIVATE && unicode.IsUpper(r):
		d.WithHint("remove 'private' and name it %s", string(unicode.ToLower(r))+name[size:])
	default:
		d.WithHint("remove '%s'", kw.Text)
	}
}

// declName returns the declared name of d, or "".
func declName(d Decl) string {
	var id *Ident
	switch d := d.(type) {
	case *FunDecl:
		id = d.Name
	case *StructDecl:
		id = d.Name
	case *InterfaceDecl:
		id = d.Name
	case *EnumDecl:
		id = d.Name
	case *PropDecl:
		id = d.Name
	case *TypeAliasDecl:
		id = d.Name
	case *ConstDecl:
		id = d.Name
	}
	if id == nil {
		return ""
	}
	return id.Name
}

// parseTypeParams parses [T, U: Constraint]. The Kotlin form <T> is parsed
// for recovery and reported by the caller.
func (p *parser) parseTypeParams() []*TypeParam {
	angle := p.at(LSS)
	p.next() // [ or <
	var tps []*TypeParam
	for !p.at(RBRACK) && !p.at(GTR) && !p.at(EOF) {
		start := p.tok().Span
		tp := &TypeParam{Name: p.parseIdent("in type parameter list")}
		if p.accept(COLON) {
			tp.Constraint = p.parseType()
		} else if !p.at(COMMA) && !p.at(RBRACK) && !p.at(GTR) {
			// Go's [T cmp.Ordered]: Lyth writes a constraint like a type, after ':'.
			c := p.parseType()
			p.errorf(diag.EUnsupportedSyntax, c.Span(), "a type parameter's constraint follows ':'").
				WithFix(tp.Name.S.EndSpan().Join(c.Span().At()), ": ", "write %s: %s", tp.Name.Name, c.Span().Text())
			tp.Constraint = c
		}
		tp.S = p.spanFrom(start)
		tps = append(tps, tp)
		if !p.accept(COMMA) {
			break
		}
	}
	if angle {
		p.expectCloseAngle()
	} else {
		p.expect(RBRACK, "to close the type parameter list")
	}
	return tps
}

// angleTypeParams reports Kotlin-style type parameters <T> at the current
// token and parses them. After a type's name the fix writes them in
// brackets in place; before a function's name (fun <T> f) it moves them
// after the name.
func (p *parser) angleTypeParams(where string, beforeName bool) []*TypeParam {
	start := p.tok().Span
	tps := p.parseTypeParams()
	span := p.spanFrom(start)
	brackets := "[" + typeParamText(tps) + "]"
	d := p.errorf(diag.EUnsupportedSyntax, span, "type parameters are written in brackets %s", where)
	if !beforeName {
		d.WithFix(span, brackets, "write %s", brackets)
		return tps
	}
	// The name, with its receiver (fun <T> []T.Second), ends before the
	// parameter list.
	end, depth := p.pos, 0
scan:
	for ; end < len(p.toks); end++ {
		switch p.toks[end].Kind {
		case LBRACK:
			depth++
		case RBRACK:
			depth--
		case LPAREN:
			if depth == 0 {
				break scan
			}
		case NEWLINE, LBRACE, EOF:
			end = p.pos
			break scan
		}
	}
	if end == p.pos {
		d.WithHint("write %s after the function name", brackets)
		return tps
	}
	name := p.tok().Span.Join(p.toks[end-1].Span)
	d.WithFix(span.Join(name), name.Text()+brackets, "write func %s%s(...)", name.Text(), brackets)
	return tps
}

func typeParamText(tps []*TypeParam) string {
	var parts []string
	for _, tp := range tps {
		parts = append(parts, tp.S.Text())
	}
	return strings.Join(parts, ", ")
}

// goCompositeLit reports Go's literal of a named type with Lyth's spelling,
// and returns a BadExpr covering it: a struct literal Point{X: 1} with the
// construction call, a map literal gin.H{"k": v} with a conversion of a Lyth
// literal. It returns nil when the braces are neither (a trailing lambda).
func (p *parser) goCompositeLit(x Expr) Expr {
	name := x
	switch g := x.(type) {
	case *IndexExpr: // Page[int]{Items: ...}
		name = g.X
	case *InstantiateExpr:
		name = g.X
	}
	if !isTypeNameExpr(name) || p.tok().Span.Start != x.Span().End {
		return nil // f { ... } is a trailing lambda; Point{ needs no space
	}
	isStruct := p.peek(1).Kind == IDENT && p.peek(2).Kind == COLON
	// Find the closing brace; an arrow at the top level makes it a lambda.
	open := p.pos
	depth, end := 0, -1
	var keys []Token
	// A map's first key is one expression; a statement break before the
	// first colon makes the braces a lambda body (run{ a := 1; b: int = a }).
	isMap, broken := false, false
	for i := open; i < len(p.toks) && end < 0; i++ {
		switch t := p.toks[i]; t.Kind {
		case LBRACE, LPAREN, LBRACK, TEMPLATE_EXPR_START:
			depth++
		case RBRACE, RPAREN, RBRACK, TEMPLATE_EXPR_END:
			depth--
			if depth == 0 {
				end = i
			}
		case ARROW:
			if depth == 1 {
				return nil
			}
		case NEWLINE, SEMI:
			if depth == 1 && !isMap && i > open+1 && p.toks[i-1].Kind != NEWLINE {
				broken = true
			}
		case COLON:
			if depth == 1 {
				isMap = isMap || !broken
				if p.toks[i-1].Kind == IDENT {
					keys = append(keys, t)
				}
			}
		case EOF:
			return nil
		}
	}
	if end < 0 || !isStruct && !isMap {
		return nil
	}
	span := x.Span().Join(p.toks[end].Span)
	body := p.toks[open].Span.Join(p.toks[end].Span)
	if !isStruct {
		// gin.H{"error": msg} -> gin.H(["error": msg]); elided element
		// literals become list or map literals: url.Values{"q": {"go"}} ->
		// url.Values(["q": ["go"]]).
		inner, exact := p.elidedToBrackets(open, end)
		repl := x.Span().Text() + "([" + strings.TrimSpace(inner) + "])"
		d := p.errorf(diag.EUnsupportedSyntax, span, "Lyth converts a literal to a named map type")
		if exact {
			d.WithFix(span, repl, "write %s", repl)
		} else {
			d.WithEdit(span, repl, "write %s, with each struct value constructed by calling its type", repl)
		}
		p.pos = end + 1
		return &BadExpr{S: span}
	}
	// Point{X: 1, Y: 2} -> Point(X = 1, Y = 2)
	args := body.Text()
	args = args[1 : len(args)-1]
	for i := len(keys) - 1; i >= 0; i-- {
		off := keys[i].Span.Start - body.Start - 1
		args = args[:off] + " =" + args[off+1:]
	}
	repl := x.Span().Text() + "(" + strings.TrimSpace(args) + ")"
	p.errorf(diag.EUnsupportedSyntax, span, "Lyth constructs a struct by calling its type").
		WithFix(span, repl, "write %s", repl)
	p.pos = end + 1
	return &BadExpr{S: span}
}

// elidedToBrackets returns the text between the braces at toks[open] and
// toks[end] with Go's elided element literals ({"go"}, {"a": 1}) written as
// Lyth's [...] literals. exact is false when an elided literal is a struct
// ({X: 1}), which has no bracket form.
func (p *parser) elidedToBrackets(open, end int) (string, bool) {
	body := p.toks[open].Span.Join(p.toks[end].Span)
	text := []byte(body.Text())
	exact := true
	var stack []int // token indexes of open braces, nil entries for other brackets
	for i := open + 1; i < end; i++ {
		switch p.toks[i].Kind {
		case LBRACE:
			if len(stack) == 0 || stack[len(stack)-1] >= 0 {
				if prev := p.toks[i-1].Kind; prev == COLON || prev == COMMA || prev == LBRACE {
					stack = append(stack, i)
					if p.toks[i+1].Kind == IDENT && p.toks[i+2].Kind == COLON {
						exact = false
					}
					text[p.toks[i].Span.Start-body.Start] = '['
					continue
				}
			}
			stack = append(stack, -1)
		case LPAREN, LBRACK, TEMPLATE_EXPR_START:
			stack = append(stack, -1)
		case RBRACE, RPAREN, RBRACK, TEMPLATE_EXPR_END:
			if n := len(stack); n > 0 {
				if stack[n-1] >= 0 && p.toks[i].Kind == RBRACE {
					text[p.toks[i].Span.Start-body.Start] = ']'
				}
				stack = stack[:n-1]
			}
		}
	}
	return string(text[1 : len(text)-1]), exact
}

// endsInTypeOperand returns the type operand that ends a header expression
// (for x in []int, if xs == []int), or nil.
func endsInTypeOperand(e Expr) *TypeOperand {
	switch x := e.(type) {
	case *TypeOperand:
		return x
	case *BinaryExpr:
		return endsInTypeOperand(x.Y)
	case *InExpr:
		return endsInTypeOperand(x.Y)
	case *UnaryExpr:
		return endsInTypeOperand(x.X)
	}
	return nil
}

// parseTypedLit parses the braces of a typed literal after its type:
// []T{a, b}, [N]T{a, b}, [...]T{a, b}, or map[K]V{k: v}. Elements are
// ordinary expressions; an element literal gives its own type or takes the
// element type from context ([][]int{[1, 2]}), since an elided {1, 2} would
// read as a lambda.
func (p *parser) parseTypedLit(t *TypeOperand) Expr {
	saved := p.noLambda
	p.noLambda = 0
	defer func() { p.noLambda = saved }()
	_, isMap := t.Type.(*MapType)
	switch t.Type.(type) {
	case *MapType, *SliceType, *ArrayType:
	default:
		return nil
	}
	open := p.next() // {
	var keys, values []Expr
	indexed := false
	for {
		p.skipNL()
		if p.at(RBRACE) || p.at(EOF) {
			break
		}
		if p.at(LBRACE) && p.elidedLit() {
			start := p.tok().Span
			p.skipBraces()
			p.errorf(diag.EUnsupportedSyntax, p.spanFrom(start), "an element of a typed literal is written with its own literal").
				WithHint("write [a, b] or [k: v] (typed by the element type), Type(...) for a struct, or the full type: []int{...}")
			values = append(values, &BadExpr{S: p.spanFrom(start)})
		} else {
			x := p.parseExpr()
			p.skipNL()
			if p.at(COLON) {
				colon := p.next()
				p.skipNL()
				v := p.parseExpr()
				if !isMap {
					if !indexed {
						p.errorf(diag.EUnsupportedSyntax, x.Span().Join(colon.Span), "list and array literals give every element in order; indexed elements are not supported").
							WithHint("list the elements: %s{a, b, c}", t.Type.Span().Text())
					}
					indexed = true
					x = v
				} else {
					keys = append(keys, x)
					x = v
				}
			} else if isMap {
				p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected ':' after a map key, found %s", describe(p.tok()))
				// Skip the rest of the literal.
				p.pos--
				for p.pos > 0 && p.tok().Span.Start > open.Span.Start {
					p.pos--
				}
				p.skipBraces()
				return &BadExpr{S: t.S.Join(p.toks[p.pos-1].Span)}
			}
			values = append(values, x)
		}
		p.skipNL()
		if !p.accept(COMMA) {
			break
		}
	}
	p.skipNL()
	end := p.expect(RBRACE, "to close the literal")
	span := t.S.Join(end.Span)
	if isMap {
		return &MapLit{Type: t.Type, Keys: keys, Values: values, S: span}
	}
	return &ListLit{Type: t.Type, Elems: values, S: span}
}

// elidedLit reports whether the braces at the current token hold an elided
// Go literal ({1, 2}, {X: 1}, {"a": 1}) rather than a lambda: a comma or a
// key at the top level, and no arrow.
func (p *parser) elidedLit() bool {
	depth, arrow, list := 0, false, false
	for i := p.pos; i < len(p.toks); i++ {
		switch p.toks[i].Kind {
		case LBRACE, LPAREN, LBRACK:
			depth++
		case RBRACE, RPAREN, RBRACK:
			depth--
			if depth == 0 {
				return list && !arrow
			}
		case ARROW:
			arrow = arrow || depth == 1
		case COMMA, COLON:
			list = list || depth == 1
		case EOF:
			return false
		}
	}
	return false
}

// skipBraces skips a balanced {...}.
func (p *parser) skipBraces() {
	depth := 0
	for !p.at(EOF) {
		switch p.tok().Kind {
		case LBRACE, LPAREN, LBRACK:
			depth++
		case RBRACE, RPAREN, RBRACK:
			depth--
		}
		p.next()
		if depth == 0 {
			return
		}
	}
}

// selfAsName reports self where a name is being declared (a lambda
// parameter, a when binding) and reads it as _, so parsing continues.
func (p *parser) selfAsName() {
	if !p.at(THIS) {
		return
	}
	switch p.peek(1).Kind {
	case ARROW, COLON, COMMA, DEFINE:
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "self is the method receiver; it cannot be declared or redeclared").
			WithHint("choose another name")
		p.toks[p.pos].Kind, p.toks[p.pos].Text = IDENT, "_"
	}
}

// oldFun turns the Kotlin keyword fun, at the start of a declaration, into
// func after reporting it.
func (p *parser) oldFun() {
	if t := p.tok(); t.Kind == IDENT && t.Text == "fun" && (p.peek(1).Kind == IDENT || p.peek(1).Kind == LSS || p.peek(1).Kind == MUL || p.peek(1).Kind == LBRACK) {
		p.errorf(diag.EUnsupportedSyntax, t.Span, "functions are declared with func").
			WithFix(t.Span, "func", "write func")
		p.toks[p.pos].Kind = FUN
	}
}

func (p *parser) parseFunDecl(start source.Span, doc string) *FunDecl {
	p.expect(FUN, "")
	d := &FunDecl{Doc: doc}
	if p.at(LPAREN) {
		// Go's func (s *Server) Name(...): Lyth names the type before the
		// method, and the receiver is self.
		open := p.tok().Span
		depth := 0
		for !p.at(EOF) {
			if p.at(LPAREN) {
				depth++
			} else if p.at(RPAREN) {
				depth--
				if depth == 0 {
					break
				}
			}
			p.next()
		}
		recvText := strings.TrimSpace(open.Join(p.tok().Span).Text())
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(recvText, "("), ")"))
		typ := inner
		if i := strings.Index(inner, ":"); i >= 0 {
			typ = strings.TrimSpace(inner[i+1:]) // (h: Handler)
		} else if f := strings.Fields(inner); len(f) == 2 {
			typ = f[1] // Go's (s *Server)
		}
		end := p.next().Span
		if p.at(DOT) {
			p.next() // func (T).name(): the receiver in parentheses, then the name
		}
		if strings.ContainsAny(typ, " ()") {
			// func (h: (W, R) -> unit).name(): a function type needs a name.
			p.errorf(diag.EUnsupportedSyntax, open.Join(end), "an extension is declared func Type.Name(...); the receiver is self").
				WithHint("name the function type first: typealias Handler = %s, then func Handler.Name(...)", typ)
		} else if d2 := p.errorf(diag.EUnsupportedSyntax, open.Join(end), "methods are declared func %s.Name(...); the receiver is self", typ); p.at(IDENT) && typ != "" {
			d2.WithEdit(open.Join(p.tok().Span.At()), typ+".", "write func %s.%s(...) and use self in the body", typ, p.tok().Text)
		}
		// The body names the Go receiver; nothing more is reported inside.
		defer func() { p.damaged = append(p.damaged, d.S) }()
		// Declare the method anyway, so that calls of it check.
		if r := p.goReceiver(open, end); r != nil {
			d.Recv = r
		}
	}
	if p.at(LSS) {
		d.TypeParams = p.angleTypeParams("after the function name", true)
	}
	switch r, name := p.parseReceiver(); {
	case name != nil:
		d.Recv, d.Name = r, name
	case r != nil:
		d.Recv = r
		d.Name = p.parseIdent("after receiver type")
	default:
		d.Name = p.parseIdent("after 'func'")
	}
	if p.at(LBRACK) {
		d.TypeParams = append(d.TypeParams, p.parseTypeParams()...)
	}
	d.TypeParams = append(d.TypeParams, p.recoveredTPs...)
	p.recoveredTPs = nil
	d.Params = p.parseParams()
	if p.accept(COLON) {
		d.Result = p.parseResultType()
	}
	if p.at(THROWS) {
		d.Throws = true
		d.ThrowsSpan = p.next().Span
	}
	switch {
	case p.at(LBRACE):
		d.Body = p.parseBlock()
	case p.at(ASSIGN):
		p.next()
		p.skipNL()
		d.ExprBody = p.parseExpr()
	default:
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected function body '{' or '= expression', found %s", describe(p.tok()))
		p.syncDecl()
	}
	d.S = p.spanFrom(start)
	return d
}

// parseReceiver parses the receiver of a method or extension function up to
// and including the dot before the function's name, or returns nil when the
// function has none. A type parser reads `[]T.Filter` as the qualified type
// T.Filter, so for a receiver followed by ( the last name is the function's,
// returned as name.
// goReceiver reads the receiver type of Go's func (t *T) M() back from the
// tokens between open and end, or returns nil.
func (p *parser) goReceiver(open, end source.Span) *Receiver {
	i := p.pos - 1
	for i > 0 && p.toks[i].Span.Start != open.Start {
		i--
	}
	j := i + 1
	if p.toks[j].Kind == IDENT && (p.toks[j+1].Kind == MUL || p.toks[j+1].Kind == IDENT || p.toks[j+1].Kind == COLON) {
		j++ // the receiver's name
	}
	if p.toks[j].Kind == COLON {
		j++
	}
	r := &Receiver{}
	if p.toks[j].Kind == MUL {
		r.Pointer = true
		j++
	}
	if p.toks[j].Kind != IDENT || p.toks[j+1].Kind != RPAREN || p.toks[j+1].Span.End != end.End {
		return nil // generic or unusual receivers: report only
	}
	name := &Ident{Name: p.toks[j].Text, S: p.toks[j].Span}
	r.Type = &NamedType{Name: name, S: name.S}
	r.Name = name
	r.S = name.S
	return r
}

func (p *parser) parseReceiver() (r *Receiver, name *Ident) {
	switch {
	case p.at(MUL), p.at(LBRACK), p.at(MAP), p.at(CHAN):
	case p.at(IDENT) && (p.peek(1).Kind == DOT || p.peek(1).Kind == LSS || p.peek(1).Kind == SAFE_DOT):
	case p.at(IDENT) && p.peek(1).Kind == LBRACK && p.afterBrackets(p.pos+1).Kind == DOT:
		// Page[T].First: a method of a generic type.
	default:
		return nil, nil
	}
	start := p.tok().Span
	r = &Receiver{Pointer: p.accept(MUL)}
	p.receiverType = true
	te := p.parseType()
	p.receiverType = false
	switch {
	case p.at(DOT):
		r.Type = te
		r.S = start.Join(p.toks[p.pos-1].Span)
		p.next()
	case p.at(SAFE_DOT):
		// fun String?.OrEmpty(): the ?. lexes as one token.
		r.Type = &NullableType{Elem: te, S: te.Span().Join(p.tok().Span)}
		r.S = start.Join(p.tok().Span)
		p.next()
	case p.at(LPAREN), p.at(LBRACK):
		recv, fname := splitReceiver(te)
		if recv == nil {
			if nt, ok := te.(*NamedType); ok && nt.Pkg == nil && len(nt.Args) > 0 && !r.Pointer {
				// func k<T>(...): keep the type parameters so the body checks.
				var names []string
				for _, a := range nt.Args {
					if at, ok := a.(*NamedType); ok && at.Pkg == nil && len(at.Args) == 0 {
						p.recoveredTPs = append(p.recoveredTPs, &TypeParam{Name: at.Name, S: at.S})
						names = append(names, at.Name.Name)
					}
				}
				d := p.errorf(diag.EUnsupportedSyntax, nt.Args[0].Span(), "type parameters are written in brackets after the function name")
				if len(names) == len(nt.Args) {
					d.WithFix(nt.Name.S.EndSpan().Join(nt.S.EndSpan()), "["+strings.Join(names, ", ")+"]", "write func %s[%s](...)", nt.Name.Name, strings.Join(names, ", "))
				} else {
					d.WithHint("write func %s[%s](...)", nt.Name.Name, strings.Join(names, ", "))
				}
				return nil, nt.Name
			}
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected '.' and the function name after the receiver type")
			r.Type = te
			r.S = start.Join(p.toks[p.pos-1].Span)
			return r, &Ident{Name: "_", S: p.tok().Span}
		}
		r.Type = recv
		r.S = start.Join(recv.Span())
		name = fname
	default:
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected '.' and the function name after the receiver type, found %s", describe(p.tok()))
		r.Type = te
		r.S = start.Join(p.toks[p.pos-1].Span)
	}
	if nt, ok := r.Type.(*NamedType); ok && nt.Pkg == nil {
		r.Name, r.TypeArgs = nt.Name, nt.Args
	}
	return r, name
}

// splitReceiver splits a receiver type that a type parser read together
// with the function's name, as the qualified name T.Filter in []T.Filter:
// it returns the receiver type (T, []T) and the name, or nil.
func splitReceiver(te TypeExpr) (TypeExpr, *Ident) {
	switch t := te.(type) {
	case *NamedType:
		if t.Pkg == nil || len(t.Args) > 0 {
			return nil, nil
		}
		return &NamedType{Name: t.Pkg, S: t.Pkg.S}, t.Name
	case *SliceType:
		elem, name := splitReceiver(t.Elem)
		if elem == nil {
			return nil, nil
		}
		return &SliceType{Elem: elem, S: t.S.Join(elem.Span())}, name
	case *MapType:
		elem, name := splitReceiver(t.Value)
		if elem == nil {
			return nil, nil
		}
		return &MapType{Key: t.Key, Value: elem, S: t.S.Join(elem.Span())}, name
	case *ChanType:
		elem, name := splitReceiver(t.Elem)
		if elem == nil {
			return nil, nil
		}
		return &ChanType{Dir: t.Dir, Elem: elem, S: t.S.Join(elem.Span())}, name
	case *ArrayType:
		elem, name := splitReceiver(t.Elem)
		if elem == nil {
			return nil, nil
		}
		return &ArrayType{Len: t.Len, Elem: elem, S: t.S.Join(elem.Span())}, name
	case *PointerType:
		elem, name := splitReceiver(t.Elem)
		if elem == nil {
			return nil, nil
		}
		return &PointerType{Elem: elem, S: t.S.Join(elem.Span())}, name
	}
	return nil, nil
}

func (p *parser) parseParams() []*Param {
	p.expect(LPAREN, "to start the parameter list")
	var params []*Param
	for !p.at(RPAREN) && !p.at(EOF) {
		start := p.tok().Span
		prm := &Param{Name: p.parseIdent("for parameter name")}
		if !p.at(COLON) {
			d := p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected ':' after parameter name %s", prm.Name.Name)
			if p.at(IDENT) || p.at(MUL) || p.at(LBRACK) {
				d.WithHint("Lyth writes parameters as name: Type, e.g. %s: %s", prm.Name.Name, p.tok().Text)
			}
		} else {
			p.next()
		}
		if p.accept(ELLIPSIS) {
			prm.Variadic = true
		}
		prm.Type = p.parseType()
		if p.accept(ASSIGN) {
			p.skipNL()
			prm.Default = p.parseExpr()
		}
		prm.S = p.spanFrom(start)
		params = append(params, prm)
		if !p.accept(COMMA) {
			break
		}
	}
	p.expect(RPAREN, "to close the parameter list")
	return params
}

// parseResultType parses a declared result: Type or (T1, T2, ...).
func (p *parser) parseResultType() *ResultType {
	start := p.tok().Span
	if p.at(LPAREN) {
		save := p.pos
		types := p.parseParenTypeList()
		if p.at(ARROW) {
			// A function type result: re-parse as a full type.
			p.pos = save
			t := p.parseType()
			return &ResultType{Types: []TypeExpr{t}, S: t.Span()}
		}
		if len(types) == 1 {
			t := types[0]
			if p.at(QUESTION) {
				q := p.next()
				t = &NullableType{Elem: t, S: t.Span().Join(q.Span)}
			}
			return &ResultType{Types: []TypeExpr{t}, S: p.spanFrom(start)}
		}
		if len(types) == 0 {
			p.errorf(diag.EUnexpectedToken, p.spanFrom(start), "empty result list; omit the result type for functions that return nothing")
		}
		return &ResultType{Types: types, Tuple: true, S: p.spanFrom(start)}
	}
	t := p.parseType()
	return &ResultType{Types: []TypeExpr{t}, S: t.Span()}
}

func (p *parser) parseParenTypeList() []TypeExpr {
	p.expect(LPAREN, "")
	var types []TypeExpr
	for !p.at(RPAREN) && !p.at(EOF) {
		types = append(types, p.parseType())
		if !p.accept(COMMA) {
			break
		}
	}
	p.expect(RPAREN, "to close the type list")
	return types
}

func (p *parser) parseStructDecl(start source.Span, doc string) *StructDecl {
	p.expect(STRUCT, "")
	d := &StructDecl{Doc: doc}
	d.Name = p.parseIdent("after 'struct'")
	switch {
	case p.at(LBRACK):
		d.TypeParams = p.parseTypeParams()
	case p.at(LSS):
		d.TypeParams = p.angleTypeParams("after the struct name", false)
	}
	if p.at(LPAREN) {
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "struct fields are declared in braces").
			WithHint("write struct %s { name: Type ... }", d.Name.Name)
		p.syncDecl()
		d.S = p.spanFrom(start)
		return d
	}
	if p.accept(COLON) {
		// struct T : I1, I2 { ... }
		for {
			d.Implements = append(d.Implements, p.parseType())
			if !p.accept(COMMA) {
				break
			}
			p.skipNL()
		}
	}
	p.expect(LBRACE, "to start the struct body")
	for {
		p.skipNLSemi()
		for p.accept(COMMA) {
			p.skipNLSemi()
		}
		if p.at(RBRACE) || p.at(EOF) {
			break
		}
		fstart := p.tok().Span
		f := &Field{Doc: p.docFor(fstart.Start)}
		if p.at(PUB) || p.at(PRIVATE) {
			kw := p.next()
			if p.at(IDENT) {
				p.visibilityKeyword(kw, p.tok().Text)
			} else {
				p.visibilityKeyword(kw, "")
			}
		}
		if (p.at(VAL) || p.at(VAR)) && p.peek(1).Kind == COLON {
			// val: int names a field val, a reserved word.
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "%s is a reserved word and cannot name a field", p.tok().Text).
				WithHint("choose another name, such as Value")
			p.toks[p.pos].Kind = IDENT
		} else if p.at(VAL) || p.at(VAR) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "struct fields are declared as name: Type").
				WithFix(p.tok().Span.Join(p.peek(1).Span.At()), "", "write the field without %s", p.tok().Text)
			p.next()
		}
		if p.at(FUN) || p.at(IDENT) && p.tok().Text == "fun" && p.peek(1).Kind == IDENT {
			p.errorf(diag.EUnsupportedSyntax, p.tok().Span, "methods are declared outside the struct").
				WithHint("write func %s.name(...) { ... } at top level", d.Name.Name)
			p.syncStmt()
			continue
		}
		f.Name = p.parseIdent("for field name")
		p.expect(COLON, "after field name")
		f.Type = p.parseType()
		if p.at(RAWSTRING) {
			t := p.next()
			f.Tag, f.HasTag, f.TagSpan = t.Value, true, t.Span
		} else if p.at(STRING) {
			t := p.next()
			f.Tag, f.HasTag, f.TagSpan = t.Value, true, t.Span
		}
		f.S = p.spanFrom(fstart)
		d.Fields = append(d.Fields, f)
		if !p.at(NEWLINE) && !p.at(SEMI) && !p.at(COMMA) && !p.at(RBRACE) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected newline after field, found %s", describe(p.tok()))
			p.syncStmt()
		}
	}
	p.expect(RBRACE, "to close the struct body")
	d.S = p.spanFrom(start)
	return d
}

// parseEnumDecl parses `enum Name { A, B, C }`; entries are separated by
// commas or newlines.
func (p *parser) parseEnumDecl(start source.Span, doc string) *EnumDecl {
	p.expect(ENUM, "")
	d := &EnumDecl{Doc: doc}
	d.Name = p.parseIdent("after 'enum'")
	if p.at(IDENT) && p.tok().Text == "class" {
		p.errorf(diag.EUnsupportedSyntax, p.tok().Span, "Lyth declares enums with 'enum Name { ... }'")
		p.next()
	}
	p.expect(LBRACE, "to start the enum body")
	for {
		for p.accept(COMMA) || p.accept(NEWLINE) || p.accept(SEMI) {
		}
		if p.at(RBRACE) || p.at(EOF) {
			break
		}
		if !p.at(IDENT) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected an enum entry name, found %s", describe(p.tok()))
			p.syncStmt()
			continue
		}
		d.Entries = append(d.Entries, p.parseIdent(""))
		if p.at(LPAREN) || p.at(LBRACE) {
			p.errorf(diag.EUnsupported, p.tok().Span, "enum entries cannot have values or bodies").
				WithHint("use a sealed interface whose members are structs for entries that carry data")
			p.syncStmt()
		}
	}
	end := p.expect(RBRACE, "to close the enum body")
	d.S = start.Join(end.Span)
	return d
}

func (p *parser) parseInterfaceDecl(start source.Span, doc string) *InterfaceDecl {
	p.expect(INTERFACE, "")
	d := &InterfaceDecl{Doc: doc}
	d.Name = p.parseIdent("after 'interface'")
	switch {
	case p.at(LBRACK):
		d.TypeParams = p.parseTypeParams()
	case p.at(LSS):
		d.TypeParams = p.angleTypeParams("after the interface name", false)
	}
	p.expect(LBRACE, "to start the interface body")
	for {
		p.skipNLSemi()
		if p.at(RBRACE) || p.at(EOF) {
			break
		}
		mstart := p.tok().Span
		if kw := p.tok(); kw.Kind == FUN || kw.Kind == IDENT && kw.Text == "fun" && p.peek(1).Kind == IDENT {
			// Go's interfaces list methods without a keyword.
			p.next()
			p.errorf(diag.EUnsupportedSyntax, kw.Span, "interface methods are written without '%s'", kw.Text).
				WithFix(kw.Span.Join(p.tok().Span.At()), "", "write %s(...)", p.tok().Text)
		}
		if p.at(IDENT) && p.peek(1).Kind == LPAREN {
			m := &MethodSpec{Doc: p.docFor(mstart.Start)}
			m.Name = p.parseIdent("for method name")
			m.Params = p.parseParams()
			if p.accept(COLON) {
				m.Result = p.parseResultType()
			}
			if p.at(THROWS) {
				m.Throws = true
				m.ThrowsSpan = p.next().Span
			}
			if p.at(LBRACE) {
				p.errorf(diag.EUnsupportedSyntax, p.tok().Span, "interface methods cannot have bodies")
				p.parseBlock()
			}
			m.S = p.spanFrom(mstart)
			d.Methods = append(d.Methods, m)
		} else {
			d.Embeds = append(d.Embeds, p.parseType())
		}
		if !p.at(NEWLINE) && !p.at(SEMI) && !p.at(RBRACE) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected newline in interface body, found %s", describe(p.tok()))
			p.syncStmt()
		}
	}
	p.expect(RBRACE, "to close the interface body")
	d.S = p.spanFrom(start)
	return d
}

// ---------------------------------------------------------------------------
// Types

func (p *parser) parseType() TypeExpr {
	t := p.parsePrefixType()
	if p.at(QUESTION) {
		q := p.next()
		t = &NullableType{Elem: t, S: t.Span().Join(q.Span)}
		if p.at(QUESTION) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "a type can be made nullable only once")
			p.next()
		}
	}
	return t
}

func (p *parser) parsePrefixType() TypeExpr {
	start := p.tok().Span
	switch p.tok().Kind {
	case MUL:
		p.next()
		elem := p.parsePrefixType()
		return &PointerType{Elem: elem, S: p.spanFrom(start)}
	case LBRACK:
		p.next()
		// A ? after the element type belongs to the element: []User? is a
		// slice of nullable users, since slices are never nullable.
		if p.at(RBRACK) {
			p.next()
			elem := p.parseType()
			return &SliceType{Elem: elem, S: p.spanFrom(start)}
		}
		var n Expr
		if !p.accept(ELLIPSIS) {
			n = p.parseExpr()
		}
		p.expect(RBRACK, "after array length")
		elem := p.parseType()
		return &ArrayType{Len: n, Elem: elem, S: p.spanFrom(start)}
	case LPAREN:
		types := p.parseParenTypeList()
		if p.at(ARROW) {
			p.next()
			ft := &FuncType{Params: types}
			ft.Result = p.parseFuncResult()
			if p.at(THROWS) {
				p.next()
				ft.Throws = true
			}
			ft.S = p.spanFrom(start)
			return ft
		}
		if len(types) == 1 {
			return types[0]
		}
		p.errorf(diag.EUnexpectedToken, p.spanFrom(start), "tuple types are only allowed as function results")
		return &BadType{S: p.spanFrom(start)}
	case STRUCT:
		if p.peek(1).Kind == LBRACE && p.peek(2).Kind == RBRACE {
			p.next()
			p.next()
			p.next()
			return &EmptyStructType{S: p.spanFrom(start)}
		}
		p.errorf(diag.EUnsupportedSyntax, p.tok().Span, "anonymous struct types are not supported").
			WithHint("declare the struct: struct Name { ... }; struct{} is the empty struct")
		p.next()
		if p.at(LBRACE) {
			p.skipBraces()
		}
		return &BadType{S: p.spanFrom(start)}
	case MAP:
		p.next()
		p.expect(LBRACK, "after 'map'")
		key := p.parseType()
		p.expect(RBRACK, "after the map key type")
		// As in a slice, a ? after the value type belongs to the value.
		value := p.parseType()
		return &MapType{Key: key, Value: value, S: p.spanFrom(start)}
	case CHAN:
		p.next()
		dir := ChanBoth
		if p.at(LSS) && p.peek(1).Kind == SUB && p.peek(1).Span.Start == p.tok().Span.End {
			p.next()
			p.next()
			dir = ChanSend
		}
		elem := p.parseType()
		return &ChanType{Dir: dir, Elem: elem, S: p.spanFrom(start)}
	case LSS:
		if minus := p.peek(1); minus.Kind == SUB && minus.Span.Start == p.tok().Span.End && p.peek(2).Kind == CHAN {
			p.next()
			p.next()
			p.next()
			elem := p.parseType()
			return &ChanType{Dir: ChanReceive, Elem: elem, S: p.spanFrom(start)}
		}
	case IDENT:
		first := p.parseIdent("")
		nt := &NamedType{Name: first}
		if p.at(DOT) && p.peek(1).Kind == IDENT {
			p.next()
			nt.Pkg = first
			nt.Name = p.parseIdent("")
		}
		switch {
		case p.at(LBRACK) && p.tok().Span.Start == nt.Name.S.End && !(p.receiverType && nt.Pkg != nil):
			nt.Args = p.parseTypeArgs()
		case p.at(LSS):
			args := p.parseTypeArgs()
			nt.Args = args
			switch nt.Name.Name {
			case "Map", "Chan", "SendChan", "ReceiveChan", "List", "MutableList", "ArrayList", "HashMap":
				// The checker names the Go spelling of the whole type.
			default:
				p.oldTypeArgs(nt.Name.S.EndSpan().Join(p.toks[p.pos-1].Span), args)
			}
		}
		nt.S = p.spanFrom(start)
		return nt
	}
	t := p.tok()
	p.errorf(diag.EUnexpectedToken, t.Span, "expected type, found %s", describe(t))
	if t.Kind != NEWLINE && t.Kind != RPAREN && t.Kind != RBRACE && t.Kind != EOF && t.Kind != COMMA {
		p.next()
	}
	return &BadType{S: t.Span}
}

func (p *parser) parseFuncResult() *ResultType {
	start := p.tok().Span
	if p.at(LPAREN) {
		save := p.pos
		types := p.parseParenTypeList()
		if p.at(ARROW) || len(types) == 1 {
			p.pos = save
			t := p.parseType()
			return &ResultType{Types: []TypeExpr{t}, S: t.Span()}
		}
		return &ResultType{Types: types, Tuple: true, S: p.spanFrom(start)}
	}
	t := p.parseType()
	return &ResultType{Types: []TypeExpr{t}, S: t.Span()}
}

// parseTypeArgs parses [A, B], or the Kotlin form <A, B> for recovery.
func (p *parser) parseTypeArgs() []TypeExpr {
	angle := p.at(LSS)
	p.next()
	var args []TypeExpr
	for !p.at(EOF) {
		args = append(args, p.parseType())
		if !p.accept(COMMA) {
			break
		}
	}
	if angle {
		p.expectCloseAngle()
	} else {
		p.expect(RBRACK, "to close the type arguments")
	}
	return args
}

// oldTypeArgs reports type arguments written <A, B> at span.
func (p *parser) oldTypeArgs(span source.Span, args []TypeExpr) {
	var parts []string
	for _, a := range args {
		parts = append(parts, a.Span().Text())
	}
	p.errorf(diag.EUnsupportedSyntax, span, "type arguments are written in brackets").
		WithFix(span, "["+strings.Join(parts, ", ")+"]", "write [%s]", strings.Join(parts, ", "))
}

// expectCloseAngle consumes one '>' and splits '>>', '>=', '>>=' tokens.
func (p *parser) expectCloseAngle() {
	t := p.tok()
	split := func(kind Kind, text string) {
		p.toks[p.pos] = Token{Kind: kind, Text: text, Span: source.Span{File: t.Span.File, Start: t.Span.Start + 1, End: t.Span.End}}
	}
	switch t.Kind {
	case GTR:
		p.next()
	case SHR:
		split(GTR, ">")
	case GEQ:
		split(ASSIGN, "=")
	case SHR_ASSIGN:
		split(GEQ, ">=")
	default:
		p.errorf(diag.EUnexpectedToken, t.Span, "expected '>' to close type arguments, found %s", describe(t))
	}
}

// tryTypeArgs speculatively parses `<T, ...>` after an expression that
// names a type or generic function. It succeeds only when the closing '>'
// is followed by a token that cannot continue a comparison.
func (p *parser) tryTypeArgs() ([]TypeExpr, bool) {
	save := p.pos
	saveSink := p.sink
	saveLast := p.lastErr
	scratch := &diag.Sink{}
	p.sink = scratch
	toksCopy := append([]Token(nil), p.toks[p.pos:min(len(p.toks), p.pos+64)]...)
	args := p.parseTypeArgs()
	ok := !scratch.HasErrors()
	if ok {
		switch p.tok().Kind {
		case LPAREN, RPAREN, COMMA, DOT, NEWLINE, RBRACK, EOF:
		default:
			ok = false
		}
	}
	p.sink = saveSink
	p.lastErr = saveLast
	if !ok {
		// Restore tokens possibly modified by '>>' splitting.
		copy(p.toks[save:], toksCopy)
		p.pos = save
		return nil, false
	}
	return args, true
}

// ---------------------------------------------------------------------------
// Statements

func (p *parser) parseBlock() *Block {
	saved := p.noLambda
	p.noLambda = 0
	defer func() { p.noLambda = saved }()
	lb := p.expect(LBRACE, "to start a block")
	b := &Block{Lbrace: lb.Span}
	b.Stmts = p.parseStmtList()
	rb := p.expect(RBRACE, "to close the block")
	b.Rbrace = rb.Span
	b.S = lb.Span.Join(rb.Span)
	return b
}

func (p *parser) parseStmtList() []Stmt {
	var stmts []Stmt
	for {
		p.skipNLSemi()
		if p.at(RBRACE) || p.at(EOF) {
			return stmts
		}
		start, n, inner := p.pos, p.sink.Len(), len(p.damaged)
		s := p.parseStmt()
		if s != nil {
			stmts = append(stmts, s)
		}
		if !p.at(NEWLINE) && !p.at(SEMI) && !p.at(RBRACE) && !p.at(EOF) {
			if !p.syntaxErrorSince(n) {
				p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected newline or ';' after statement, found %s", describe(p.tok()))
			}
			p.syncStmt()
		}
		if p.pos == start {
			p.next()
		}
		p.markDamaged(start, n, inner)
	}
}

// parseBody parses a control-structure body: a block or a single statement.
func (p *parser) parseBody() *Block {
	if p.at(LBRACE) {
		return p.parseBlock()
	}
	s := p.parseStmt()
	if s == nil {
		return &Block{S: p.tok().Span.At()}
	}
	return &Block{Stmts: []Stmt{s}, S: s.Span()}
}

func canStartExpr(k Kind) bool {
	switch k {
	case NEWLINE, SEMI, RBRACE, RPAREN, RBRACK, COMMA, EOF, ELSE, ARROW, COLON, TEMPLATE_EXPR_END:
		return false
	}
	return true
}

func (p *parser) parseStmt() Stmt {
	t := p.tok()
	if t.Kind == IDENT && p.peek(1).Kind == COLON && (p.peek(2).Kind == FOR || p.peek(2).Kind == WHILE) {
		// Go's label outer: for ...: read it as outer@ for ...
		colon := p.peek(1)
		p.errorf(diag.EUnsupportedSyntax, t.Span.Join(colon.Span), "labels are written %s@ before the loop", t.Text).
			WithFix(t.Span.Join(colon.Span), t.Text+"@", "write %s@ for ...", t.Text).
			WithNote("break and continue then name it: break@%s", t.Text)
		p.toks[p.pos+1].Kind = AT
		p.toks[p.pos+1].Span.Start = t.Span.End
	}
	if t.Kind == IDENT && p.peek(1).Kind == AT && p.peek(1).Span.Start == t.Span.End {
		// outer@ for (...) / outer@ while (...)
		label := p.parseIdent("")
		p.next() // @
		switch p.tok().Kind {
		case FOR:
			s := p.parseFor().(*ForStmt)
			s.Label = label
			return s
		case WHILE:
			s := p.parseWhile().(*WhileStmt)
			s.Label = label
			return s
		}
		p.errorf(diag.EUnsupportedSyntax, label.S, "labels can only be placed on for and while loops")
		t = p.tok()
	}
	if t.Kind != IDENT && t.Kind != THIS && t.Kind != VAL && t.Kind != VAR && IsKeywordName(t.Text) && (p.peek(1).Kind == DEFINE || p.peek(1).Kind == COLON && t.Kind != CATCH) {
		// in := 3, select: Time: a keyword cannot name a variable.
		p.errorf(diag.EUnexpectedToken, t.Span, "%s is a keyword and cannot be a variable name", t.Text).
			WithHint("choose another name, such as %s", t.Text+"Value")
		p.syncStmt()
		bad := &BadStmt{S: p.spanFrom(t.Span)}
		p.damaged = append(p.damaged, bad.S)
		return bad
	}
	switch t.Kind {
	case VAL, VAR:
		return p.parseOldLocalDecl()
	case CONST:
		return p.parseConst(t.Span)
	case IDENT:
		if p.peek(1).Kind == COLON {
			return p.parseTypedLocalDecl()
		}
		if t.Text == "switch" && p.goSwitch() {
			// Go's switch: Lyth's when has -> branches instead of case:.
			p.errorf(diag.EUnsupportedSyntax, t.Span, "Lyth has no switch: use when").
				WithHint("write when x { 1, 2 -> ...; in 3..9 -> ...; else -> ... } (without x for boolean conditions)")
			for !p.at(LBRACE) && !p.at(NEWLINE) && !p.at(EOF) {
				p.next()
			}
			if p.at(LBRACE) {
				p.skipBraces()
			}
			bad := &BadStmt{S: p.spanFrom(t.Span)}
			p.damaged = append(p.damaged, bad.S)
			return bad
		}
	case FOR:
		return p.parseFor()
	case WHILE:
		return p.parseWhile()
	case SELECT:
		return p.parseSelect()
	case DEFER, GO:
		return p.parseDeferGo()
	case LBRACE:
		return p.parseBlock()
	case RETURN:
		p.next()
		j := &JumpExpr{Kind: JumpReturn}
		if canStartExpr(p.tok().Kind) {
			j.Values = append(j.Values, p.parseExpr())
			for p.accept(COMMA) {
				p.skipNL()
				j.Values = append(j.Values, p.parseExpr())
			}
		}
		j.S = p.spanFrom(t.Span)
		return &ExprStmt{X: j}
	case FUN:
		p.errorf(diag.EUnsupportedSyntax, t.Span, "local function declarations are not supported").
			WithHint("use a lambda: f := { x: int -> x * 2 }")
		p.syncStmt()
		return &BadStmt{S: p.spanFrom(t.Span)}
	}
	x := p.parseExpr()
	if tup, ok := x.(*TupleExpr); ok && (p.at(ASSIGN) || p.at(DEFINE)) {
		// (a, b) = (b, a): Go's list form has no parentheses.
		p.errorf(diag.EUnsupportedSyntax, tup.S, "assignment lists are written without parentheses").
			WithFix(tup.S, tupleText(tup), "write %s", tupleText(tup))
	}
	lhs := []Expr{x}
	for p.at(COMMA) {
		p.next()
		lhs = append(lhs, p.parseExpr())
	}
	switch p.tok().Kind {
	case ASSIGN, ADD_ASSIGN, SUB_ASSIGN, MUL_ASSIGN, QUO_ASSIGN, REM_ASSIGN,
		AND_ASSIGN, OR_ASSIGN, XOR_ASSIGN, SHL_ASSIGN, SHR_ASSIGN, AND_NOT_ASSIGN:
		op := p.next()
		p.skipNL()
		rhs := p.parseExprList()
		if len(lhs) > 1 {
			x = &TupleExpr{Elems: lhs, S: lhs[0].Span().Join(lhs[len(lhs)-1].Span())}
		}
		return &AssignStmt{Op: op.Kind, Lhs: x, Rhs: rhs, OpSpan: op.Span, S: x.Span().Join(rhs.Span())}
	case INC, DEC:
		op := p.next()
		return &IncDecStmt{X: x, Inc: op.Kind == INC, S: x.Span().Join(op.Span)}
	case DEFINE:
		p.next()
		p.skipNL()
		rhs := p.parseExprList()
		d := &LocalDecl{Mutable: true, Define: true, Value: rhs, S: x.Span().Join(rhs.Span())}
		if tup, ok := x.(*TupleExpr); ok && len(lhs) == 1 {
			lhs = tup.Elems
		}
		for _, e := range lhs {
			id, ok := e.(*Ident)
			if _, isSelf := e.(*ThisExpr); isSelf {
				p.errorf(diag.EUnexpectedToken, e.Span(), "self is the method receiver; it cannot be declared or redeclared").
					WithHint("choose another name")
				id, ok = &Ident{Name: "_", S: e.Span()}, true
			}
			if !ok {
				p.errorf(diag.EUnexpectedToken, e.Span(), "':=' declares names; %s is not a name", e.Span().Short()).
					WithHint("use = to assign to %s", e.Span().Short())
				id = &Ident{Name: "_", S: e.Span()}
			}
			d.Names = append(d.Names, id)
		}
		d.Destructure = len(d.Names) > 1
		return d
	}
	if len(lhs) > 1 {
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected ':=' or '=' after a list of names, found %s", describe(p.tok()))
		return &BadStmt{S: lhs[0].Span().Join(lhs[len(lhs)-1].Span())}
	}
	return &ExprStmt{X: x}
}

// parseExprList parses e or e1, e2, ...; a list becomes a TupleExpr.
func (p *parser) parseExprList() Expr {
	x := p.parseExpr()
	if !p.at(COMMA) {
		return x
	}
	tup := &TupleExpr{Elems: []Expr{x}}
	for p.accept(COMMA) {
		p.skipNL()
		tup.Elems = append(tup.Elems, p.parseExpr())
	}
	tup.S = x.Span().Join(tup.Elems[len(tup.Elems)-1].Span())
	return tup
}

func tupleText(t *TupleExpr) string {
	var parts []string
	for _, e := range t.Elems {
		parts = append(parts, e.Span().Text())
	}
	return strings.Join(parts, ", ")
}

// parseTypedLocalDecl parses `name: Type` or `name: Type = value`.
func (p *parser) parseTypedLocalDecl() Stmt {
	start := p.tok().Span
	d := &LocalDecl{Mutable: true, Names: []*Ident{p.parseIdent("")}}
	p.next() // :
	d.Type = p.parseType()
	switch {
	case p.at(ASSIGN):
		p.next()
		p.skipNL()
		d.Value = p.parseExpr()
	case p.at(DEFINE):
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "a declaration with a type uses '='").
			WithFix(p.tok().Span, "=", "write %s: %s = ...", d.Names[0].Name, d.Type.Span().Text())
		p.next()
		d.Value = p.parseExpr()
	}
	d.S = p.spanFrom(start)
	return d
}

// parseOldLocalDecl parses a Kotlin-style val or var declaration after
// reporting it with the := or name: Type spelling that replaces it.
func (p *parser) parseOldLocalDecl() Stmt {
	kw := p.tok()
	if kw.Kind == VAR && p.peek(1).Kind == IDENT {
		switch p.peek(2).Kind {
		case IDENT, MUL, LBRACK, MAP, CHAN:
			if p.peek(2).Span.StartPos().Line == kw.Span.StartPos().Line {
				return p.parseGoVarDecl()
			}
		}
	}
	d := p.parseLocalDecl()
	ld, ok := d.(*LocalDecl)
	if !ok || len(ld.Names) == 0 {
		return d
	}
	names := make([]string, len(ld.Names))
	for i, id := range ld.Names {
		names[i] = id.Name
	}
	list := strings.Join(names, ", ")
	d2 := p.errorf(diag.EUnsupportedSyntax, kw.Span, "local variables are declared with := (or name: Type = value)")
	switch {
	case ld.Type != nil && ld.Value != nil && !ld.Destructure:
		// val x: T = e and val x: T keep their type: drop the keyword.
		d2.WithFix(kw.Span.Join(ld.Names[0].S.At()), "", "write %s: %s = %s", list, ld.Type.Span().Text(), ld.Value.Span().Short())
	case ld.Type != nil && !ld.Destructure:
		d2.WithFix(kw.Span.Join(ld.Names[0].S.At()), "", "write %s: %s", list, ld.Type.Span().Text())
	case ld.Value != nil && ld.Type == nil:
		d2.WithFix(kw.Span.Join(ld.Value.Span().At()), list+" := ", "write %s := %s", list, ld.Value.Span().Short())
	}
	ld.Define = ld.Type == nil
	ld.Mutable = true
	return ld
}

// parseGoVarDecl parses Go's var x T [= value] in a function body, reported
// with the edit to x: T [= value].
func (p *parser) parseGoVarDecl() Stmt {
	kw := p.next() // var
	name := p.parseIdent("")
	te := p.parseType()
	d := &LocalDecl{Names: []*Ident{name}, Type: te, Mutable: true}
	if p.accept(ASSIGN) {
		p.skipNL()
		d.Value = p.parseExpr()
	}
	d.S = p.spanFrom(kw.Span)
	head := kw.Span.Join(te.Span())
	repl := name.Name + ": " + te.Span().Text()
	p.errorf(diag.EUnsupportedSyntax, head, "local variables are declared with := (or name: Type = value)").
		WithFix(head, repl, "write %s", repl)
	return d
}

func (p *parser) parseLocalDecl() Stmt {
	start := p.tok().Span
	mutable := p.next().Kind == VAR
	d := &LocalDecl{Mutable: mutable}
	if p.at(LPAREN) {
		p.next()
		d.Destructure = true
		for !p.at(RPAREN) && !p.at(EOF) {
			d.Names = append(d.Names, p.parseIdent("in destructuring declaration"))
			if !p.accept(COMMA) {
				break
			}
		}
		p.expect(RPAREN, "to close the destructuring declaration")
		if p.at(COLON) {
			p.errorf(diag.EUnsupportedSyntax, p.tok().Span, "destructuring declarations cannot have a type annotation")
			p.next()
			p.parseType()
		}
	} else {
		d.Names = []*Ident{p.parseIdent("after '" + map[bool]string{true: "var", false: "val"}[mutable] + "'")}
		if p.accept(COLON) {
			d.Type = p.parseType()
		}
	}
	if p.at(ASSIGN) {
		p.next()
		p.skipNL()
		d.Value = p.parseExpr()
	} else if p.at(DEFINE) {
		p.errorf(diag.EColonEquals, p.tok().Span, "use '=' in declarations")
		p.next()
		d.Value = p.parseExpr()
	}
	d.S = p.spanFrom(start)
	return d
}

func (p *parser) parseFor() Stmt {
	start := p.next().Span // for
	s := &ForStmt{}
	if p.at(LPAREN) {
		return p.parseOldFor(start, s)
	}
	if p.at(THIS) {
		p.parseIdent("") // reports self
		s.Names = []*Ident{{Name: "_", S: p.toks[p.pos-1].Span}}
		if p.accept(IN) {
			p.noLambda++
			s.Iter = p.parseExpr()
			p.noLambda--
		}
		s.Body = p.parseHeaderBody("for", s.Iter)
		s.S = p.spanFrom(start)
		if s.Iter == nil {
			s.Iter = &BadExpr{S: s.S}
		}
		return s
	}
	if !p.at(IDENT) {
		d := p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected a loop variable after 'for', found %s", describe(p.tok()))
		if p.at(LBRACE) {
			d.WithHint("a loop without a condition is written while true { ... }")
		} else {
			d.WithHint("Lyth loops are written for x in items { ... }; a condition loop is while cond { ... }")
		}
		p.syncStmt()
		if p.at(LBRACE) {
			s.Body = p.parseBlock()
		} else {
			s.Body = &Block{S: p.tok().Span.At()}
		}
		s.S = p.spanFrom(start)
		s.Iter = &BadExpr{S: s.S}
		return s
	}
	s.Names = []*Ident{p.parseIdent("for the loop variable")}
	if p.accept(COMMA) {
		s.Names = append(s.Names, p.parseIdent("for the second loop variable"))
	}
	switch {
	case p.at(IN):
		p.next()
		p.skipNL()
		p.noLambda++
		s.Iter = p.parseExpr()
		p.noLambda--
		p.kotlinRange(s)
	case p.at(DEFINE) && p.peek(1).Kind == IDENT && p.peek(1).Text == "range":
		// Go's for i, x := range xs. Lyth's in binds the element first.
		def := p.next()
		p.next() // range
		p.noLambda++
		s.Iter = p.parseExpr()
		p.noLambda--
		d := p.errorf(diag.EUnsupportedSyntax, def.Span.Join(p.toks[p.pos-1].Span.At()), "Lyth iterates with in, not := range")
		names := make([]string, len(s.Names))
		for i, id := range s.Names {
			names[i] = id.Name
		}
		if len(s.Names) == 2 {
			if names[0] == "_" {
				d.WithHint("write for %s in %s (the element comes first)", names[1], s.Iter.Span().Text())
			} else {
				d.WithHint("write for %s, %s in %s", names[0], names[1], s.Iter.Span().Text())
			}
		} else {
			d.WithHint("for %s in %s binds elements; for the index of a slice write for %s, _ in %s, for the numbers 0 to n-1 write for %s in 0..<n", names[0], s.Iter.Span().Text(), names[0], s.Iter.Span().Text(), names[0])
		}
	default:
		if rng, ok := p.goCountingFor(s, LBRACE); ok {
			header := s.Names[0].S.Join(p.toks[p.pos-1].Span)
			name, exact := p.countingRewrite(s, rng)
			repl := name + " in " + rng
			d := p.errorf(diag.EUnsupportedSyntax, header, "Lyth has no three-part for loop")
			if exact {
				d.WithFix(header, repl, "write for %s", repl)
			} else {
				d.WithEdit(header, repl, "write for %s if the body does not change the bound (the range is evaluated once), or use while", repl)
			}
			break
		}
		d := p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected 'in' in for loop, found %s", describe(p.tok()))
		if p.at(DEFINE) || p.at(ASSIGN) || p.at(SEMI) {
			d.WithHint("counting loops are written for i in 0..<n { ... }; for another step, use while: i := 0; while i < n { ...; i += 2 }")
		} else {
			d.WithHint("for iterates (for x in xs { ... }); a loop on a condition is while cond { ... }")
		}
		for !p.at(EOF) && !p.at(LBRACE) && !p.at(NEWLINE) {
			p.next()
		}
	}
	s.Body = p.parseHeaderBody("for", s.Iter)
	s.S = p.spanFrom(start)
	if s.Iter == nil {
		s.Iter = &BadExpr{S: s.S}
	}
	return s
}

// kotlinRange reports Kotlin's infix range functions after a for loop's
// iterable (for i in 0 until n, n downTo 0, 0..n step 2) and skips them.
func (p *parser) kotlinRange(s *ForStmt) {
	t := p.tok()
	if t.Kind != IDENT || (t.Text != "until" && t.Text != "downTo" && t.Text != "step") {
		return
	}
	p.next()
	p.noLambda++
	arg := p.parseExpr()
	p.noLambda--
	span := s.Iter.Span().Join(arg.Span())
	d := p.errorf(diag.EUnsupportedSyntax, t.Span, "Lyth has no %s", t.Text)
	switch t.Text {
	case "until":
		repl := s.Iter.Span().Text() + "..<" + arg.Span().Text()
		d.WithFix(span, repl, "write %s", repl)
		s.Iter = &RangeExpr{Lo: s.Iter, Hi: arg, Exclusive: true, S: span}
		return
	case "downTo":
		d.WithHint("count down with while: i := %s; while i >= %s { ...; i-- }", s.Iter.Span().Text(), arg.Span().Text())
	default:
		d.WithHint("for another step, use while: i := 0; while i <= n { ...; i += %s }", arg.Span().Text())
	}
	p.damaged = append(p.damaged, span)
	for !p.at(LBRACE) && !p.at(NEWLINE) && !p.at(EOF) {
		p.next()
	}
}

// goCountingFor recognizes Go's counting loop, for i := lo; i < hi; i++,
// up to closer (the body's { or a parenthesized header's )), and gives s
// the range lo..<hi. It returns false, having consumed nothing, for any
// other three-part loop.
func (p *parser) goCountingFor(s *ForStmt, closer Kind) (string, bool) {
	if len(s.Names) != 1 || !p.at(DEFINE) {
		return "", false
	}
	save, errs, damaged := p.pos, p.sink.Len(), len(p.damaged)
	name := s.Names[0]
	fail := func() (string, bool) {
		p.pos, p.damaged = save, p.damaged[:damaged]
		p.sink.Truncate(errs)
		return "", false
	}
	p.next() // :=
	p.noLambda++
	lo := p.parseExpr()
	p.noLambda--
	if !p.accept(SEMI) {
		return fail()
	}
	p.noLambda++
	cond := p.parseExpr()
	p.noLambda--
	b, ok := cond.(*BinaryExpr)
	if !ok || (b.Op != LSS && b.Op != LEQ) || !isIdentNamed(b.X, name.Name) || !p.accept(SEMI) {
		return fail()
	}
	if !p.at(IDENT) || p.tok().Text != name.Name || p.peek(1).Kind != INC || p.peek(2).Kind != closer {
		return fail()
	}
	p.next()
	p.next() // ++
	if p.sink.Len() != errs {
		return fail()
	}
	s.Iter = &RangeExpr{Lo: lo, Hi: b.Y, Exclusive: b.Op == LSS, S: lo.Span().Join(b.Y.Span())}
	op := "..<"
	if b.Op == LEQ {
		op = ".."
	}
	return lo.Span().Text() + op + b.Y.Span().Text(), true
}

// countingRewrite decides how a Go counting loop recognized by
// goCountingFor is rewritten: the loop variable's name (_ when the body
// does not use it), and whether the rewrite is exact. It is not when the
// body mentions a name the bound uses: for i := 0; i < len(xs); i++ reads
// len(xs) on every iteration, a Lyth range once. The body starts at the
// next '{'.
func (p *parser) countingRewrite(s *ForStmt, rng string) (string, bool) {
	r, ok := s.Iter.(*RangeExpr)
	if !ok {
		return s.Names[0].Name, false
	}
	bound := map[string]bool{}
	Walk(r.Hi, func(n Node) {
		if id, ok := n.(*Ident); ok {
			bound[id.Name] = true
		}
	})
	i := p.pos
	for i < len(p.toks) && p.toks[i].Kind != LBRACE {
		i++
	}
	exact, used := true, false
	depth := 0
	for ; i < len(p.toks); i++ {
		t := p.toks[i]
		switch t.Kind {
		case LBRACE:
			depth++
		case RBRACE:
			depth--
		case IDENT:
			if bound[t.Text] {
				exact = false
			}
			if t.Text == s.Names[0].Name {
				used = true
			}
		}
		if depth == 0 || t.Kind == EOF {
			break
		}
	}
	name := s.Names[0].Name
	if !used {
		name = "_"
	}
	return name, exact
}

func isIdentNamed(e Expr, name string) bool {
	id, ok := e.(*Ident)
	return ok && id.Name == name
}

// parseOldFor parses for ((a, b) in e) after reporting the parentheses.
func (p *parser) parseOldFor(start source.Span, s *ForStmt) Stmt {
	open := p.next()
	if p.at(LPAREN) {
		p.next()
		for !p.at(RPAREN) && !p.at(EOF) {
			s.Names = append(s.Names, p.parseIdent("in for-loop destructuring"))
			if !p.accept(COMMA) {
				break
			}
		}
		p.expect(RPAREN, "")
	} else {
		s.Names = []*Ident{p.parseIdent("for the loop variable")}
		for p.at(COMMA) && p.peek(1).Kind == IDENT {
			p.next()
			s.Names = append(s.Names, p.parseIdent(""))
		}
	}
	counting := ""
	if !p.at(IN) {
		if rng, ok := p.goCountingFor(s, RPAREN); ok {
			counting = rng
		} else {
			d := p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected 'in' in for loop, found %s", describe(p.tok()))
			if p.at(DEFINE) || p.at(ASSIGN) || p.at(SEMI) {
				d.WithHint("counting loops are written for i in 0..<n { ... }; for another step, use while: i := 0; while i < n { ...; i += 2 }")
			}
			for !p.at(RPAREN) && !p.at(EOF) && !p.at(LBRACE) {
				p.next()
			}
		}
	} else {
		p.next()
		s.Iter = p.parseExpr()
	}
	end := p.expect(RPAREN, "to close the for header")
	names := make([]string, len(s.Names))
	for i, id := range s.Names {
		names[i] = id.Name
	}
	iter := counting
	if s.Iter != nil && iter == "" {
		iter = s.Iter.Span().Text()
	}
	d := p.errorf(diag.EUnsupportedSyntax, open.Span.Join(end.Span), "for loops have no parentheses")
	if counting != "" {
		d.Message = "Lyth has no three-part for loop"
	}
	if s.Iter != nil {
		exact := true
		if counting != "" {
			names[0], exact = p.countingRewrite(s, counting)
		}
		if exact {
			d.WithFix(open.Span.Join(end.Span), strings.Join(names, ", ")+" in "+iter, "write for %s in %s", strings.Join(names, ", "), iter)
		} else {
			d.WithEdit(open.Span.Join(end.Span), strings.Join(names, ", ")+" in "+iter, "write for %s in %s if the body does not change the bound (the range is evaluated once), or use while", strings.Join(names, ", "), iter)
		}
	}
	p.skipNL()
	s.Body = p.parseBody()
	s.S = p.spanFrom(start)
	if s.Iter == nil {
		s.Iter = &BadExpr{S: s.S}
	}
	return s
}

func (p *parser) parseWhile() Stmt {
	start := p.next().Span
	s := &WhileStmt{}
	s.Cond = p.parseCondition()
	s.Body = p.parseHeaderBody("while", s.Cond)
	s.S = p.spanFrom(start)
	return s
}

// parseCondition parses the condition of an if or while. As in Go it is not
// parenthesized, so it cannot end in a trailing lambda: the { starts the
// body. A parenthesized condition is an ordinary parenthesized expression.
func (p *parser) parseCondition() Expr {
	p.noLambda++
	x := p.parseExpr()
	p.noLambda--
	return x
}

// parseHeaderBody parses the block after an if, while, or for header,
// reporting a body that is not a block.
func (p *parser) parseHeaderBody(keyword string, header Expr) *Block {
	if t := endsInTypeOperand(header); t != nil && p.at(LBRACE) {
		// for x in []int{1, 2} { ... }: a type is never a header's value,
		// so these braces are the literal's; the body follows them.
		p.skipBraces()
		lit := t.S.Join(p.toks[p.pos-1].Span)
		p.errorf(diag.EUnsupportedSyntax, lit, "a typed literal in a %s header must be parenthesized: its { would start the body", keyword).
			WithFix(lit, "("+lit.Text()+")", "write (%s)", lit.Text())
		if header != nil {
			p.damaged = append(p.damaged, header.Span().Join(p.toks[p.pos-1].Span))
		}
	}
	if p.at(LBRACE) && header != nil && endsInTypeOperand(header) == nil {
		// if xs.Any { x -> x > 0 } { ... }: braces followed by braces are a
		// trailing lambda and the body. Skip the lambda unparsed: read as a
		// block, its parameters would only add errors.
		save := p.pos
		p.skipBraces()
		if p.at(LBRACE) {
			lambda := p.toks[save].Span.Join(p.toks[p.pos-1].Span)
			full := header.Span().Join(lambda)
			p.errorf(diag.EUnsupportedSyntax, lambda, "the %s header cannot end in a trailing lambda: its { starts the body", keyword).
				WithFix(full, "("+full.Text()+")", "parenthesize the header expression: (%s)", full.Text())
			p.damaged = append(p.damaged, full)
			return p.parseBlock()
		}
		p.pos = save
	}
	if p.at(LBRACE) {
		b := p.parseBlock()
		if p.at(LBRACE) {
			// if xs.Any { it > 0 } { ... }: the lambda ended the header.
			if t := endsInTypeOperand(header); t != nil {
				p.errorf(diag.EUnsupportedSyntax, b.S, "a typed literal in a %s header must be parenthesized: its { starts the body", keyword).
					WithHint("parenthesize it: (%s{...})", t.Span().Text())
			} else {
				p.errorf(diag.EUnsupportedSyntax, b.S, "the %s header cannot end in a trailing lambda: its { starts the body", keyword).
					WithHint("parenthesize the header expression: %s (xs.Any { ... }) { ... }", keyword)
			}
			if header != nil {
				// The header was read without its lambda; checking it would
				// only report the same mistake again.
				p.damaged = append(p.damaged, header.Span().Join(b.S))
			}
			return p.parseBlock()
		}
		return b
	}
	if p.at(NEWLINE) && p.peekPastNL().Kind == LBRACE {
		p.skipNL()
		return p.parseBlock()
	}
	t := p.tok()
	if t.Kind == NEWLINE || t.Kind == EOF || t.Kind == RBRACE {
		p.errorf(diag.EUnexpectedToken, t.Span, "expected '{' after the %s header, found %s", keyword, describe(t))
		return &Block{S: t.Span.At()}
	}
	reported := p.sink.Len()
	stmt := p.parseStmt()
	if stmt == nil {
		return &Block{S: t.Span.At()}
	}
	d := p.errorf(diag.EUnsupportedSyntax, stmt.Span(), "%s bodies are blocks", keyword)
	if p.sink.Len() == reported+1 {
		d.WithFix(stmt.Span(), "{ "+stmt.Span().Text()+" }", "write { %s }", stmt.Span().Text())
	} else {
		// The statement itself did not parse: braces would not mend it.
		d.WithHint("write the body in braces: { ... }")
	}
	return &Block{Stmts: []Stmt{stmt}, S: stmt.Span()}
}

func (p *parser) parseDeferGo() Stmt {
	t := p.next()
	if p.at(LBRACE) {
		b := p.parseBlock()
		if t.Kind == DEFER {
			return &DeferStmt{Body: b, S: t.Span.Join(b.S)}
		}
		return &GoStmt{Body: b, S: t.Span.Join(b.S)}
	}
	if p.at(FUN) && p.peek(1).Kind == LPAREN && p.peek(2).Kind == RPAREN && p.peek(3).Kind == LBRACE {
		// Go's go func() { ... }(): Lyth's go and defer take the block.
		fn := p.next()
		p.next()
		p.next()
		b := p.parseBlock()
		if p.at(LPAREN) && p.peek(1).Kind == RPAREN {
			p.next()
			end := p.next()
			span := fn.Span.Join(end.Span)
			p.errorf(diag.EUnsupportedSyntax, span, "%s takes a block: %s { ... }", t.Text, t.Text).
				WithFix(span, b.S.Text(), "write %s { ... }", t.Text)
			if t.Kind == DEFER {
				return &DeferStmt{Body: b, S: t.Span.Join(end.Span)}
			}
			return &GoStmt{Body: b, S: t.Span.Join(end.Span)}
		}
		p.errorf(diag.EUnsupportedSyntax, fn.Span, "anonymous functions are written as lambdas").
			WithHint("%s takes a block: %s { ... }", t.Text, t.Text)
		p.syncStmt()
		return &BadStmt{S: p.spanFrom(t.Span)}
	}
	x := p.parseExpr()
	if t.Kind == DEFER {
		return &DeferStmt{Call: x, S: t.Span.Join(x.Span())}
	}
	return &GoStmt{Call: x, S: t.Span.Join(x.Span())}
}

// ---------------------------------------------------------------------------
// Expressions

var binPrec = map[Kind]int{
	LOR: 1, LAND: 2,
	EQL: 3, NEQ: 3,
	LSS: 4, LEQ: 4, GTR: 4, GEQ: 4,
	IN: 5, IS: 5,
	ELVIS: 6,
	RANGE: 7, RANGE_EXCL: 7,
	ADD: 8, SUB: 8, OR: 8, XOR: 8,
	MUL: 9, QUO: 9, REM: 9, SHL: 9, SHR: 9, AND: 9, AND_NOT: 9,
	AS: 10,
}

func (p *parser) parseExpr() Expr { return p.parseBinary(1) }

func (p *parser) parseBinary(minPrec int) Expr {
	x := p.parseUnary()
	for {
		p.continueLine(ELVIS, LAND, LOR)
		t := p.tok()
		kind := t.Kind
		not := false
		if kind == NOT && (p.peek(1).Kind == IN || p.peek(1).Kind == IS) {
			not = true
			kind = p.peek(1).Kind
		}
		prec, ok := binPrec[kind]
		if !ok || prec < minPrec {
			return x
		}
		opStart := p.next().Span
		if not {
			opStart = opStart.Join(p.next().Span)
		}
		switch kind {
		case ELVIS:
			p.skipNL()
			y := p.parseBinary(prec) // right-associative
			x = &ElvisExpr{X: x, Y: y, OpSpan: opStart, S: x.Span().Join(y.Span())}
		case IS:
			typ := p.parseType()
			x = &IsExpr{X: x, Type: typ, Not: not, S: x.Span().Join(typ.Span())}
		case AS:
			safe := false
			if p.at(QUESTION) && p.tok().Span.Start == opStart.End {
				p.next()
				safe = true
			}
			typ := p.parseType()
			x = &AsExpr{X: x, Type: typ, Safe: safe, S: x.Span().Join(typ.Span())}
		case IN:
			p.skipNL()
			y := p.parseBinary(prec + 1)
			x = &InExpr{X: x, Y: y, Not: not, S: x.Span().Join(y.Span())}
		case RANGE, RANGE_EXCL:
			p.skipNL()
			y := p.parseBinary(prec + 1)
			x = &RangeExpr{Lo: x, Hi: y, Exclusive: kind == RANGE_EXCL, S: x.Span().Join(y.Span())}
		default:
			p.skipNL()
			y := p.parseBinary(prec + 1)
			x = &BinaryExpr{Op: kind, X: x, Y: y, OpSpan: opStart, S: x.Span().Join(y.Span())}
		}
	}
}

func (p *parser) parseUnary() Expr {
	t := p.tok()
	switch t.Kind {
	case SUB, ADD, NOT, XOR, AND, MUL:
		p.next()
		x := p.parseUnary()
		return &UnaryExpr{Op: t.Kind, X: x, OpSpan: t.Span, S: t.Span.Join(x.Span())}
	case LSS:
		if minus := p.peek(1); minus.Kind == SUB && minus.Span.Start == t.Span.End {
			// Go's receive operator, <-ch.
			p.next()
			p.next()
			x := p.parseUnary()
			p.errorf(diag.EUnsupportedSyntax, t.Span.Join(minus.Span), "Lyth receives from a channel with a method").
				WithHint("write %s.receive()", x.Span().Text())
			return &BadExpr{S: t.Span.Join(x.Span())}
		}
	case NOT_NOT:
		// '!!x' in prefix position is double negation.
		p.next()
		x := p.parseUnary()
		first := source.Span{File: t.Span.File, Start: t.Span.Start, End: t.Span.Start + 1}
		second := source.Span{File: t.Span.File, Start: t.Span.Start + 1, End: t.Span.End}
		inner := &UnaryExpr{Op: NOT, X: x, OpSpan: second, S: second.Join(x.Span())}
		return &UnaryExpr{Op: NOT, X: inner, OpSpan: first, S: t.Span.Join(x.Span())}
	}
	return p.parsePostfix(p.parsePrimary())
}

func isTypeNameExpr(x Expr) bool {
	switch x := x.(type) {
	case *Ident:
		return true
	case *SelectorExpr:
		_, ok := x.X.(*Ident)
		return ok && !x.Safe
	}
	return false
}

// startsSliceElem reports whether a token directly after `[]` begins the
// element type of a slice type rather than following an empty list literal.
func startsSliceElem(k Kind) bool {
	return k == IDENT || k == MUL || k == LBRACK || k == LPAREN || k == MAP || k == CHAN || k == STRUCT || k == LSS
}

// canTakeTrailingLambda reports whether `x { ... }` is a call of x with a
// trailing lambda: x must name something callable (identifier, selector, or
// instantiation).
func canTakeTrailingLambda(x Expr) bool {
	switch x.(type) {
	case *Ident, *SelectorExpr, *InstantiateExpr:
		return true
	}
	return false
}

func (p *parser) parsePostfix(x Expr) Expr {
	for {
		p.continueLine(DOT, SAFE_DOT, CATCH)
		t := p.tok()
		switch t.Kind {
		case DOT, SAFE_DOT:
			p.next()
			p.skipNL()
			if k := p.tok(); k.Kind.IsKeyword() && k.Kind != THIS {
				// After a dot a keyword can only be a member name (xs.map
				// from Kotlin): let the checker report the member.
				p.toks[p.pos].Kind = IDENT
			}
			sel := p.parseIdent("after '" + t.Kind.String() + "'")
			x = &SelectorExpr{X: x, Sel: sel, Safe: t.Kind == SAFE_DOT, OpSpan: t.Span, S: x.Span().Join(sel.S)}
		case LSS:
			if !isTypeNameExpr(x) {
				return x
			}
			lt := p.tok().Span
			args, ok := p.tryTypeArgs()
			if !ok {
				return x
			}
			p.oldTypeArgs(lt.Join(p.toks[p.pos-1].Span), args)
			x = &InstantiateExpr{X: x, Args: args, S: p.spanFrom(x.Span())}
		case LPAREN:
			x = p.parseCall(x)
		case LBRACK:
			x = p.parseIndex(x)
		case QUESTION:
			p.next()
			x = &TryExpr{X: x, OpSpan: t.Span, S: x.Span().Join(t.Span)}
		case NOT_NOT:
			p.next()
			x = &NotNullExpr{X: x, OpSpan: t.Span, S: x.Span().Join(t.Span)}
		case CATCH:
			x = p.parseCatch(x)
		case LBRACE:
			if t, ok := x.(*TypeOperand); ok {
				// []T{...}, [N]T{...}, map[K]V{...}: Go's typed literals. In a
				// header the { starts the body, as in Go.
				if p.noLambda > 0 || p.tok().Span.StartPos().Line != x.Span().EndPos().Line {
					return x
				}
				if lit := p.parseTypedLit(t); lit != nil {
					x = lit
					continue
				}
				return x
			}
			if p.noLambda == 0 {
				if bad := p.goCompositeLit(x); bad != nil {
					x = bad
					continue
				}
			}
			if p.noLambda > 0 || !canTakeTrailingLambda(x) {
				return x
			}
			lam := p.parseLambda()
			x = &CallExpr{Fun: x, Trailing: lam, S: x.Span().Join(lam.S)}
		default:
			return x
		}
	}
}

func (p *parser) parseCall(fun Expr) Expr {
	lp := p.next()
	c := &CallExpr{Fun: fun, Lparen: lp.Span}
	saved := p.noLambda
	p.noLambda = 0
	for !p.at(RPAREN) && !p.at(EOF) {
		start := p.tok().Span
		a := &Arg{}
		if p.at(IDENT) && p.peek(1).Kind == ASSIGN {
			a.Name = p.parseIdent("")
			p.next() // =
		}
		a.Value = p.parseExpr()
		if p.at(ELLIPSIS) {
			p.next()
			a.Spread = true
		}
		a.S = p.spanFrom(start)
		c.Args = append(c.Args, a)
		if !p.accept(COMMA) {
			break
		}
	}
	rp := p.expect(RPAREN, "to close the argument list")
	p.noLambda = saved
	c.Rparen = rp.Span
	if p.at(LBRACE) && p.noLambda == 0 {
		c.Trailing = p.parseLambda()
	}
	c.S = p.spanFrom(fun.Span())
	return c
}

// parseIndex parses x[i], x[lo:hi], or the type arguments x[A, B]. One
// operand is an IndexExpr even when it names a type (f[int]); the checker
// decides between indexing and instantiation, as go/types does.
func (p *parser) parseIndex(x Expr) Expr {
	saved := p.noLambda
	p.noLambda = 0
	defer func() { p.noLambda = saved }()
	p.next() // [
	var lo, hi Expr
	if !p.at(COLON) {
		lo = p.parseTypeOrExpr()
	}
	if p.at(COMMA) {
		args := []TypeExpr{ExprToType(lo)}
		for p.accept(COMMA) {
			args = append(args, ExprToType(p.parseTypeOrExpr()))
		}
		p.expect(RBRACK, "to close the type arguments")
		for _, a := range args {
			if b, ok := a.(*BadType); ok {
				p.errorf(diag.EUnexpectedToken, b.S, "expected a type argument")
			}
		}
		return &InstantiateExpr{X: x, Args: args, S: p.spanFrom(x.Span())}
	}
	if p.accept(COLON) {
		if !p.at(RBRACK) {
			hi = p.parseExpr()
		}
		p.expect(RBRACK, "to close the slice expression")
		return &SliceExpr{X: x, Lo: lo, Hi: hi, S: p.spanFrom(x.Span())}
	}
	p.expect(RBRACK, "to close the index expression")
	return &IndexExpr{X: x, Index: lo, S: p.spanFrom(x.Span())}
}

// parseTypeOrExpr parses an index operand, which may be a type that has no
// expression form (map[K]V, chan T, []T, a function type).
func (p *parser) parseTypeOrExpr() Expr {
	switch {
	case p.at(MAP), p.at(CHAN),
		p.at(LBRACK) && p.peek(1).Kind == RBRACK,
		p.at(LSS) && p.peek(1).Kind == SUB && p.peek(2).Kind == CHAN:
		t := p.parseType()
		return &TypeOperand{Type: t, S: t.Span()}
	}
	return p.parseExpr()
}

// ExprToType converts an expression that spells a type (an index operand
// read before the checker knew it was a type argument) to that type, or
// returns a BadType.
func ExprToType(e Expr) TypeExpr {
	switch e := e.(type) {
	case *Ident:
		return &NamedType{Name: e, S: e.S}
	case *SelectorExpr:
		if pkg, ok := e.X.(*Ident); ok && !e.Safe {
			return &NamedType{Pkg: pkg, Name: e.Sel, S: e.S}
		}
	case *ParenExpr:
		return ExprToType(e.X)
	case *UnaryExpr:
		if e.Op == MUL {
			return &PointerType{Elem: ExprToType(e.X), S: e.S}
		}
	case *TryExpr:
		return &NullableType{Elem: ExprToType(e.X), S: e.S}
	case *TypeOperand:
		return e.Type
	case *IndexExpr:
		if nt, ok := ExprToType(e.X).(*NamedType); ok && len(nt.Args) == 0 {
			return &NamedType{Pkg: nt.Pkg, Name: nt.Name, Args: []TypeExpr{ExprToType(e.Index)}, S: e.S}
		}
	case *InstantiateExpr:
		if nt, ok := ExprToType(e.X).(*NamedType); ok && len(nt.Args) == 0 {
			return &NamedType{Pkg: nt.Pkg, Name: nt.Name, Args: e.Args, S: e.S}
		}
	}
	if e == nil {
		return &BadType{}
	}
	return &BadType{S: e.Span()}
}

func (p *parser) parseCatch(x Expr) Expr {
	kw := p.next()
	c := &CatchExpr{X: x, CatchSpan: kw.Span}
	if p.at(LPAREN) {
		p.next()
		c.Binder = p.parseIdent("for the error binding")
		if p.accept(COLON) {
			c.Type = p.parseType()
		}
		p.expect(RPAREN, "after the error binding")
	}
	if !p.at(LBRACE) {
		p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected '{' to start the catch block, found %s", describe(p.tok()))
		c.Body = &Block{S: p.tok().Span.At()}
	} else {
		c.Body = p.parseBlock()
	}
	c.S = x.Span().Join(c.Body.S)
	return c
}

func (p *parser) parsePrimary() Expr {
	t := p.tok()
	switch t.Kind {
	case IDENT:
		p.next()
		if t.Text == "this" {
			p.errorf(diag.EUnsupportedSyntax, t.Span, "the receiver is called self").
				WithFix(t.Span, "self", "write self")
			return &ThisExpr{S: t.Span}
		}
		return &Ident{Name: t.Text, S: t.Span}
	case INT, FLOAT, RUNE, STRING, RAWSTRING:
		p.next()
		return &BasicLit{Kind: t.Kind, Raw: t.Text, Value: t.Value, S: t.Span}
	case TEMPLATE_START:
		return p.parseTemplate()
	case TRUE, FALSE:
		p.next()
		return &BoolLit{Value: t.Kind == TRUE, S: t.Span}
	case NULL:
		p.next()
		return &NullLit{S: t.Span}
	case THIS:
		p.next()
		return &ThisExpr{S: t.Span}
	case LPAREN:
		p.next()
		saved := p.noLambda
		p.noLambda = 0
		defer func() { p.noLambda = saved }()
		p.skipNL()
		x := p.parseExpr()
		p.skipNL()
		if p.at(COMMA) {
			// (a, b): a tuple, for assignments and destructuring
			tup := &TupleExpr{Elems: []Expr{x}}
			for p.accept(COMMA) {
				p.skipNL()
				tup.Elems = append(tup.Elems, p.parseExpr())
				p.skipNL()
			}
			rp := p.expect(RPAREN, "to close the tuple")
			tup.S = t.Span.Join(rp.Span)
			return tup
		}
		rp := p.expect(RPAREN, "to close the parenthesized expression")
		return &ParenExpr{X: x, S: t.Span.Join(rp.Span)}
	case LBRACK:
		return nested(p, p.parseCollectionLit)
	case MAP, CHAN:
		// make(map[string]int), make(chan int, n): a type used as an operand.
		t := p.parseType()
		return &TypeOperand{Type: t, S: t.Span()}
	case STRUCT:
		if p.peek(1).Kind == LBRACE && p.peek(2).Kind == RBRACE {
			if p.peek(3).Kind == LBRACE && p.peek(4).Kind == RBRACE {
				// struct{}{}: the empty struct value.
				for range 5 {
					p.next()
				}
				return &EmptyStructLit{S: p.spanFrom(t.Span)}
			}
			st := p.parseType()
			return &TypeOperand{Type: st, S: st.Span()}
		}
	case LSS:
		if p.peek(1).Kind == SUB && p.peek(2).Kind == CHAN {
			t := p.parseType()
			return &TypeOperand{Type: t, S: t.Span()}
		}
	case LBRACE:
		return nested(p, func() Expr { return p.parseLambda() })
	case IF:
		return p.parseIf()
	case WHEN:
		return p.parseWhen()
	case RETURN:
		p.next()
		j := &JumpExpr{Kind: JumpReturn}
		if canStartExpr(p.tok().Kind) {
			j.Values = []Expr{p.parseExpr()}
		}
		j.S = p.spanFrom(t.Span)
		return j
	case THROW:
		p.next()
		j := &JumpExpr{Kind: JumpThrow}
		if canStartExpr(p.tok().Kind) {
			j.Values = []Expr{p.parseExpr()}
		} else {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected an error value after 'throw'")
		}
		j.S = p.spanFrom(t.Span)
		return j
	case BREAK, CONTINUE:
		p.next()
		k := JumpBreak
		if t.Kind == CONTINUE {
			k = JumpContinue
		}
		j := &JumpExpr{Kind: k, S: t.Span}
		if p.at(AT) && p.tok().Span.Start == t.Span.End {
			// break@outer
			p.next()
			j.Label = p.parseIdent("for the loop label")
			j.S = t.Span.Join(j.Label.S)
		} else if l := p.tok(); l.Kind == IDENT && p.file.Position(l.Span.Start).Line == p.file.Position(t.Span.Start).Line {
			// Go's break outer.
			span := t.Span.Join(l.Span)
			p.errorf(diag.EUnsupportedSyntax, span, "a label follows %s with @", t.Text).
				WithFix(span, t.Text+"@"+l.Text, "write %s@%s", t.Text, l.Text)
			p.next()
			j.Label = &Ident{Name: l.Text, S: l.Span}
			j.S = span
		}
		return j
	case FUN:
		p.errorf(diag.EUnsupportedSyntax, t.Span, "anonymous functions are written as lambdas").
			WithHint("write { x: int -> x * 2 }")
		p.next()
		return &BadExpr{S: t.Span}
	}
	if t.Kind == ILLEGAL {
		p.next() // the lexer reported it
		return &BadExpr{S: t.Span}
	}
	d := p.errorf(diag.EUnexpectedToken, t.Span, "expected expression, found %s", describe(t))
	if t.Kind == DEFINE {
		d.WithHint("':=' declares the names on its left: name := value")
	}
	switch t.Kind {
	case NEWLINE, RPAREN, RBRACK, RBRACE, EOF, COMMA, SEMI, TEMPLATE_EXPR_END:
	default:
		p.next()
	}
	return &BadExpr{S: t.Span}
}

func (p *parser) parseTemplate() Expr {
	start := p.next().Span // TEMPLATE_START
	lit := &TemplateLit{}
	for {
		t := p.tok()
		switch t.Kind {
		case TEMPLATE_TEXT:
			p.next()
			lit.Parts = append(lit.Parts, &TemplatePart{Text: t.Value, S: t.Span})
		case TEMPLATE_EXPR_START:
			p.next()
			if p.at(TEMPLATE_EXPR_END) {
				p.errorf(diag.EUnexpectedToken, t.Span.Join(p.tok().Span), "empty ${} in string template")
				p.next()
				continue
			}
			x := p.parseExpr()
			end := p.expect(TEMPLATE_EXPR_END, "to close ${")
			lit.Parts = append(lit.Parts, &TemplatePart{X: x, S: t.Span.Join(end.Span)})
			if end.Text == "" && !p.at(TEMPLATE_TEXT) && !p.at(TEMPLATE_EXPR_START) && !p.at(TEMPLATE_END) {
				// Recover from a malformed template: skip to its end.
				for !p.at(TEMPLATE_END) && !p.at(NEWLINE) && !p.at(EOF) {
					p.next()
				}
			}
		case TEMPLATE_END:
			p.next()
			lit.S = start.Join(t.Span)
			return lit
		default:
			lit.S = p.spanFrom(start)
			return lit
		}
	}
}

func (p *parser) parseCollectionLit() Expr {
	start := p.next().Span // [
	if p.at(RBRACK) {
		if k := p.peek(1); k.Span.Start == p.tok().Span.End && startsSliceElem(k.Kind) {
			// []Byte(s), make([]Int, n): a slice type used as an operand.
			p.pos-- // back to [
			t := p.parsePrefixType()
			return &TypeOperand{Type: t, S: t.Span()}
		}
		end := p.next()
		return &ListLit{S: start.Join(end.Span)}
	}
	if p.at(COLON) && p.peek(1).Kind == RBRACK {
		p.next()
		end := p.next()
		return &MapLit{S: start.Join(end.Span)}
	}
	if p.at(ELLIPSIS) && p.peek(1).Kind == RBRACK {
		// [...]T{a, b}: an array type whose length the literal gives.
		p.pos-- // back to [
		t := p.parsePrefixType()
		return &TypeOperand{Type: t, S: t.Span()}
	}
	first := p.parseExpr()
	if p.at(RBRACK) {
		if k := p.peek(1); k.Span.Start == p.tok().Span.End && startsSliceElem(k.Kind) {
			// [3]int{1, 2, 3}, [4]byte(b): an array type used as an operand.
			end := p.next()
			elem := p.parseType()
			t := &ArrayType{Len: first, Elem: elem, S: start.Join(end.Span).Join(elem.Span())}
			return &TypeOperand{Type: t, S: t.S}
		}
	}
	if p.at(COLON) {
		m := &MapLit{}
		p.next()
		m.Keys = append(m.Keys, first)
		m.Values = append(m.Values, p.parseExpr())
		for p.accept(COMMA) {
			if p.at(RBRACK) {
				break
			}
			k := p.parseExpr()
			p.expect(COLON, "in map literal")
			m.Keys = append(m.Keys, k)
			m.Values = append(m.Values, p.parseExpr())
		}
		end := p.expect(RBRACK, "to close the map literal")
		m.S = start.Join(end.Span)
		return m
	}
	l := &ListLit{Elems: []Expr{first}}
	for p.accept(COMMA) {
		if p.at(RBRACK) {
			break
		}
		l.Elems = append(l.Elems, p.parseExpr())
	}
	end := p.expect(RBRACK, "to close the list literal")
	l.S = start.Join(end.Span)
	return l
}

// goSwitch reports whether the statement at the cursor is Go's switch:
// switch [x] { case ... }. switch is an ordinary identifier in Lyth, so a
// call switch { ... } of a function of that name is not one.
func (p *parser) goSwitch() bool {
	i := p.pos + 1
	for i < len(p.toks) && p.toks[i].Kind != LBRACE {
		if k := p.toks[i].Kind; k == NEWLINE || k == EOF || k == DEFINE || k == ASSIGN {
			return false
		}
		i++
	}
	for i++; i < len(p.toks) && p.toks[i].Kind == NEWLINE; i++ {
	}
	if i >= len(p.toks) {
		return false
	}
	t := p.toks[i]
	if t.Kind != IDENT || (t.Text != "case" && t.Text != "default") {
		return false
	}
	// case x: or default: — a ':' ends Go's case clause on its line.
	line := p.file.Position(t.Span.Start).Line
	for i++; i < len(p.toks) && p.toks[i].Kind != EOF && p.file.Position(p.toks[i].Span.Start).Line == line; i++ {
		if p.toks[i].Kind == COLON {
			return true
		}
	}
	return false
}

// headerHasInit reports whether the if header starting at the cursor has a
// Go initializer statement: a ';' before the body's '{' on the same line.
func (p *parser) headerHasInit() bool {
	depth := 0
	for i := p.pos; i < len(p.toks); i++ {
		switch p.toks[i].Kind {
		case LPAREN, LBRACK:
			depth++
		case RPAREN, RBRACK:
			depth--
		case LBRACE:
			if depth == 0 {
				return false
			}
			depth++
		case RBRACE:
			depth--
		case SEMI:
			if depth == 0 {
				return true
			}
		case NEWLINE, EOF:
			return false
		}
	}
	return false
}

func (p *parser) parseLambda() *LambdaExpr {
	lb := p.next() // {
	lam := &LambdaExpr{}
	p.skipNL()
	p.selfAsName()
	if p.at(ARROW) {
		p.next()
		lam.HasArrow = true
	} else if p.at(IDENT) {
		// Speculatively parse parameters followed by '->'.
		save := p.pos
		saveSink, saveLast := p.sink, p.lastErr
		scratch := &diag.Sink{}
		p.sink = scratch
		var params []*LambdaParam
		for {
			if !p.at(IDENT) {
				break
			}
			start := p.tok().Span
			lp := &LambdaParam{Name: p.parseIdent("")}
			if p.accept(COLON) {
				lp.Type = p.parseType()
			}
			lp.S = p.spanFrom(start)
			params = append(params, lp)
			if !p.accept(COMMA) {
				break
			}
			p.skipNL() // a line may end after a comma (spec §1.3)
		}
		p.skipNL()
		ok := p.at(ARROW) && !scratch.HasErrors()
		p.sink, p.lastErr = saveSink, saveLast
		if ok {
			p.next()
			lam.Params = params
			lam.HasArrow = true
		} else {
			p.pos = save
		}
	}
	stmts := p.parseStmtList()
	rb := p.expect(RBRACE, "to close the lambda")
	lam.Body = &Block{Stmts: stmts, Lbrace: lb.Span, Rbrace: rb.Span, S: lb.Span.Join(rb.Span)}
	lam.S = lam.Body.S
	return lam
}

func (p *parser) parseIf() Expr {
	start := p.next().Span // if
	e := &IfExpr{}
	if p.headerHasInit() {
		// Go's if v := f(); v > 0 { }: Lyth declares v on the line before.
		init := p.parseStmt()
		semi := p.expect(SEMI, "after the if statement's initializer")
		line := p.file.LineStart(start.StartPos().Line)
		indent := string(p.file.Content[line:start.Start])
		p.errorf(diag.EUnsupportedSyntax, start.Join(semi.Span), "if has no initializer statement in Lyth").
			WithEdit(start.Join(semi.Span), init.Span().Text()+"\n"+indent+"if", "declare it on the line before: %s, then if ...", init.Span().Text())
		defer func() { p.damaged = append(p.damaged, e.S) }()
	}
	e.Cond = p.parseCondition()
	e.Then = nested(p, func() *Block { return p.parseHeaderBody("if", e.Cond) })
	if p.peekPastNL().Kind == ELSE {
		p.skipNL()
		p.next()
		p.skipNL()
		if p.at(IF) {
			// else if: a block holding one if
			x := p.parseIf()
			e.Else = &Block{Stmts: []Stmt{&ExprStmt{X: x}}, S: x.Span()}
		} else {
			e.Else = nested(p, func() *Block { return p.parseHeaderBody("else", nil) })
		}
	}
	e.S = p.spanFrom(start)
	return e
}

func (p *parser) parseWhen() Expr {
	start := p.next().Span // when
	w := &WhenExpr{}
	switch {
	case p.at(LPAREN) && p.peek(1).Kind == VAL:
		// Kotlin's when (val x = e)
		open := p.next()
		p.next()
		w.Bind = p.parseIdent("after 'val'")
		p.expect(ASSIGN, "in when subject")
		w.Subject = p.parseExpr()
		p.skipNL()
		end := p.expect(RPAREN, "to close the when subject")
		p.errorf(diag.EUnsupportedSyntax, open.Span.Join(end.Span), "a when subject is bound with :=").
			WithFix(open.Span.Join(end.Span), w.Bind.Name+" := "+w.Subject.Span().Text(), "write when %s := %s { ... }", w.Bind.Name, w.Subject.Span().Text())
	case p.at(THIS) && p.peek(1).Kind == DEFINE:
		p.selfAsName()
		fallthrough
	case p.at(IDENT) && p.peek(1).Kind == DEFINE:
		w.Bind = p.parseIdent("")
		p.next() // :=
		p.noLambda++
		w.Subject = p.parseExpr()
		p.noLambda--
	case !p.at(LBRACE):
		p.noLambda++
		w.Subject = p.parseExpr()
		p.noLambda--
	}
	p.expect(LBRACE, "to start the when body")
	for {
		p.skipNLSemi()
		if p.at(RBRACE) || p.at(EOF) {
			break
		}
		bstart := p.tok().Span
		br := &WhenBranch{}
		if p.at(ELSE) {
			p.next()
			br.Else = true
		} else {
			for {
				br.Conds = append(br.Conds, p.parseWhenCond())
				if !p.accept(COMMA) {
					break
				}
				p.skipNL()
			}
		}
		p.expect(ARROW, "after when condition")
		p.skipNL()
		br.Body = nested(p, p.parseBody)
		br.S = p.spanFrom(bstart)
		w.Branches = append(w.Branches, br)
		if !p.at(NEWLINE) && !p.at(SEMI) && !p.at(RBRACE) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected newline after when branch, found %s", describe(p.tok()))
			p.syncStmt()
		}
	}
	end := p.expect(RBRACE, "to close the when body")
	w.S = start.Join(end.Span)
	return w
}

func (p *parser) parseSelect() Stmt {
	start := p.next().Span // select
	s := &SelectStmt{}
	p.expect(LBRACE, "to start the select body")
	for {
		p.skipNLSemi()
		if p.at(RBRACE) || p.at(EOF) {
			break
		}
		cstart := p.tok().Span
		sc := &SelectCase{}
		p.selfAsName()
		switch {
		case p.at(ELSE):
			p.next()
			sc.Else = true
		case p.at(VAL) || p.at(VAR):
			kw := p.next()
			if p.accept(LPAREN) {
				for {
					sc.Names = append(sc.Names, p.parseIdent("in destructuring declaration"))
					if !p.accept(COMMA) {
						break
					}
				}
				p.expect(RPAREN, "to close the destructuring declaration")
			} else {
				sc.Names = []*Ident{p.parseIdent("after 'val'")}
			}
			p.expect(ASSIGN, "in select branch")
			sc.Op = p.parseExpr()
			names := make([]string, len(sc.Names))
			for i, id := range sc.Names {
				names[i] = id.Name
			}
			p.errorf(diag.EUnsupportedSyntax, kw.Span, "a select branch binds a received value with :=").
				WithFix(kw.Span.Join(sc.Op.Span().At()), strings.Join(names, ", ")+" := ", "write %s := %s -> ...", strings.Join(names, ", "), sc.Op.Span().Text())
		case p.at(IDENT) && (p.peek(1).Kind == DEFINE || p.peek(1).Kind == COMMA):
			sc.Names = []*Ident{p.parseIdent("")}
			for p.accept(COMMA) {
				sc.Names = append(sc.Names, p.parseIdent("in a select binding"))
			}
			p.expect(DEFINE, "in select branch")
			sc.Op = p.parseExpr()
		default:
			sc.Op = p.parseExpr()
		}
		p.expect(ARROW, "after select operation")
		p.skipNL()
		sc.Body = nested(p, p.parseBody)
		sc.S = p.spanFrom(cstart)
		s.Cases = append(s.Cases, sc)
		if !p.at(NEWLINE) && !p.at(SEMI) && !p.at(RBRACE) {
			p.errorf(diag.EUnexpectedToken, p.tok().Span, "expected newline after select branch, found %s", describe(p.tok()))
			p.syncStmt()
		}
	}
	end := p.expect(RBRACE, "to close the select body")
	s.S = start.Join(end.Span)
	return s
}

func (p *parser) parseWhenCond() *WhenCond {
	start := p.tok().Span
	switch {
	case p.at(IS):
		p.next()
		t := p.parseType()
		return &WhenCond{Kind: WhenIs, Type: t, S: p.spanFrom(start)}
	case p.at(NOT) && p.peek(1).Kind == IS:
		p.next()
		p.next()
		t := p.parseType()
		return &WhenCond{Kind: WhenNotIs, Type: t, S: p.spanFrom(start)}
	case p.at(IN):
		p.next()
		x := p.parseExpr()
		return &WhenCond{Kind: WhenIn, X: x, S: p.spanFrom(start)}
	case p.at(NOT) && p.peek(1).Kind == IN:
		p.next()
		p.next()
		x := p.parseExpr()
		return &WhenCond{Kind: WhenNotIn, X: x, S: p.spanFrom(start)}
	}
	x := p.parseExpr()
	return &WhenCond{Kind: WhenExprCond, X: x, S: x.Span()}
}
