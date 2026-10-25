package lsp

import (
	"encoding/json"
	"fmt"
	gotypes "go/types"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// positionParams decodes a textDocument/position request.
func positionParams(params json.RawMessage) (string, Position) {
	var p textDocumentPositionParams
	_ = json.Unmarshal(params, &p)
	return canonical(uriToPath(p.TextDocument.URI)), p.Position
}

func documentParam(params json.RawMessage) string {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(params, &p)
	return canonical(uriToPath(p.TextDocument.URI))
}

// occurrenceAt analyzes path and returns the file and the occurrence at pos.
func (s *Server) occurrenceAt(path string, pos Position, wholeModule bool) (*analysis, *fileData, *occurrence, int) {
	a := s.analyze(path, wholeModule)
	fd := a.files[path]
	if fd == nil {
		return a, nil, nil, 0
	}
	off := fd.idx.offset(pos)
	return a, fd, a.at(path, off), off
}

// ---------------------------------------------------------------------------
// hover

func (s *Server) hover(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	a, fd, o, off := s.occurrenceAt(path, pos, false)
	if fd == nil {
		return nil, nil
	}
	if o == nil {
		if w, ws, we := wordAt(fd.src.Content, off); w != "" {
			if doc := keywordDocs[w]; doc != "" {
				return map[string]any{"contents": markup(doc), "range": fd.idx.rangeOf(ws, we)}, nil
			}
		}
		return nil, nil
	}
	md := a.describe(o)
	if md == "" {
		return nil, nil
	}
	return map[string]any{"contents": markup(md), "range": fd.idx.rangeOf(o.start, o.end)}, nil
}

func markup(md string) map[string]any { return map[string]any{"kind": "markdown", "value": md} }

func code(lang, text string) string { return "```" + lang + "\n" + text + "\n```" }

// describe renders the hover text for the declaration an occurrence names.
func (a *analysis) describe(o *occurrence) string {
	var b strings.Builder
	doc, goDecl := "", ""
	switch k := o.key.(type) {
	case *check.VarSym:
		t := k.Type
		if !o.def && o.file.info != nil {
			if nt := o.file.info.TypeOf(o.id); nt != nil {
				t = nt // the type here, after smart casts
			}
		}
		what := map[check.VarKind]string{check.LocalVar: "local variable", check.ParamVar: "parameter", check.ReceiverVar: "receiver",
			check.GlobalVar: "package variable", check.LoopVar: "loop variable", check.CatchVar: "error of catch"}[k.Kind]
		if k.Kind == check.GlobalVar && k.Decl != nil {
			kw := "var"
			if !k.Decl.Mutable {
				kw = "val"
			}
			b.WriteString(code("lyth", fmt.Sprintf("%s %s: %s", kw, k.Name(), typeString(t))))
			doc = k.Decl.Doc
			goDecl = a.goLine(declFile(a, k.Span), "var "+k.GoName+" ")
		} else {
			b.WriteString(code("lyth", fmt.Sprintf("%s: %s", k.Name(), typeString(t))))
		}
		if what != "" {
			b.WriteString("\n\n" + what)
			if t != k.Type && k.Type != nil {
				b.WriteString(fmt.Sprintf(" (declared %s)", typeString(k.Type)))
			}
		}
	case *check.ConstSym:
		decl := fmt.Sprintf("const %s: %s", k.Name(), typeString(k.Type))
		if k.Val != nil {
			decl += " = " + k.Val.ExactString()
		}
		b.WriteString(code("lyth", decl))
		if k.Decl != nil {
			doc = k.Decl.Doc
		}
	case *check.FuncSym:
		b.WriteString(code("lyth", a.funcHeader(k)))
		if k.Decl != nil {
			doc = k.Decl.Doc
			if k.Recv != nil {
				goDecl = a.goLine(declFile(a, k.Span), ") "+k.GoName+"(", ") "+k.GoName+"[")
			} else {
				goDecl = a.goLine(declFile(a, k.Span), "func "+k.GoName+"(", "func "+k.GoName+"[")
			}
		}
	case *types.TypeDecl:
		b.WriteString(code("lyth", typeDeclText(k)))
		if d := a.syntaxDecl(k.Span); d != nil {
			doc = declDoc(d)
		}
		goDecl = a.goLine(declFile(a, k.Span), "type "+k.GoName+" ", "type "+k.GoName+"[")
	case *types.Field:
		text := fmt.Sprintf("%s: %s", k.Name, typeString(k.Type))
		b.WriteString(code("lyth", text))
		if k.Tag != "" {
			b.WriteString("\n\nGo field `" + k.GoName + "` with tag `" + k.Tag + "`")
		}
	case *types.Method:
		b.WriteString(code("lyth", k.Name+signatureText(k.Sig, nil)))
	case *types.EnumEntry:
		name := k.Name
		if k.Decl != nil {
			name = k.Decl.Name + "." + k.Name
		}
		b.WriteString(code("lyth", fmt.Sprintf("%s // = %d", name, k.Value)))
		b.WriteString("\n\nGo constant `" + k.GoName + "`")
	case *check.TypeSym:
		if k.Alias != nil {
			b.WriteString(code("lyth", "type "+k.Name()+" = "+typeString(k.Type)))
			doc = k.Alias.Doc
		} else {
			b.WriteString(code("lyth", "type "+k.Name()))
		}
	case *check.PkgSym:
		if k.Pkg != nil {
			b.WriteString(code("lyth", fmt.Sprintf("import %q", k.Pkg.Path)))
			b.WriteString("\n\npackage `" + k.Pkg.Name + "`")
			if !k.Pkg.Lyth {
				b.WriteString(" (Go)")
			}
		}
	case gotypes.Object:
		b.WriteString(code("go", gotypes.ObjectString(k, func(p *gotypes.Package) string { return p.Name() })))
		if file, line, _, ok := a.goPosition(k); ok {
			doc = goDoc(file, line)
		}
	default:
		if sym, ok := o.key.(check.Symbol); ok {
			b.WriteString(code("lyth", sym.Name()))
		}
	}
	if doc = strings.TrimSpace(doc); doc != "" {
		b.WriteString("\n\n" + docText(doc))
	}
	if goDecl != "" {
		b.WriteString("\n\n---\nCompiles to:\n" + code("go", goDecl))
	}
	return b.String()
}

// docText turns // comment lines into Markdown.
func docText(doc string) string {
	var lines []string
	for _, l := range strings.Split(doc, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(strings.TrimPrefix(l, "//"), " ")
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

func typeString(t types.Type) string {
	if t == nil {
		return "?"
	}
	return t.String()
}

// funcHeader is a function's declaration as written, up to its body.
func (a *analysis) funcHeader(f *check.FuncSym) string {
	d := f.Decl
	if d == nil || !d.S.IsValid() {
		return "func " + f.Name() + signatureText(f.Sig, nil)
	}
	end := d.S.End
	switch {
	case d.Body != nil:
		end = d.Body.S.Start
	case d.ExprBody != nil:
		end = d.ExprBody.Span().Start
	}
	text := string(d.S.File.Content[d.S.Start:end])
	text = strings.TrimSpace(text)
	if d.ExprBody != nil {
		text = strings.TrimSpace(strings.TrimSuffix(text, "="))
		if d.Result == nil && f.Sig != nil && len(f.Sig.Results) > 0 {
			text += ": " + resultsText(f.Sig)
		}
	}
	return collapseSpace(text)
}

var spaces = regexp.MustCompile(`\s*\n\s*`)

func collapseSpace(s string) string { return spaces.ReplaceAllString(s, " ") }

// signatureText renders (a: A, b: B): R throws.
func signatureText(sig *types.Signature, names []string) string {
	if sig == nil {
		return "()"
	}
	var b strings.Builder
	b.WriteString("(")
	for i, p := range sig.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		name := ""
		if i < len(names) {
			name = names[i]
		} else if i < len(sig.ParamNames) {
			name = sig.ParamNames[i]
		}
		if name != "" && name != "_" {
			b.WriteString(name + ": ")
		}
		if sig.Variadic && i == len(sig.Params)-1 {
			if s, ok := p.(*types.Slice); ok {
				b.WriteString("..." + typeString(s.Elem))
				continue
			}
		}
		b.WriteString(typeString(p))
	}
	b.WriteString(")")
	if len(sig.Results) > 0 {
		b.WriteString(": " + resultsText(sig))
	}
	if sig.Throws {
		b.WriteString(" throws")
	}
	return b.String()
}

func resultsText(sig *types.Signature) string {
	if len(sig.Results) == 1 {
		return typeString(sig.Results[0])
	}
	parts := make([]string, len(sig.Results))
	for i, r := range sig.Results {
		parts[i] = typeString(r)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func typeDeclText(d *types.TypeDecl) string {
	var b strings.Builder
	switch d.Kind {
	case types.StructDecl:
		b.WriteString("struct " + d.Name + typeParamsText(d.TypeParams))
		if len(d.Fields) == 0 {
			return b.String()
		}
		b.WriteString(" {\n")
		for _, f := range d.Fields {
			b.WriteString(fmt.Sprintf("    %s: %s\n", f.Name, typeString(f.Type)))
		}
		b.WriteString("}")
	case types.InterfaceDecl:
		if d.Sealed {
			b.WriteString("sealed ")
		}
		b.WriteString("interface " + d.Name + typeParamsText(d.TypeParams))
		if len(d.Methods) == 0 && len(d.Embeds) == 0 {
			return b.String()
		}
		b.WriteString(" {\n")
		for _, e := range d.Embeds {
			b.WriteString("    " + typeString(e) + "\n")
		}
		for _, m := range d.Methods {
			b.WriteString("    " + m.Name + signatureText(m.Sig, nil) + "\n")
		}
		b.WriteString("}")
	case types.EnumDecl:
		names := make([]string, len(d.Entries))
		for i, e := range d.Entries {
			names[i] = e.Name
		}
		b.WriteString("enum " + d.Name + " { " + strings.Join(names, ", ") + " }")
	}
	return b.String()
}

func typeParamsText(tps []*types.TypeParam) string {
	if len(tps) == 0 {
		return ""
	}
	parts := make([]string, len(tps))
	for i, tp := range tps {
		parts[i] = tp.Name
		if tp.Constraint != nil {
			parts[i] += ": " + typeString(tp.Constraint)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// declFile returns the file of the analysis a span is in.
func declFile(a *analysis, sp source.Span) *fileData {
	if !sp.IsValid() {
		return nil
	}
	return a.files[sp.File.Path]
}

// goLine finds the generated Go line that declares something, by the
// text it starts with.
func (a *analysis) goLine(fd *fileData, needles ...string) string {
	if fd == nil || fd.goCode == nil {
		return ""
	}
	for _, line := range strings.Split(string(fd.goCode), "\n") {
		for _, n := range needles {
			if strings.Contains(line, n) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "{"))
			}
		}
	}
	return ""
}

// syntaxDecl returns the declaration whose name is at sp.
func (a *analysis) syntaxDecl(sp source.Span) syntax.Decl {
	fd := declFile(a, sp)
	if fd == nil {
		return nil
	}
	for _, d := range fd.syn.Decls {
		if n := declName(d); n != nil && n.S.Start == sp.Start {
			return d
		}
	}
	return nil
}

func declName(d syntax.Decl) *syntax.Ident {
	switch d := d.(type) {
	case *syntax.FunDecl:
		return d.Name
	case *syntax.StructDecl:
		return d.Name
	case *syntax.InterfaceDecl:
		return d.Name
	case *syntax.EnumDecl:
		return d.Name
	case *syntax.PropDecl:
		return d.Name
	case *syntax.ConstDecl:
		return d.Name
	case *syntax.TypeAliasDecl:
		return d.Name
	}
	return nil
}

func declDoc(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.FunDecl:
		return d.Doc
	case *syntax.StructDecl:
		return d.Doc
	case *syntax.InterfaceDecl:
		return d.Doc
	case *syntax.EnumDecl:
		return d.Doc
	case *syntax.PropDecl:
		return d.Doc
	case *syntax.ConstDecl:
		return d.Doc
	case *syntax.TypeAliasDecl:
		return d.Doc
	}
	return ""
}

var keywordDocs = map[string]string{
	"throws": "`func f(): T throws` can fail: in Go it returns `(T, error)`. Call it with `f()?` to pass the error on, or handle it with `catch`.",
	"throw":  "`throw err` returns `err` from a `throws` function (with zero values for its other results).",
	"catch":  "`call() catch { fallback }` handles a failed call; `catch (err) { ... }` binds the error; `catch (e: *NotFound) { ... }?` handles one error type through wrapping.",
	"when":   "`when x { 1, 2 -> ...; is T -> ...; in 1..9 -> ...; else -> ... }` chooses a branch by value, type, or range. Over an enum or sealed interface it must cover every case or have `else`.",
	"is":     "`x is T` tests a value's type (a Go type assertion); after the test, `x` is a `T` (smart cast).",
	"as":     "`x as T` converts or asserts to `T` (panics when it fails); `x as? T` gives `null` instead.",
	"in":     "`x in xs` tests membership: ranges, slices, arrays, map keys, and substrings. `for x in xs` iterates.",
	"null":   "`null` is the absent value of a nullable type `T?` (Go's nil). Use `?.`, `?:`, `!!`, or a check before using a nullable value.",
	"self":   "`self` is a method's receiver (Go's receiver variable). It cannot be declared or assigned.",
	"type":   "`type Name Underlying` declares a new type (a Go defined type); `type Name = Type` gives a type another name (a Go alias).",
	"sealed": "`sealed interface` is implemented only by structs of its own package that declare it (`struct T : I`); a `when` over it can be exhaustive.",
	"enum":   "`enum Color { Red, Green }` declares named constants (Go: `ColorRed` iota constants with a `String` method; JSON carries the name `\"red\"`). `enum Plan: string { Free = \"free\" }` gives entries string values (Go: `type Plan string`).",
	"func":   "`func name(a: A): R { ... }` declares a function; `func T.m()` a method with value receiver, `func *T.m()` with pointer receiver.",
	"struct": "`struct T { Field: Type }` declares a struct; build one by calling the type: `T(Field = x)`.",
	"defer":  "`defer f()` or `defer { ... }` runs when the function returns.",
	"go":     "`go { ... }` runs the block in a new goroutine.",
}

// wordAt returns the identifier around byte offset off.
func wordAt(text []byte, off int) (string, int, int) {
	off = max(0, min(off, len(text)))
	s, e := off, off
	for s > 0 {
		r, size := utf8.DecodeLastRune(text[:s])
		if !isIdentRune(r) {
			break
		}
		s -= size
	}
	for e < len(text) {
		r, size := utf8.DecodeRune(text[e:])
		if !isIdentRune(r) {
			break
		}
		e += size
	}
	return string(text[s:e]), s, e
}

func isIdentRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// ---------------------------------------------------------------------------
// definition

func (s *Server) definition(method string, params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	a, fd, o, _ := s.occurrenceAt(path, pos, false)
	if fd == nil || o == nil {
		return nil, nil
	}
	k := o.key
	if method == "textDocument/typeDefinition" {
		k = typeKey(a, o)
		if k == nil {
			return nil, nil
		}
	}
	return a.locations(s, k), nil
}

// typeKey returns the declaration of the type of the thing o names.
func typeKey(a *analysis, o *occurrence) any {
	var t types.Type
	switch k := o.key.(type) {
	case *check.VarSym:
		t = k.Type
	case *types.Field:
		t = k.Type
	case *check.ConstSym:
		t = k.Type
	case *types.TypeDecl:
		return k
	}
	if o.file.info != nil {
		if nt := o.file.info.TypeOf(o.id); nt != nil {
			t = nt
		}
	}
	for t != nil {
		switch tt := types.Base(t).(type) {
		case *types.Pointer:
			t = tt.Elem
			continue
		case *types.Slice:
			t = tt.Elem
			continue
		case *types.Named:
			return key(tt)
		}
		return nil
	}
	return nil
}

// locations returns where k is declared: Lyth declarations, or Go
// declarations in the standard library and the module cache.
func (a *analysis) locations(s *Server, k any) []Location {
	if obj, ok := k.(gotypes.Object); ok {
		file, line, col, ok := a.goPosition(obj)
		if !ok {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil
		}
		idx := newLineIndex(data)
		if line-1 >= len(idx.lines) {
			return nil
		}
		start := idx.lines[line-1] + col - 1
		end := start + len(obj.Name())
		return []Location{{URI: s.uri(file), Range: idx.rangeOf(start, end)}}
	}
	var out []Location
	for _, d := range a.defs[k] {
		out = append(out, Location{URI: s.uri(d.file.path), Range: d.file.idx.rangeOf(d.start, d.end)})
	}
	if len(out) == 0 {
		if sp := declSpan(k); sp.IsValid() {
			out = append(out, Location{URI: s.uri(sp.File.Path), Range: spanRange(sp)})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// references, highlights, rename

func (s *Server) references(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	var p struct {
		Context struct {
			IncludeDeclaration bool `json:"includeDeclaration"`
		} `json:"context"`
	}
	_ = json.Unmarshal(params, &p)
	a, fd, o, _ := s.occurrenceAt(path, pos, true)
	if fd == nil || o == nil {
		return []Location{}, nil
	}
	out := []Location{}
	for _, r := range a.refs(o.key) {
		if r.def && !p.Context.IncludeDeclaration {
			continue
		}
		out = append(out, Location{URI: s.uri(r.file.path), Range: r.file.idx.rangeOf(r.start, r.end)})
	}
	return out, nil
}

func (s *Server) highlight(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	a, fd, o, _ := s.occurrenceAt(path, pos, false)
	if fd == nil || o == nil {
		return []any{}, nil
	}
	out := []any{}
	for _, r := range a.refs(o.key) {
		if r.file != fd {
			continue
		}
		kind := 2 // read
		if r.def {
			kind = 3 // write
		}
		out = append(out, map[string]any{"range": fd.idx.rangeOf(r.start, r.end), "kind": kind})
	}
	return out, nil
}

// renamable reports why the declaration k cannot be renamed, or "".
func (a *analysis) renamable(k any) string {
	switch v := k.(type) {
	case gotypes.Object:
		return fmt.Sprintf("%s is declared in Go", v.Name())
	case *check.VarSym:
		if v.Implicit {
			return v.Name() + " is implicit"
		}
	case *check.TypeSym:
		if v.Pkg == nil {
			return v.Name() + " is a built-in type"
		}
	case *check.PkgSym:
		return "rename an import with import name \"path\""
	}
	defs := a.defs[k]
	if len(defs) == 0 && !declSpan(k).IsValid() {
		return "this name is not declared in Lyth source"
	}
	for _, d := range defs {
		if d.file.pkg != nil && d.file.pkg.Dir.Std {
			return "it is declared in the standard library"
		}
	}
	if sp := declSpan(k); sp.IsValid() && a.files[sp.File.Path] == nil {
		return "it is declared outside this module"
	}
	return ""
}

func (s *Server) prepareRename(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	a, fd, o, _ := s.occurrenceAt(path, pos, true)
	if fd == nil || o == nil {
		return nil, &rpcError{Code: codeRequestFailed, Message: "no name to rename here"}
	}
	if why := a.renamable(o.key); why != "" {
		return nil, &rpcError{Code: codeRequestFailed, Message: "cannot rename: " + why}
	}
	return map[string]any{"range": fd.idx.rangeOf(o.start, o.end), "placeholder": string(fd.src.Content[o.start:o.end])}, nil
}

var identRE = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*$`)

func (s *Server) rename(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	var p struct {
		NewName string `json:"newName"`
	}
	_ = json.Unmarshal(params, &p)
	if !identRE.MatchString(p.NewName) || syntax.IsKeywordName(p.NewName) {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("%q is not a valid Lyth name", p.NewName)}
	}
	a, fd, o, _ := s.occurrenceAt(path, pos, true)
	if fd == nil || o == nil {
		return nil, &rpcError{Code: codeRequestFailed, Message: "no name to rename here"}
	}
	if why := a.renamable(o.key); why != "" {
		return nil, &rpcError{Code: codeRequestFailed, Message: "cannot rename: " + why}
	}
	changes := map[string][]TextEdit{}
	for _, r := range a.refs(o.key) {
		uri := s.uri(r.file.path)
		changes[uri] = append(changes[uri], TextEdit{Range: r.file.idx.rangeOf(r.start, r.end), NewText: p.NewName})
	}
	return map[string]any{"changes": changes}, nil
}

// ---------------------------------------------------------------------------
// symbols

type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

// LSP SymbolKind values.
const (
	symMethod    = 6
	symField     = 8
	symEnum      = 10
	symInterface = 11
	symFunction  = 12
	symVariable  = 13
	symConstant  = 14
	symEnumEntry = 22
	symStruct    = 23
	symTypeParam = 26
)

func (s *Server) documentSymbols(params json.RawMessage) (any, error) {
	path := documentParam(params)
	text, ok := s.doc(path)
	if !ok {
		return []documentSymbol{}, nil
	}
	f := syntax.Parse(source.NewFile(path, path, []byte(text)), &diag.Sink{})
	return fileSymbols(f, newLineIndex(f.Src.Content)), nil
}

// fileSymbols is a file's outline: declarations, with fields, interface
// methods, enum entries, and the methods declared in the file under their
// type.
func fileSymbols(f *syntax.File, idx *lineIndex) []documentSymbol {
	out := []documentSymbol{}
	byType := map[string]int{}
	sym := func(name, detail string, kind int, whole source.Span, id *syntax.Ident) documentSymbol {
		return documentSymbol{Name: name, Detail: detail, Kind: kind, Range: idx.rangeOf(whole.Start, whole.End), SelectionRange: idx.rangeOf(id.S.Start, id.S.End)}
	}
	firstLine := func(sp source.Span) string {
		text := sp.Text()
		if i := strings.IndexAny(text, "{\n"); i >= 0 {
			text = text[:i]
		}
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "="))
	}
	var methods []*syntax.FunDecl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.FunDecl:
			if d.Name == nil {
				continue
			}
			if d.Recv != nil && d.Recv.Name != nil {
				methods = append(methods, d)
				continue
			}
			out = append(out, sym(d.Name.Name, firstLine(d.S), symFunction, d.S, d.Name))
		case *syntax.StructDecl:
			ds := sym(d.Name.Name, "struct", symStruct, d.S, d.Name)
			for _, fl := range d.Fields {
				ds.Children = append(ds.Children, sym(fl.Name.Name, fl.Type.Span().Text(), symField, fl.S, fl.Name))
			}
			byType[d.Name.Name] = len(out)
			out = append(out, ds)
		case *syntax.InterfaceDecl:
			ds := sym(d.Name.Name, "interface", symInterface, d.S, d.Name)
			for _, m := range d.Methods {
				ds.Children = append(ds.Children, sym(m.Name.Name, firstLine(m.S), symMethod, m.S, m.Name))
			}
			byType[d.Name.Name] = len(out)
			out = append(out, ds)
		case *syntax.EnumDecl:
			ds := sym(d.Name.Name, "enum", symEnum, d.S, d.Name)
			for _, e := range d.Entries {
				ds.Children = append(ds.Children, sym(e.Name, "", symEnumEntry, e.S, e))
			}
			byType[d.Name.Name] = len(out)
			out = append(out, ds)
		case *syntax.PropDecl:
			kind := symVariable
			if !d.Mutable {
				kind = symConstant
			}
			out = append(out, sym(d.Name.Name, firstLine(d.S), kind, d.S, d.Name))
		case *syntax.ConstDecl:
			out = append(out, sym(d.Name.Name, firstLine(d.S), symConstant, d.S, d.Name))
		case *syntax.TypeAliasDecl:
			out = append(out, sym(d.Name.Name, firstLine(d.S), symTypeParam, d.S, d.Name))
		}
	}
	for _, m := range methods {
		ms := sym(m.Name.Name, firstLine(m.S), symMethod, m.S, m.Name)
		if i, ok := byType[m.Recv.Name.Name]; ok {
			out[i].Children = append(out[i].Children, ms)
		} else {
			ms.Name = m.Recv.Name.Name + "." + m.Name.Name
			out = append(out, ms)
		}
	}
	return out
}

func (s *Server) workspaceSymbols(params json.RawMessage) (any, error) {
	var p struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(params, &p)
	s.mu.Lock()
	first := ""
	for path := range s.docs {
		if first == "" || path < first {
			first = path
		}
	}
	s.mu.Unlock()
	out := []any{}
	if first == "" {
		return out, nil
	}
	a := s.analyze(first, true)
	query := strings.ToLower(p.Query)
	var paths []string
	for path := range a.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fd := a.files[path]
		if fd.pkg != nil && fd.pkg.Dir.Std {
			continue
		}
		var walk func(list []documentSymbol, container string)
		walk = func(list []documentSymbol, container string) {
			for _, ds := range list {
				if fuzzy(strings.ToLower(ds.Name), query) {
					info := map[string]any{"name": ds.Name, "kind": ds.Kind, "location": Location{URI: s.uri(path), Range: ds.SelectionRange}}
					if container != "" {
						info["containerName"] = container
					}
					out = append(out, info)
				}
				walk(ds.Children, ds.Name)
			}
		}
		walk(fileSymbols(fd.syn, fd.idx), "")
	}
	return out, nil
}

// fuzzy reports whether the letters of query appear in name in order.
func fuzzy(name, query string) bool {
	for _, r := range query {
		i := strings.IndexRune(name, r)
		if i < 0 {
			return false
		}
		name = name[i+utf8.RuneLen(r):]
	}
	return true
}

// ---------------------------------------------------------------------------
// code actions and commands

func (s *Server) codeActions(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Range   Range `json:"range"`
		Context struct {
			Diagnostics []Diagnostic `json:"diagnostics"`
		} `json:"context"`
	}
	_ = json.Unmarshal(params, &p)
	path := canonical(uriToPath(p.TextDocument.URI))
	diags := p.Context.Diagnostics
	// Clients may drop data: take the fixes from the published diagnostics.
	s.mu.Lock()
	published := append(append([]Diagnostic{}, s.lythDiags[path]...), s.vetDiags[path]...)
	s.mu.Unlock()
	for i := range diags {
		if diags[i].Data == nil {
			for _, pd := range published {
				if pd.Range == diags[i].Range && pd.Message == diags[i].Message {
					diags[i].Data = pd.Data
				}
			}
		}
	}
	if len(diags) == 0 {
		for _, pd := range published {
			if pd.Range.Start.Line <= p.Range.End.Line && pd.Range.End.Line >= p.Range.Start.Line {
				diags = append(diags, pd)
			}
		}
	}
	out := []any{}
	for _, d := range diags {
		if d.Data == nil {
			continue
		}
		for i, f := range d.Data.Fixes {
			plain := d
			plain.Data = nil
			out = append(out, map[string]any{
				"title": f.Title, "kind": "quickfix", "diagnostics": []Diagnostic{plain}, "isPreferred": i == 0,
				"edit": map[string]any{"changes": map[string][]TextEdit{p.TextDocument.URI: f.Edits}},
			})
		}
	}
	return out, nil
}

func (s *Server) executeCommand(params json.RawMessage) (any, error) {
	var p struct {
		Command   string            `json:"command"`
		Arguments []json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Command != "lyth.showGo" || len(p.Arguments) == 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown command " + p.Command}
	}
	var uri string
	_ = json.Unmarshal(p.Arguments[0], &uri)
	path := canonical(uriToPath(uri))
	a := s.analyze(path, false)
	fd := a.files[path]
	if fd == nil || fd.goCode == nil {
		return "", nil
	}
	return string(fd.goCode), nil
}

// ---------------------------------------------------------------------------
// inlay hints

func (s *Server) inlayHints(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Range Range `json:"range"`
	}
	_ = json.Unmarshal(params, &p)
	path := canonical(uriToPath(p.TextDocument.URI))
	a := s.analyze(path, false)
	fd := a.files[path]
	out := []any{}
	if fd == nil || fd.info == nil {
		return out, nil
	}
	lo, hi := fd.idx.offset(p.Range.Start), fd.idx.offset(p.Range.End)
	if hi <= lo {
		hi = len(fd.src.Content)
	}
	add := func(off int, label string) {
		if off < lo || off > hi {
			return
		}
		out = append(out, map[string]any{"position": fd.idx.position(off), "label": label, "kind": 1, "paddingLeft": false})
	}
	inFile := func(sp source.Span) bool { return sp.IsValid() && sp.File == fd.src }
	info := fd.info
	// x := f(): the inferred type.
	for ld, vars := range info.Locals {
		if !inFile(ld.S) || ld.Type != nil || ld.Value == nil || obviousType(info, ld.Value) {
			continue
		}
		for i, id := range ld.Names {
			if i < len(vars) && vars[i] != nil && id.Name != "_" {
				add(id.S.End, ": "+typeString(vars[i].Type))
			}
		}
	}
	// for k, v in m: the loop variables' types.
	for st, fi := range info.Fors {
		if !inFile(st.S) {
			continue
		}
		for i, id := range st.Names {
			if i < len(fi.Vars) && fi.Vars[i] != nil && id.Name != "_" {
				add(id.S.End, ": "+typeString(fi.Vars[i].Type))
			}
		}
	}
	// { a, b -> ... }: lambda parameters without a written type.
	for le, li := range info.Lambdas {
		if !inFile(le.S) {
			continue
		}
		for i, lp := range le.Params {
			if lp.Type == nil && i < len(li.Params) && li.Params[i] != nil && lp.Name.Name != "_" {
				add(lp.Name.S.End, ": "+typeString(li.Params[i].Type))
			}
		}
	}
	// func sq(n: int) = n * n: the inferred result type.
	for fdecl, fi := range info.Funcs {
		if !inFile(fdecl.S) || fdecl.Result != nil || fdecl.ExprBody == nil || fi.Sym == nil || fi.Sym.Sig == nil || len(fi.Sym.Sig.Results) == 0 {
			continue
		}
		text := fd.src.Content
		i := fdecl.ExprBody.Span().Start
		for i > fdecl.S.Start && text[i-1] != '=' {
			i--
		}
		i-- // the =
		for i > fdecl.S.Start && (text[i-1] == ' ' || text[i-1] == '\t') {
			i--
		}
		add(i, ": "+resultsText(fi.Sym.Sig))
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := out[i].(map[string]any)["position"].(Position), out[j].(map[string]any)["position"].(Position)
		return pi.Line < pj.Line || pi.Line == pj.Line && pi.Character < pj.Character
	})
	return out, nil
}

// obviousType reports whether a value spells out its type: a construction,
// a conversion, a typed literal, or a basic literal.
func obviousType(info *check.Info, e syntax.Expr) bool {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.CallExpr:
		if ci := info.Calls[x]; ci != nil {
			return ci.Kind == check.CallConstruct || ci.Kind == check.CallConvert
		}
	case *syntax.ListLit:
		return x.Type != nil
	case *syntax.MapLit:
		return x.Type != nil
	case *syntax.BasicLit, *syntax.TemplateLit, *syntax.BoolLit:
		return true
	case *syntax.UnaryExpr:
		return obviousType(info, x.X)
	}
	return false
}
