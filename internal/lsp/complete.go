package lsp

import (
	"encoding/json"
	gotypes "go/types"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// placeholder stands in for the word being completed, so that the code
// around the cursor parses and the checker resolves what precedes it.
const placeholder = "lythCompletionHere__"

type completionItem struct {
	Label            string    `json:"label"`
	Kind             int       `json:"kind"`
	Detail           string    `json:"detail,omitempty"`
	Documentation    any       `json:"documentation,omitempty"`
	SortText         string    `json:"sortText,omitempty"`
	FilterText       string    `json:"filterText,omitempty"`
	InsertText       string    `json:"insertText,omitempty"`
	InsertTextFormat int       `json:"insertTextFormat,omitempty"`
	TextEdit         *TextEdit `json:"textEdit,omitempty"`
}

// LSP CompletionItemKind values.
const (
	itemMethod    = 2
	itemFunction  = 3
	itemField     = 5
	itemVariable  = 6
	itemClass     = 7
	itemInterface = 8
	itemModule    = 9
	itemEnum      = 13
	itemKeyword   = 14
	itemSnippet   = 15
	itemConstant  = 21
	itemStruct    = 22
	itemEnumEntry = 20
	itemTypeParam = 25
)

type completions struct {
	items []completionItem
	seen  map[string]bool
}

func (c *completions) add(it completionItem, rank string) {
	if c.seen[it.Label] {
		return
	}
	c.seen[it.Label] = true
	it.SortText = rank + it.Label
	c.items = append(c.items, it)
}

func (s *Server) completion(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	text, ok := s.doc(path)
	c := &completions{seen: map[string]bool{}}
	result := map[string]any{"isIncomplete": false, "items": &c.items}
	if !ok {
		return result, nil
	}
	data := []byte(text)
	idx := newLineIndex(data)
	off := idx.offset(pos)
	_, ws, we := wordAt(data, off)
	lineStart := idx.lines[idx.position(off).Line]
	before := text[lineStart:off]
	if q := importQuote(before); q >= 0 {
		s.importCompletions(c, path, idx, lineStart+q+1, off)
		return result, nil
	}
	if inCommentOrString(text[lineStart:ws]) {
		return result, nil
	}
	a := s.analyzeWith(path, text[:ws]+placeholder+text[we:])
	fd := a.files[path]
	if fd == nil {
		return result, nil
	}
	var sel *syntax.SelectorExpr
	var ident *syntax.Ident
	for _, d := range fd.syn.Decls {
		syntax.Walk(d, func(n syntax.Node) {
			switch x := n.(type) {
			case *syntax.SelectorExpr:
				if x.Sel != nil && x.Sel.Name == placeholder {
					sel = x
				}
			case *syntax.Ident:
				if x.Name == placeholder {
					ident = x
				}
			}
		})
	}
	if sel != nil && fd.info != nil {
		a.memberCompletions(c, fd, sel)
		return result, nil
	}
	if strings.HasSuffix(strings.TrimRight(text[:ws], " \t"), ".") {
		return result, nil // a member of something the checker could not resolve
	}
	at := ws
	if ident != nil {
		at = ident.S.Start
	}
	a.scopeCompletions(c, fd, at)
	return result, nil
}

// importQuote returns the offset of the opening quote when the line so far
// is an import path being written, or -1.
func importQuote(line string) int {
	t := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(t, "import") {
		return -1
	}
	q := strings.IndexByte(line, '"')
	if q < 0 || strings.Count(line, "\"") != 1 {
		return -1
	}
	return q
}

// inCommentOrString reports whether the end of a line prefix is inside a
// // comment or a string literal.
func inCommentOrString(line string) bool {
	inStr, raw := false, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case inStr && c == '\\' && !raw:
			i++
		case c == '"' || c == '`':
			if !inStr {
				inStr, raw = true, c == '`'
			} else if (c == '`') == raw {
				inStr = false
			}
		case !inStr && c == '/' && i+1 < len(line) && line[i+1] == '/':
			return true
		}
	}
	return inStr
}

// memberCompletions lists what can follow x.: a package's exported names, a
// type's fields and methods, extension functions, and enum entries.
func (a *analysis) memberCompletions(c *completions, fd *fileData, sel *syntax.SelectorExpr) {
	info := fd.info
	if id, ok := syntax.Unparen(sel.X).(*syntax.Ident); ok {
		if ps, ok := info.Uses[id].(*check.PkgSym); ok && ps.Pkg != nil {
			packageMembers(c, ps.Pkg)
			return
		}
	}
	tv, ok := info.Types[sel.X]
	if !ok || tv.Type == nil {
		return
	}
	if tv.Mode == check.ModeType {
		if d := types.EnumOf(tv.Type); d != nil {
			for _, e := range d.Entries {
				c.add(completionItem{Label: e.Name, Kind: itemEnumEntry, Detail: d.Name}, "0")
			}
		}
		return
	}
	var own *check.Package
	if fd.pkg != nil {
		own = fd.pkg.Checked
	}
	typeMembers(c, tv.Type, own)
	extensionMembers(c, tv.Type, own)
	preludeMembers(c, tv.Type)
}

// typeMembers lists the fields and methods of a value of type t.
func typeMembers(c *completions, t types.Type, own *check.Package) {
	t = types.Base(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Base(p.Elem)
	}
	if n, ok := t.(*types.Named); ok && n.Decl != nil {
		var walk func(d *types.TypeDecl)
		walk = func(d *types.TypeDecl) {
			for _, f := range d.Fields {
				c.add(completionItem{Label: f.Name, Kind: itemField, Detail: typeString(f.Type)}, "0")
			}
			for _, m := range d.Methods {
				c.add(callItem(m.Name, itemMethod, m.Sig, nil), "0")
			}
			for _, e := range d.Embeds {
				if en, ok := types.Base(e).(*types.Named); ok && en.Decl != nil {
					walk(en.Decl)
				}
			}
		}
		walk(n.Decl)
		return
	}
	g := types.Lower(t)
	if g == nil {
		return
	}
	visible := func(o gotypes.Object) bool {
		return o.Exported() || own != nil && o.Pkg() != nil && o.Pkg().Path() == own.Path
	}
	mset := gotypes.NewMethodSet(g)
	if _, isPtr := g.(*gotypes.Pointer); !isPtr && !gotypes.IsInterface(g) {
		mset = gotypes.NewMethodSet(gotypes.NewPointer(g))
	}
	for i := range mset.Len() {
		fn, ok := mset.At(i).Obj().(*gotypes.Func)
		if !ok || !visible(fn) {
			continue
		}
		sig := types.LiftSignature(fn.Type().(*gotypes.Signature))
		c.add(callItem(fn.Name(), itemMethod, sig, nil), "0")
	}
	var fields func(st *gotypes.Struct, depth int)
	fields = func(st *gotypes.Struct, depth int) {
		for i := range st.NumFields() {
			f := st.Field(i)
			if visible(f) {
				c.add(completionItem{Label: f.Name(), Kind: itemField, Detail: typeString(types.Lift(f.Type()))}, "0")
			}
			if f.Embedded() && depth < 3 {
				ft := f.Type()
				if p, ok := ft.(*gotypes.Pointer); ok {
					ft = p.Elem()
				}
				if est, ok := ft.Underlying().(*gotypes.Struct); ok {
					fields(est, depth+1)
				}
			}
		}
	}
	under := g
	if p, ok := under.(*gotypes.Pointer); ok {
		under = p.Elem()
	}
	if st, ok := under.Underlying().(*gotypes.Struct); ok {
		fields(st, 0)
	}
}

// extensionMembers lists the extension functions that apply to t, from the
// package and the Lyth packages it imports (lyth/coll's Filter, Map, ...).
func extensionMembers(c *completions, t types.Type, own *check.Package) {
	if own == nil {
		return
	}
	pkgs := []*check.Package{own}
	for _, p := range own.Imports {
		if p.Lyth {
			pkgs = append(pkgs, p)
		}
	}
	for _, p := range pkgs {
		for name, f := range p.Extensions {
			if f.ExtRecv == nil || !extApplies(f.ExtRecv, t) || p != own && !f.Pub {
				continue
			}
			var names []string
			if f.Decl != nil {
				for _, prm := range f.Decl.Params {
					if prm.Name != nil {
						names = append(names, prm.Name.Name)
					}
				}
			}
			it := callItem(name, itemMethod, f.Sig, names)
			if p != own {
				it.Detail += " (" + p.Path + ")"
			}
			c.add(it, "1")
		}
	}
}

// extApplies approximates whether an extension declared for recv applies to
// a value of type t.
func extApplies(recv, t types.Type) bool {
	t = types.Base(t)
	switch r := types.Base(recv).(type) {
	case *types.Slice:
		_, ok := t.(*types.Slice)
		return ok
	case *types.Map:
		_, ok := t.(*types.Map)
		return ok
	case *types.Chan:
		_, ok := t.(*types.Chan)
		return ok
	case *types.Basic:
		b, ok := types.DefaultType(t).(*types.Basic)
		return ok && b.Name == r.Name
	case *types.Named:
		if p, ok := t.(*types.Pointer); ok {
			t = types.Base(p.Elem)
		}
		n, ok := t.(*types.Named)
		return ok && key(n) == key(r)
	case *types.TypeParam:
		return true
	}
	return false
}

// preludeMembers lists the methods Lyth gives built-in types.
func preludeMembers(c *completions, t types.Type) {
	t = types.DefaultType(types.Base(t))
	switch {
	case types.IsString(t):
		for _, m := range []string{"toInt", "toInt64", "toFloat64", "toBool"} {
			c.add(completionItem{Label: m, Kind: itemMethod, Detail: "(): " + strings.TrimPrefix(strings.ToLower(m), "to") + " throws", InsertText: m + "()"}, "2")
		}
	case types.IsErrorType(t):
		c.add(completionItem{Label: "wrap", Kind: itemMethod, Detail: "(context: string): error", InsertText: "wrap(\"$1\")", InsertTextFormat: 2}, "2")
	}
	if ch, ok := t.(*types.Chan); ok {
		elem := typeString(ch.Elem)
		c.add(completionItem{Label: "send", Kind: itemMethod, Detail: "(v: " + elem + ")", InsertText: "send($1)", InsertTextFormat: 2}, "2")
		c.add(completionItem{Label: "receive", Kind: itemMethod, Detail: "(): " + elem, InsertText: "receive()"}, "2")
		c.add(completionItem{Label: "close", Kind: itemMethod, Detail: "()", InsertText: "close()"}, "2")
	}
}

// packageMembers lists a package's exported declarations.
func packageMembers(c *completions, p *check.Package) {
	if p.Lyth && p.Scope != nil {
		for _, sym := range p.Scope.Symbols() {
			if it, ok := symbolItem(sym); ok && exported(sym.Name()) {
				c.add(it, "0")
			}
		}
		return
	}
	if p.Go == nil {
		return
	}
	scope := p.Go.Scope()
	for _, name := range scope.Names() {
		o := scope.Lookup(name)
		if !o.Exported() {
			continue
		}
		switch o := o.(type) {
		case *gotypes.Func:
			c.add(callItem(name, itemFunction, types.LiftSignature(o.Type().(*gotypes.Signature)), nil), "0")
		case *gotypes.TypeName:
			kind := itemClass
			if gotypes.IsInterface(o.Type()) {
				kind = itemInterface
			} else if _, ok := o.Type().Underlying().(*gotypes.Struct); ok {
				kind = itemStruct
			}
			c.add(completionItem{Label: name, Kind: kind, Detail: "type"}, "0")
		case *gotypes.Const:
			c.add(completionItem{Label: name, Kind: itemConstant, Detail: typeString(types.Lift(o.Type()))}, "0")
		case *gotypes.Var:
			c.add(completionItem{Label: name, Kind: itemVariable, Detail: typeString(types.Lift(o.Type()))}, "0")
		}
	}
}

func exported(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}

// callItem is a function or method item that inserts name($0).
func callItem(name string, kind int, sig *types.Signature, names []string) completionItem {
	insert := name + "($0)"
	if sig != nil && len(sig.Params) == 0 {
		insert = name + "()"
	}
	return completionItem{Label: name, Kind: kind, Detail: signatureText(sig, names), InsertText: insert, InsertTextFormat: 2}
}

// symbolItem is the completion item for a package-level symbol.
func symbolItem(sym check.Symbol) (completionItem, bool) {
	switch v := sym.(type) {
	case *check.FuncSym:
		if v.ExtRecv != nil || v.Recv != nil {
			return completionItem{}, false
		}
		var names []string
		if v.Decl != nil {
			for _, p := range v.Decl.Params {
				if p.Name != nil {
					names = append(names, p.Name.Name)
				}
			}
		}
		it := callItem(v.Name(), itemFunction, v.Sig, names)
		if v.Decl != nil && v.Decl.Doc != "" {
			it.Documentation = markup(docText(v.Decl.Doc))
		}
		return it, true
	case *check.TypeSym:
		kind := itemClass
		if v.Decl != nil {
			switch v.Decl.Kind {
			case types.StructDecl:
				kind = itemStruct
			case types.InterfaceDecl:
				kind = itemInterface
			case types.EnumDecl:
				kind = itemEnum
			}
		}
		return completionItem{Label: v.Name(), Kind: kind, Detail: "type"}, true
	case *check.VarSym:
		return completionItem{Label: v.Name(), Kind: itemVariable, Detail: typeString(v.Type)}, true
	case *check.ConstSym:
		return completionItem{Label: v.Name(), Kind: itemConstant, Detail: typeString(v.Type)}, true
	}
	return completionItem{}, false
}

// scopeCompletions lists the names visible at offset off: locals, the
// receiver's fields and methods, the package's declarations, imports,
// built-ins, and keywords.
func (a *analysis) scopeCompletions(c *completions, fd *fileData, off int) {
	if fd.info != nil {
		scopes := scopeSpans(fd.syn)
		var locals []*check.VarSym
		for id, sym := range fd.info.Defs {
			v, ok := sym.(*check.VarSym)
			if !ok || v.Kind == check.GlobalVar || id.S.File != fd.src || id.S.Start >= off || id.Name == "_" || id.Name == placeholder {
				continue
			}
			if sc := innermost(scopes, id.S); sc.IsValid() && sc.Start <= off && off <= sc.End {
				locals = append(locals, v)
			}
		}
		// Nearest declarations first.
		sort.Slice(locals, func(i, j int) bool { return locals[i].Span.Start > locals[j].Span.Start })
		for _, v := range locals {
			c.add(completionItem{Label: v.Name(), Kind: itemVariable, Detail: typeString(v.Type)}, "0")
		}
		// In a method, the receiver's fields and methods by bare name.
		for f, fi := range fd.info.Funcs {
			if f.S.File == fd.src && f.S.Start <= off && off <= f.S.End && fi.Sym != nil && fi.Sym.Recv != nil {
				for _, fl := range fi.Sym.Recv.Fields {
					c.add(completionItem{Label: fl.Name, Kind: itemField, Detail: typeString(fl.Type)}, "1")
				}
				for _, m := range fi.Sym.Recv.Methods {
					c.add(callItem(m.Name, itemMethod, m.Sig, nil), "1")
				}
			}
		}
	}
	if fd.pkg != nil && fd.pkg.Checked != nil && fd.pkg.Checked.Scope != nil {
		for _, sym := range fd.pkg.Checked.Scope.Symbols() {
			if it, ok := symbolItem(sym); ok && it.Label != placeholder {
				c.add(it, "2")
			}
		}
	}
	for _, imp := range fd.syn.Imports {
		name := path.Base(imp.Path)
		if imp.Alias != nil {
			name = imp.Alias.Name
		} else if fd.pkg != nil && fd.pkg.Checked != nil {
			if p := fd.pkg.Checked.Imports[imp.Path]; p != nil {
				name = p.Name
			}
		}
		if name != "_" {
			c.add(completionItem{Label: name, Kind: itemModule, Detail: "import \"" + imp.Path + "\""}, "3")
		}
	}
	for _, b := range builtinFuncs {
		c.add(completionItem{Label: b[0], Kind: itemFunction, Detail: b[1], InsertText: b[0] + "($0)", InsertTextFormat: 2}, "4")
	}
	for _, t := range builtinTypes {
		c.add(completionItem{Label: t, Kind: itemTypeParam, Detail: "built-in type"}, "5")
	}
	for _, k := range keywordList {
		c.add(completionItem{Label: k, Kind: itemKeyword}, "6")
	}
	for _, sn := range snippets {
		c.add(completionItem{Label: sn[0], Kind: itemSnippet, Detail: sn[2], InsertText: sn[1], InsertTextFormat: 2, FilterText: strings.ReplaceAll(sn[0], " ", "")}, "7")
	}
}

// scopeSpans returns the spans of the nodes that open a scope.
func scopeSpans(f *syntax.File) []source.Span {
	var out []source.Span
	for _, d := range f.Decls {
		syntax.Walk(d, func(n syntax.Node) {
			switch n.(type) {
			case *syntax.Block, *syntax.FunDecl, *syntax.LambdaExpr, *syntax.ForStmt, *syntax.CatchExpr, *syntax.WhenExpr, *syntax.SelectCase:
				out = append(out, n.Span())
			}
		})
	}
	return out
}

// innermost returns the smallest scope span containing sp.
func innermost(scopes []source.Span, sp source.Span) source.Span {
	var best source.Span
	for _, sc := range scopes {
		if sc.Start <= sp.Start && sp.End <= sc.End && (!best.IsValid() || sc.Len() < best.Len()) {
			best = sc
		}
	}
	return best
}

var builtinFuncs = [][2]string{
	{"println", "(args: ...any)"}, {"print", "(args: ...any)"}, {"len", "(v): int"}, {"cap", "(v): int"},
	{"make", "(T, size...): T"}, {"append", "(s: []T, elems: ...T): []T"}, {"copy", "(dst: []T, src: []T): int"},
	{"delete", "(m: map[K]V, key: K)"}, {"clear", "(v)"}, {"min", "(x: T, y: ...T): T"}, {"max", "(x: T, y: ...T): T"},
	{"panic", "(v: any)"}, {"recover", "(): any"}, {"new", "(T): *T"},
}

var builtinTypes = []string{
	"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"float32", "float64", "complex64", "complex128", "string", "bool", "byte", "rune", "any", "error", "comparable",
}

var keywordList = []string{
	"func", "struct", "interface", "enum", "sealed", "typealias", "const", "var", "import", "if", "else", "when", "for",
	"in", "while", "return", "break", "continue", "throw", "throws", "catch", "is", "as", "defer", "go", "select", "self",
	"null", "true", "false", "map", "chan",
}

var snippets = [][3]string{
	{"main", "func main() {\n\t$0\n}", "main function"},
	{"main throws", "func main() throws {\n\t$0\n}", "main function that can fail"},
	{"func", "func ${1:name}(${2}): ${3:Type} {\n\t$0\n}", "function"},
	{"struct", "struct ${1:Name} {\n\t${2:Field}: ${3:Type}\n}", "struct"},
	{"interface", "interface ${1:Name} {\n\t${2:Method}(): ${3:Type}\n}", "interface"},
	{"enum", "enum ${1:Name} { ${2:First}, ${3:Second} }", "enum"},
	{"when", "when ${1:value} {\n\t${2:case} -> ${3}\n\telse -> ${0}\n}", "when"},
	{"for in", "for ${1:x} in ${2:xs} {\n\t$0\n}", "for loop"},
	{"catch", "catch (${1:err}) {\n\t$0\n}", "handle an error"},
	{"test", "func Test${1:Name}(t: *testing.T) {\n\t$0\n}", "test function"},
}

// ---------------------------------------------------------------------------
// import paths

var (
	stdOnce sync.Once
	stdPkgs []string
)

func (s *Server) importCompletions(c *completions, file string, idx *lineIndex, start, end int) {
	edit := func(p string) *TextEdit { return &TextEdit{Range: idx.rangeOf(start, end), NewText: p} }
	a := s.analyze(file, true)
	seen := map[string]bool{}
	for _, fd := range a.files {
		if fd.pkg == nil || seen[fd.pkg.Dir.ImportPath] || fd.pkg.Dir.Dir == dirOf(file) {
			continue
		}
		seen[fd.pkg.Dir.ImportPath] = true
		p := fd.pkg.Dir.ImportPath
		c.add(completionItem{Label: p, Kind: itemModule, Detail: "Lyth package", TextEdit: edit(p)}, "0")
	}
	c.add(completionItem{Label: "lyth/coll", Kind: itemModule, Detail: "Lyth standard library", TextEdit: edit("lyth/coll")}, "1")
	stdOnce.Do(func() {
		out, err := exec.Command("go", "list", "std").Output()
		if err != nil {
			return
		}
		for _, p := range strings.Fields(string(out)) {
			if !strings.Contains(p, "internal") && !strings.HasPrefix(p, "vendor/") {
				stdPkgs = append(stdPkgs, p)
			}
		}
	})
	for _, p := range stdPkgs {
		c.add(completionItem{Label: p, Kind: itemModule, Detail: "Go standard library", TextEdit: edit(p)}, "2")
	}
}

func dirOf(file string) string {
	i := strings.LastIndexAny(file, `/\`)
	if i < 0 {
		return "."
	}
	return file[:i]
}

// ---------------------------------------------------------------------------
// signature help

func (s *Server) signatureHelp(params json.RawMessage) (any, error) {
	path, pos := positionParams(params)
	text, ok := s.doc(path)
	if !ok {
		return nil, nil
	}
	data := []byte(text)
	idx := newLineIndex(data)
	off := idx.offset(pos)
	open, active, named := enclosingCall(text, off)
	if open < 0 {
		return nil, nil
	}
	// Close the call where its line ends, so that it parses.
	end := strings.IndexByte(text[off:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += off
	}
	a := s.analyzeWith(path, text[:open]+"()"+text[end:])
	fd := a.files[path]
	if fd == nil || fd.info == nil {
		return nil, nil
	}
	var call *syntax.CallExpr
	for _, d := range fd.syn.Decls {
		syntax.Walk(d, func(n syntax.Node) {
			if x, ok := n.(*syntax.CallExpr); ok && x.Lparen.Start == open {
				call = x
			}
		})
	}
	if call == nil {
		return nil, nil
	}
	label, plist, doc := callSignature(fd.info, call)
	if label == "" {
		return nil, nil
	}
	if named != "" {
		for i, p := range plist {
			if p.name == named {
				active = i
			}
		}
	}
	infos := []any{}
	for _, p := range plist {
		infos = append(infos, map[string]any{"label": []int{utf16Index(label, p.start), utf16Index(label, p.end)}})
	}
	sig := map[string]any{"label": label, "parameters": infos}
	if doc != "" {
		sig["documentation"] = markup(docText(doc))
	}
	if len(plist) > 0 && active >= len(plist) {
		active = len(plist) - 1
	}
	return map[string]any{"signatures": []any{sig}, "activeSignature": 0, "activeParameter": active}, nil
}

func utf16Index(s string, byteOff int) int { return len(utf16.Encode([]rune(s[:byteOff]))) }

// enclosingCall finds the unclosed ( before off: its offset, the index of
// the argument at off, and the name of a named argument being written.
func enclosingCall(text string, off int) (open, arg int, named string) {
	depth, commas := 0, 0
	lastComma := off
	for i := off - 1; i >= 0 && off-i < 4000; i-- {
		switch text[i] {
		case ')', ']', '}':
			depth++
		case '[':
			depth--
		case '{':
			if depth == 0 {
				return -1, 0, ""
			}
			depth--
		case '(':
			if depth == 0 {
				seg := strings.TrimSpace(text[i+1 : off])
				if commas > 0 {
					seg = strings.TrimSpace(text[lastComma+1 : off])
				}
				if eq := strings.Index(seg, "="); eq > 0 && !strings.HasPrefix(seg[eq:], "==") {
					if name := strings.TrimSpace(seg[:eq]); identRE.MatchString(name) {
						named = name
					}
				}
				return i, commas, named
			}
			depth--
		case ',':
			if depth == 0 {
				if commas == 0 {
					lastComma = i
				}
				commas++
			}
		}
	}
	return -1, 0, ""
}

type paramLabel struct {
	name       string
	start, end int
}

// callSignature renders the signature of the function a call calls, with
// the byte ranges of its parameters.
func callSignature(info *check.Info, call *syntax.CallExpr) (string, []paramLabel, string) {
	ci := info.Calls[call]
	var fn *check.FuncSym
	var goFn *gotypes.Func
	name := call.Fun.Span().Text()
	switch f := syntax.Unparen(call.Fun).(type) {
	case *syntax.Ident:
		fn, _ = info.Uses[f].(*check.FuncSym)
		name = f.Name
	case *syntax.SelectorExpr:
		name = f.Sel.Name
		if s := info.Selections[f]; s != nil {
			switch o := s.Obj.(type) {
			case *check.FuncSym:
				fn = o
			case *types.Method:
				fn, _ = o.Sym.(*check.FuncSym)
			case *check.ExtCall:
				fn = o.Func
			case *gotypes.Func:
				goFn = o
			}
		}
	}
	var b strings.Builder
	var params []paramLabel
	b.WriteString(name + "(")
	param := func(pname, text string) {
		if len(params) > 0 {
			b.WriteString(", ")
		}
		start := b.Len()
		b.WriteString(text)
		params = append(params, paramLabel{pname, start, b.Len()})
	}
	var sig *types.Signature
	doc := ""
	switch {
	case ci != nil && ci.Kind == check.CallConstruct:
		n, ok := types.Base(ci.Target).(*types.Named)
		if !ok || n.Decl == nil {
			return "", nil, ""
		}
		for _, f := range n.Decl.Fields {
			param(f.Name, f.Name+": "+typeString(f.Type))
		}
		b.WriteString(")")
		return b.String(), params, ""
	case fn != nil && fn.Decl != nil:
		sig = fn.Sig
		if ci != nil && ci.Sig != nil {
			sig = ci.Sig
		}
		doc = fn.Decl.Doc
		for i, p := range fn.Decl.Params {
			text := p.Name.Name + ": "
			if sig != nil && i < len(sig.Params) {
				text += typeString(sig.Params[i])
			} else if p.Type != nil {
				text += p.Type.Span().Text()
			}
			if p.Default != nil {
				text += " = " + p.Default.Span().Text()
			}
			param(p.Name.Name, text)
		}
	default:
		if ci != nil {
			sig = ci.Sig
		}
		if sig == nil {
			if s, ok := info.TypeOf(call.Fun).(*types.Signature); ok {
				sig = s
			}
		}
		if sig == nil {
			return "", nil, ""
		}
		var goSig *gotypes.Signature
		if goFn != nil {
			goSig, _ = goFn.Type().(*gotypes.Signature)
		}
		for i, p := range sig.Params {
			pname := ""
			if i < len(sig.ParamNames) {
				pname = sig.ParamNames[i]
			}
			if pname == "" && goSig != nil && i < goSig.Params().Len() {
				pname = goSig.Params().At(i).Name()
			}
			text := typeString(p)
			if sig.Variadic && i == len(sig.Params)-1 {
				if sl, ok := p.(*types.Slice); ok {
					text = "..." + typeString(sl.Elem)
				}
			}
			if pname != "" && pname != "_" {
				text = pname + ": " + text
			}
			param(pname, text)
		}
	}
	b.WriteString(")")
	if sig != nil {
		if len(sig.Results) > 0 {
			b.WriteString(": " + resultsText(sig))
		}
		if sig.Throws {
			b.WriteString(" throws")
		}
	}
	return b.String(), params, doc
}
