// Package lower translates a type-checked Lyth package into Go syntax trees.
//
// Lowering reads only check.Info; it never re-derives types. Every emitted
// Go node that corresponds to Lyth source is recorded in a span table so the
// emitter can build an exact source map. Expressions that need statements
// (?, catch, if/when expressions, ?. and ?: chains, !!) are linearized into
// preceding statements while preserving left-to-right evaluation order.
package lower

import (
	"fmt"
	"go/ast"
	"go/doc/comment"
	"go/token"
	gotypes "go/types"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// File is the lowered form of one Lyth source file.
type File struct {
	Src       *source.File
	GoName    string // generated file name, e.g. users.lyth.go
	AST       *ast.File
	Fset      *token.FileSet // holds the mirror file used for layout positions
	Spans     map[ast.Node]source.Span
	Synthetic map[ast.Node]bool
	Build     string // //go:build line, if any
}

// Package lowers all files of a checked package.
func Package(pkg *check.Package) []*File {
	pkgNames := map[string]bool{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.FunDecl:
				if d.Recv == nil {
					if sym, ok := pkg.Scope.LookupLocal(d.Name.Name).(*check.FuncSym); ok {
						pkgNames[sym.GoName] = true
					}
				}
			case *syntax.StructDecl, *syntax.InterfaceDecl, *syntax.PropDecl, *syntax.ConstDecl, *syntax.TypeAliasDecl:
				name := declName(d)
				if sym := pkg.Scope.LookupLocal(name); sym != nil {
					pkgNames[goNameOf(sym)] = true
				}
			}
		}
	}
	var out []*File
	for _, f := range pkg.Files {
		out = append(out, lowerFile(pkg, f, pkgNames))
	}
	return out
}

func declName(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.StructDecl:
		return d.Name.Name
	case *syntax.InterfaceDecl:
		return d.Name.Name
	case *syntax.PropDecl:
		return d.Name.Name
	case *syntax.ConstDecl:
		return d.Name.Name
	case *syntax.TypeAliasDecl:
		return d.Name.Name
	}
	return ""
}

func goNameOf(sym check.Symbol) string {
	switch s := sym.(type) {
	case *check.FuncSym:
		return s.GoName
	case *check.VarSym:
		return s.GoName
	case *check.ConstSym:
		return s.GoName
	case *check.TypeSym:
		if s.Decl != nil {
			return s.Decl.GoName
		}
		if s.Alias != nil {
			return s.GoName
		}
	}
	return sym.Name()
}

// GoFileName maps a Lyth file name to its generated Go file name, keeping
// Go's _test and GOOS/GOARCH file-name conventions (compiler.md §6.2).
func GoFileName(lythName string) string {
	base := strings.TrimSuffix(lythName, ".lyth")
	if strings.HasSuffix(base, "_test") {
		return strings.TrimSuffix(base, "_test") + ".lyth_test.go"
	}
	return base + ".lyth.go"
}

// fileLowerer holds per-file state.
type fileLowerer struct {
	pkg       *check.Package
	info      *check.Info
	src       *syntax.File
	fset      *token.FileSet
	mirror    *token.File
	spans     map[ast.Node]source.Span
	synthetic map[ast.Node]bool
	pkgNames  map[string]bool
	// imports: path -> local name, and local name -> path.
	imports     map[string]string
	importNames map[string]string
	usedImports map[string]bool
	blank       []string
	decls       []ast.Decl
	runName     string
}

func lowerFile(pkg *check.Package, f *syntax.File, pkgNames map[string]bool) *File {
	fset := token.NewFileSet()
	mirror := fset.AddFile(f.Src.Path, -1, len(f.Src.Content)+1)
	mirror.SetLinesForContent(f.Src.Content)
	l := &fileLowerer{
		pkg: pkg, info: pkg.Info, src: f, fset: fset, mirror: mirror,
		spans: map[ast.Node]source.Span{}, synthetic: map[ast.Node]bool{}, pkgNames: pkgNames,
		imports: map[string]string{}, importNames: map[string]string{}, usedImports: map[string]bool{},
	}
	// User imports keep their local names.
	for _, imp := range f.Imports {
		p := pkg.Imports[imp.Path]
		if p == nil {
			continue
		}
		if imp.Alias != nil && imp.Alias.Name == "_" {
			l.blank = append(l.blank, imp.Path)
			continue
		}
		name := p.Name
		if imp.Alias != nil {
			name = imp.Alias.Name
		}
		l.imports[imp.Path] = name
		l.importNames[name] = imp.Path
	}
	for _, d := range f.Decls {
		l.decl(d)
	}
	file := &ast.File{Name: ast.NewIdent(pkg.Name), Decls: l.importDecls()}
	file.Decls = append(file.Decls, l.decls...)
	return &File{Src: f.Src, GoName: GoFileName(f.Src.Base()), AST: file, Fset: fset, Spans: l.spans, Synthetic: l.synthetic, Build: f.BuildConstraint}
}

// pos returns a layout position mirroring a Lyth span start.
func (l *fileLowerer) pos(s source.Span) token.Pos {
	if !s.IsValid() || s.File != l.src.Src {
		return token.NoPos
	}
	return l.mirror.Pos(s.Start)
}

func (l *fileLowerer) endPos(s source.Span) token.Pos {
	if !s.IsValid() || s.File != l.src.Src {
		return token.NoPos
	}
	end := s.End - 1
	if end < s.Start {
		end = s.Start
	}
	return l.mirror.Pos(end)
}

// mark records the Lyth span of a generated node.
func (l *fileLowerer) mark(n ast.Node, s source.Span) {
	if n != nil && s.IsValid() {
		if _, exists := l.spans[n]; !exists {
			l.spans[n] = s
		}
	}
}

// markSynth records a compiler-introduced node attributed to span s.
func (l *fileLowerer) markSynth(n ast.Node, s source.Span) {
	if n != nil && s.IsValid() {
		if _, exists := l.spans[n]; !exists {
			l.spans[n] = s
			l.synthetic[n] = true
		}
	}
}

// useImport returns the local name for an import path, adding the import
// if needed. preferred is the package's declared name.
func (l *fileLowerer) useImport(path, preferred string) string {
	if name, ok := l.imports[path]; ok {
		l.usedImports[path] = true
		return name
	}
	name := preferred
	for i := 2; l.importNames[name] != "" || l.pkgNames[name]; i++ {
		name = fmt.Sprintf("%s%d", preferred, i)
	}
	l.imports[path] = name
	l.importNames[name] = path
	l.usedImports[path] = true
	return name
}

func (l *fileLowerer) importDecls() []ast.Decl {
	var paths []string
	for p := range l.usedImports {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) == 0 && len(l.blank) == 0 {
		return nil
	}
	gd := &ast.GenDecl{Tok: token.IMPORT, Lparen: 1, Rparen: 1}
	for _, p := range paths {
		spec := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(p)}}
		if name := l.imports[p]; name != pkgNameGuess(l.pkg, p) {
			spec.Name = ast.NewIdent(name)
		}
		gd.Specs = append(gd.Specs, spec)
	}
	for _, p := range l.blank {
		gd.Specs = append(gd.Specs, &ast.ImportSpec{Name: ast.NewIdent("_"), Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(p)}})
	}
	if len(gd.Specs) == 1 {
		gd.Lparen, gd.Rparen = 0, 0
	}
	return []ast.Decl{gd}
}

// pkgNameGuess returns the declared name of an imported package if known.
func pkgNameGuess(pkg *check.Package, p string) string {
	if ip := pkg.Imports[p]; ip != nil {
		return ip.Name
	}
	if known, ok := supportPkgs[p]; ok {
		return known
	}
	return path.Base(p)
}

var supportPkgs = map[string]string{"errors": "errors", "fmt": "fmt", "os": "os", "slices": "slices", "strconv": "strconv", "strings": "strings"}

// importFor returns the qualifier for a Go or Lyth package.
func (l *fileLowerer) importFor(p *gotypes.Package) string {
	return l.useImport(p.Path(), p.Name())
}

// ---------------------------------------------------------------------------
// Types

// typeExpr returns the Go type expression for a Lyth type.
func (l *fileLowerer) typeExpr(t types.Type) ast.Expr {
	return l.goTypeExpr(types.Lower(t))
}

func (l *fileLowerer) goTypeExpr(t gotypes.Type) ast.Expr {
	switch t := t.(type) {
	case *gotypes.Basic:
		return ast.NewIdent(t.Name())
	case *gotypes.Alias:
		obj := t.Obj()
		if obj.Pkg() == nil {
			return ast.NewIdent(obj.Name())
		}
		return l.qualified(obj, nil)
	case *gotypes.Named:
		var targs []gotypes.Type
		if ta := t.TypeArgs(); ta != nil {
			for i := 0; i < ta.Len(); i++ {
				targs = append(targs, ta.At(i))
			}
		}
		return l.qualified(t.Obj(), targs)
	case *gotypes.Pointer:
		return &ast.StarExpr{X: l.goTypeExpr(t.Elem())}
	case *gotypes.Slice:
		return &ast.ArrayType{Elt: l.goTypeExpr(t.Elem())}
	case *gotypes.Array:
		return &ast.ArrayType{Len: &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(t.Len(), 10)}, Elt: l.goTypeExpr(t.Elem())}
	case *gotypes.Map:
		return &ast.MapType{Key: l.goTypeExpr(t.Key()), Value: l.goTypeExpr(t.Elem())}
	case *gotypes.Chan:
		dir := ast.SEND | ast.RECV
		switch t.Dir() {
		case gotypes.SendOnly:
			dir = ast.SEND
		case gotypes.RecvOnly:
			dir = ast.RECV
		}
		return &ast.ChanType{Dir: dir, Value: l.goTypeExpr(t.Elem()), Begin: 1, Arrow: 1}
	case *gotypes.Signature:
		return l.goFuncType(t)
	case *gotypes.Interface:
		if t.Empty() {
			return ast.NewIdent("any")
		}
		it := &ast.InterfaceType{Methods: &ast.FieldList{}}
		for i := 0; i < t.NumExplicitMethods(); i++ {
			m := t.ExplicitMethod(i)
			it.Methods.List = append(it.Methods.List, &ast.Field{Names: []*ast.Ident{ast.NewIdent(m.Name())}, Type: l.goFuncType(m.Type().(*gotypes.Signature))})
		}
		for i := 0; i < t.NumEmbeddeds(); i++ {
			it.Methods.List = append(it.Methods.List, &ast.Field{Type: l.goTypeExpr(t.EmbeddedType(i))})
		}
		return it
	case *gotypes.Struct:
		st := &ast.StructType{Fields: &ast.FieldList{}}
		for i := 0; i < t.NumFields(); i++ {
			f := t.Field(i)
			fld := &ast.Field{Type: l.goTypeExpr(f.Type())}
			if !f.Embedded() {
				fld.Names = []*ast.Ident{ast.NewIdent(f.Name())}
			}
			if tag := t.Tag(i); tag != "" {
				fld.Tag = tagLit(tag)
			}
			st.Fields.List = append(st.Fields.List, fld)
		}
		return st
	case *gotypes.TypeParam:
		return ast.NewIdent(t.Obj().Name())
	}
	return ast.NewIdent("invalid")
}

func (l *fileLowerer) qualified(obj *gotypes.TypeName, targs []gotypes.Type) ast.Expr {
	var x ast.Expr
	if obj.Pkg() == nil || obj.Pkg().Path() == l.pkg.Path {
		x = ast.NewIdent(obj.Name())
	} else {
		x = &ast.SelectorExpr{X: ast.NewIdent(l.importFor(obj.Pkg())), Sel: ast.NewIdent(obj.Name())}
	}
	if len(targs) == 0 {
		return x
	}
	idx := make([]ast.Expr, len(targs))
	for i, a := range targs {
		idx[i] = l.goTypeExpr(a)
	}
	if len(idx) == 1 {
		return &ast.IndexExpr{X: x, Index: idx[0]}
	}
	return &ast.IndexListExpr{X: x, Indices: idx}
}

func (l *fileLowerer) goFuncType(s *gotypes.Signature) *ast.FuncType {
	ft := &ast.FuncType{Params: &ast.FieldList{}}
	for i := 0; i < s.Params().Len(); i++ {
		p := s.Params().At(i)
		var te ast.Expr = l.goTypeExpr(p.Type())
		if s.Variadic() && i == s.Params().Len()-1 {
			te = &ast.Ellipsis{Elt: l.goTypeExpr(p.Type().(*gotypes.Slice).Elem())}
		}
		ft.Params.List = append(ft.Params.List, &ast.Field{Type: te})
	}
	if s.Results().Len() > 0 {
		ft.Results = &ast.FieldList{}
		for i := 0; i < s.Results().Len(); i++ {
			ft.Results.List = append(ft.Results.List, &ast.Field{Type: l.goTypeExpr(s.Results().At(i).Type())})
		}
	}
	return ft
}

func tagLit(tag string) *ast.BasicLit {
	if strings.Contains(tag, "`") || strings.ContainsAny(tag, "\r\n") {
		return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(tag)}
	}
	return &ast.BasicLit{Kind: token.STRING, Value: "`" + tag + "`"}
}

// zeroValue returns the Go zero value expression of a Lyth type.
func (l *fileLowerer) zeroValue(t types.Type) ast.Expr {
	gt := types.Lower(t)
	switch u := gt.Underlying().(type) {
	case *gotypes.Basic:
		switch {
		case u.Info()&gotypes.IsBoolean != 0:
			return ast.NewIdent("false")
		case u.Info()&gotypes.IsString != 0:
			return &ast.BasicLit{Kind: token.STRING, Value: `""`}
		case u.Info()&gotypes.IsNumeric != 0:
			return &ast.BasicLit{Kind: token.INT, Value: "0"}
		}
		return ast.NewIdent("nil")
	case *gotypes.Struct, *gotypes.Array:
		return &ast.CompositeLit{Type: l.goTypeExpr(gt)}
	case *gotypes.TypeParam:
		return &ast.StarExpr{X: &ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{l.goTypeExpr(gt)}}}
	}
	return ast.NewIdent("nil")
}

// ---------------------------------------------------------------------------
// Declarations

func docGroup(doc string) *ast.CommentGroup {
	if doc == "" {
		return nil
	}
	g := &ast.CommentGroup{}
	for _, line := range strings.Split(canonicalDoc(doc), "\n") {
		g.List = append(g.List, &ast.Comment{Text: line})
	}
	return g
}

// canonicalDoc formats a // doc comment as gofmt does (go/doc/comment), so
// the generated file is gofmt-clean: indented lines become tab-indented
// code blocks, and lists and headings get their canonical spacing.
func canonicalDoc(doc string) string {
	var text strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		after, ok := strings.CutPrefix(line, "//")
		if !ok {
			return doc // a /* */ comment
		}
		text.WriteString(strings.TrimPrefix(after, " "))
		text.WriteByte('\n')
	}
	var p comment.Parser
	var pr comment.Printer
	// Printer.Comment returns bare text; add the markers as go/printer does.
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(string(pr.Comment(p.Parse(text.String()))), "\n"), "\n") {
		switch {
		case line == "":
			out = append(out, "//")
		case strings.HasPrefix(line, "\t"):
			out = append(out, "//"+line)
		default:
			out = append(out, "// "+line)
		}
	}
	return strings.Join(out, "\n")
}

// memberDoc returns the doc comment of a struct field or interface method
// declared at span s, positioned on the lines before it so go/printer keeps
// it on its own lines.
func (l *fileLowerer) memberDoc(doc string, s source.Span) *ast.CommentGroup {
	g := docGroup(doc)
	if g == nil || !s.IsValid() || s.File != l.src.Src {
		return g
	}
	first := s.StartPos().Line - len(g.List)
	for i, c := range g.List {
		if line := first + i; line >= 1 && line <= l.mirror.LineCount() {
			c.Slash = l.mirror.LineStart(line)
		}
	}
	return g
}

func (l *fileLowerer) decl(d syntax.Decl) {
	switch d := d.(type) {
	case *syntax.StructDecl:
		l.structDecl(d)
	case *syntax.InterfaceDecl:
		l.interfaceDecl(d)
	case *syntax.EnumDecl:
		l.enumDecl(d)
	case *syntax.FunDecl:
		l.funDecl(d)
	case *syntax.PropDecl:
		l.propDecl(d)
	case *syntax.ConstDecl:
		l.constDecl(d)
	case *syntax.TypeAliasDecl:
		l.aliasDecl(d)
	}
}

func (l *fileLowerer) aliasDecl(d *syntax.TypeAliasDecl) {
	ts := l.typeSym(d.Name.Name)
	if ts == nil || ts.Type == nil {
		return
	}
	spec := &ast.TypeSpec{Name: ast.NewIdent(ts.GoName), Assign: 1, Type: l.typeExpr(ts.Type)}
	l.mark(spec.Name, d.Name.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.TYPE, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
}

func (l *fileLowerer) typeSym(name string) *check.TypeSym {
	ts, _ := l.pkg.Scope.LookupLocal(name).(*check.TypeSym)
	return ts
}

func (l *fileLowerer) structDecl(d *syntax.StructDecl) {
	ts := l.typeSym(d.Name.Name)
	if ts == nil || ts.Decl == nil {
		return
	}
	st := &ast.StructType{Fields: &ast.FieldList{Opening: l.pos(d.S), Closing: l.endPos(d.S)}}
	for i, f := range ts.Decl.Fields {
		fld := &ast.Field{Names: []*ast.Ident{ast.NewIdent(f.GoName)}, Type: l.typeExpr(f.Type)}
		if f.Tag != "" {
			fld.Tag = tagLit(f.Tag)
		}
		if i < len(d.Fields) {
			fld.Doc = l.memberDoc(d.Fields[i].Doc, d.Fields[i].S)
			fld.Names[0].NamePos = l.pos(d.Fields[i].S)
			l.mark(fld, d.Fields[i].S)
			l.mark(fld.Names[0], f.Span)
		}
		st.Fields.List = append(st.Fields.List, fld)
	}
	spec := &ast.TypeSpec{Name: ast.NewIdent(ts.Decl.GoName), Type: st}
	if len(ts.Decl.TypeParams) > 0 {
		spec.TypeParams = l.typeParamList(ts.Decl.TypeParams)
	}
	l.mark(spec.Name, d.Name.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.TYPE, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
	l.conformance(d, ts)
}

// conformance lowers `struct T : I` to Go's compile-time assertion
// var _ I = T{}, or var _ I = (*T)(nil) when only *T implements I.
func (l *fileLowerer) conformance(d *syntax.StructDecl, ts *check.TypeSym) {
	confs := l.info.Conforms[d]
	if len(confs) == 0 || len(ts.Decl.TypeParams) > 0 {
		// A generic struct has no Go assertion without type arguments; the
		// checker has verified the declaration.
		return
	}
	gd := &ast.GenDecl{Tok: token.VAR, TokPos: l.endPos(d.S)}
	for _, cf := range confs {
		var val ast.Expr = &ast.CompositeLit{Type: ast.NewIdent(ts.Decl.GoName)}
		if cf.Ptr {
			val = &ast.CallExpr{Fun: &ast.ParenExpr{X: &ast.StarExpr{X: ast.NewIdent(ts.Decl.GoName)}}, Args: []ast.Expr{ast.NewIdent("nil")}}
		}
		// The specs stay unmarked: line directives between them would break
		// gofmt's alignment of the group.
		gd.Specs = append(gd.Specs, &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("_")}, Type: l.typeExpr(cf.Iface), Values: []ast.Expr{val}})
	}
	l.mark(gd, confs[0].Span)
	l.decls = append(l.decls, gd)
	// Members of sealed interfaces get the interface's marker method.
	for _, cf := range confs {
		sd := types.SealedOf(cf.Iface)
		if sd == nil {
			continue
		}
		pos := l.endPos(d.S)
		fd := &ast.FuncDecl{
			Recv: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent(ts.Decl.GoName)}}},
			Name: ast.NewIdent(sd.Marker),
			Type: &ast.FuncType{Func: pos, Params: &ast.FieldList{}},
			Body: &ast.BlockStmt{Lbrace: pos, Rbrace: pos}, // {} on one line
		}
		markTree(l, fd, cf.Span)
		l.decls = append(l.decls, fd)
	}
}

// entryRef names the Go constant of an enum entry, qualified when the enum
// belongs to another package.
func (l *fileLowerer) entryRef(e *types.EnumEntry) ast.Expr {
	if e.Decl.PkgPath == l.pkg.Path {
		return ast.NewIdent(e.GoName)
	}
	return &ast.SelectorExpr{X: ast.NewIdent(l.useImport(e.Decl.PkgPath, e.Decl.PkgName)), Sel: ast.NewIdent(e.GoName)}
}

// enumDecl lowers `enum Color { Red, Green }` to Go's iota idiom:
//
//	type Color int
//
//	const (
//		ColorRed Color = iota
//		ColorGreen
//	)
//
// and a String method that returns the entry's name.
func (l *fileLowerer) enumDecl(d *syntax.EnumDecl) {
	ts := l.typeSym(d.Name.Name)
	if ts == nil || ts.Decl == nil {
		return
	}
	decl := ts.Decl
	spec := &ast.TypeSpec{Name: ast.NewIdent(decl.GoName), Type: ast.NewIdent("int")}
	l.mark(spec.Name, d.Name.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.TYPE, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
	if len(decl.Entries) == 0 {
		return
	}

	consts := &ast.GenDecl{Tok: token.CONST, TokPos: l.pos(d.S), Lparen: l.pos(d.S), Rparen: l.endPos(d.S)}
	for i, e := range decl.Entries {
		vs := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(e.GoName)}}
		if i == 0 {
			vs.Type, vs.Values = ast.NewIdent(decl.GoName), []ast.Expr{ast.NewIdent("iota")}
		}
		consts.Specs = append(consts.Specs, vs)
	}
	markTree(l, consts, d.S)
	l.decls = append(l.decls, consts)

	recv := strings.ToLower(decl.GoName[:1])
	sw := &ast.SwitchStmt{Tag: ast.NewIdent(recv), Body: &ast.BlockStmt{}}
	for _, e := range decl.Entries {
		sw.Body.List = append(sw.Body.List, &ast.CaseClause{List: []ast.Expr{ast.NewIdent(e.GoName)},
			Body: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{strLit(e.Name)}}}})
	}
	strconvPkg := l.useImport("strconv", "strconv")
	// Color(7) for a value that is not an entry, as Go's stringer prints.
	other := &ast.BinaryExpr{Op: token.ADD,
		X: &ast.BinaryExpr{Op: token.ADD, X: strLit(decl.Name + "("),
			Y: &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent(strconvPkg), Sel: ast.NewIdent("Itoa")},
				Args: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("int"), Args: []ast.Expr{ast.NewIdent(recv)}}}}},
		Y: strLit(")")}
	fd := &ast.FuncDecl{
		Doc:  &ast.CommentGroup{List: []*ast.Comment{{Text: "// String returns the name of the " + decl.Name + " entry."}}},
		Recv: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(recv)}, Type: ast.NewIdent(decl.GoName)}}},
		Name: ast.NewIdent("String"),
		Type: &ast.FuncType{Params: &ast.FieldList{}, Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}}},
		Body: &ast.BlockStmt{List: []ast.Stmt{sw, &ast.ReturnStmt{Results: []ast.Expr{other}}}},
	}
	markTree(l, fd, d.S)
	l.decls = append(l.decls, fd)
}

func (l *fileLowerer) interfaceDecl(d *syntax.InterfaceDecl) {
	ts := l.typeSym(d.Name.Name)
	if ts == nil || ts.Decl == nil {
		return
	}
	it := &ast.InterfaceType{Methods: &ast.FieldList{Opening: l.pos(d.S), Closing: l.endPos(d.S)}}
	for i, m := range ts.Decl.Methods {
		fld := &ast.Field{Names: []*ast.Ident{ast.NewIdent(m.GoName)}, Type: l.funcType(m.Sig, nil)}
		if i < len(d.Methods) {
			fld.Doc = l.memberDoc(d.Methods[i].Doc, d.Methods[i].S)
			fld.Names[0].NamePos = l.pos(d.Methods[i].S)
			l.mark(fld, d.Methods[i].S)
		}
		it.Methods.List = append(it.Methods.List, fld)
	}
	for _, e := range ts.Decl.Embeds {
		it.Methods.List = append(it.Methods.List, &ast.Field{Type: l.typeExpr(e)})
	}
	if ts.Decl.Sealed {
		// The unexported marker method keeps other types, and other
		// packages, from implementing the interface (ADR 0025).
		it.Methods.List = append(it.Methods.List, &ast.Field{Names: []*ast.Ident{ast.NewIdent(ts.Decl.Marker)}, Type: &ast.FuncType{Params: &ast.FieldList{}}})
	}
	spec := &ast.TypeSpec{Name: ast.NewIdent(ts.Decl.GoName), Type: it}
	if len(ts.Decl.TypeParams) > 0 {
		spec.TypeParams = l.typeParamList(ts.Decl.TypeParams)
	}
	l.mark(spec.Name, d.Name.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.TYPE, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
}

// recvParamName names an extension's receiver parameter as Go code names
// such a parameter: s for slices and strings, m for maps, ch for channels,
// and the type name's initial otherwise.
func recvParamName(t types.Type) string {
	switch b := types.Base(t).(type) {
	case *types.Pointer:
		return recvParamName(b.Elem)
	case *types.Slice, *types.Array:
		return "s"
	case *types.Map:
		return "m"
	case *types.Chan:
		return "ch"
	case *types.Named:
		name := b.String()
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		return strings.ToLower(name[:1])
	}
	if types.IsString(t) {
		return "s"
	}
	if types.IsNumeric(t) {
		return "n"
	}
	return "v"
}

// extFunc refers to an extension function: by name in its own package,
// qualified elsewhere.
func (l *fileLowerer) extFunc(ext *check.ExtCall) ast.Expr {
	if ext.Pkg == l.pkg {
		return ast.NewIdent(ext.Func.GoName)
	}
	return sel(l.useImport(ext.Pkg.Path, ext.Pkg.Name), ext.Func.GoName)
}

// typeParamList lowers type parameters to Go's [T C, U any], grouping
// consecutive parameters with the same constraint as Go code does ([K, V any]).
func (l *fileLowerer) typeParamList(tps []*types.TypeParam) *ast.FieldList {
	list := &ast.FieldList{}
	last := ""
	for _, tp := range tps {
		var constraint types.Type = types.Any
		if tp.Constraint != nil {
			constraint = tp.Constraint
		}
		ce := l.typeExpr(constraint)
		key := gotypes.ExprString(ce)
		if n := len(list.List); n > 0 && key == last {
			list.List[n-1].Names = append(list.List[n-1].Names, ast.NewIdent(tp.Name))
			continue
		}
		list.List = append(list.List, &ast.Field{Names: []*ast.Ident{ast.NewIdent(tp.Name)}, Type: ce})
		last = key
	}
	return list
}

// instantiate applies type arguments to a generic function or type: f[T].
func (l *fileLowerer) instantiate(x ast.Expr, targs []types.Type) ast.Expr {
	idx := make([]ast.Expr, len(targs))
	for i, t := range targs {
		idx[i] = l.typeExpr(t)
	}
	if len(idx) == 1 {
		return &ast.IndexExpr{X: x, Index: idx[0]}
	}
	return &ast.IndexListExpr{X: x, Indices: idx}
}

// funcType builds a Go function type from a Lyth signature with optional
// parameter names.
func (l *fileLowerer) funcType(sig *types.Signature, names []string) *ast.FuncType {
	ft := &ast.FuncType{Params: &ast.FieldList{}}
	for i, p := range sig.Params {
		var te ast.Expr = l.typeExpr(p)
		if sig.Variadic && i == len(sig.Params)-1 {
			if s, ok := p.(*types.Slice); ok {
				te = &ast.Ellipsis{Elt: l.typeExpr(s.Elem)}
			}
		}
		fld := &ast.Field{Type: te}
		if names != nil {
			fld.Names = []*ast.Ident{ast.NewIdent(names[i])}
		}
		ft.Params.List = append(ft.Params.List, fld)
	}
	var results []ast.Expr
	for _, r := range sig.Results {
		results = append(results, l.typeExpr(r))
	}
	if sig.Throws {
		results = append(results, ast.NewIdent("error"))
	}
	if len(results) > 0 {
		ft.Results = &ast.FieldList{}
		for _, r := range results {
			ft.Results.List = append(ft.Results.List, &ast.Field{Type: r})
		}
	}
	return ft
}

func (l *fileLowerer) funDecl(d *syntax.FunDecl) {
	fi := l.info.Funcs[d]
	if fi == nil || fi.Sym == nil || fi.Sym.Sig == nil {
		return
	}
	sym := fi.Sym
	fl := newFnLower(l, sym.Sig, false)
	fl.collectNames(d)
	var recv *ast.FieldList
	var extParam *ast.Field // an extension's receiver, its first parameter
	if fi.Recv != nil && sym.ExtRecv != nil {
		rname := recvParamName(sym.ExtRecv)
		if fl.taken[rname] || fl.reserved[rname] || check.IsGoReserved(rname) {
			rname = "this"
		}
		fl.recvName = rname
		fl.locals[fi.Recv] = rname
		fl.taken[rname] = true
		extParam = &ast.Field{Names: []*ast.Ident{ast.NewIdent(rname)}, Type: l.typeExpr(sym.ExtRecv)}
	} else if fi.Recv != nil {
		rname := strings.ToLower(sym.Recv.Name[:1])
		if fl.taken[rname] || check.IsGoReserved(rname) {
			rname = "this"
		}
		fl.recvName = rname
		fl.locals[fi.Recv] = rname
		fl.taken[rname] = true
		var rt ast.Expr = ast.NewIdent(sym.Recv.GoName)
		if tps := sym.Recv.TypeParams; len(tps) > 0 {
			// func (p Page[T]) ...: the declaration's names, which the
			// lowered types in the method refer to.
			names := make([]ast.Expr, len(tps))
			for i, tp := range tps {
				names[i] = ast.NewIdent(tp.Name)
			}
			if len(names) == 1 {
				rt = &ast.IndexExpr{X: rt, Index: names[0]}
			} else {
				rt = &ast.IndexListExpr{X: rt, Indices: names}
			}
		}
		if sym.PtrRecv {
			rt = &ast.StarExpr{X: rt}
		}
		recv = &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(rname)}, Type: rt}}}
	}
	var names []string
	for _, p := range fi.Params {
		names = append(names, fl.declareLocal(p))
	}
	name := sym.GoName
	if sym.IsMain && sym.Sig.Throws {
		name = l.mainRunName()
	}
	if sym.IsTest && sym.Sig.Throws {
		name = l.freshPkgName(lowerFirst(sym.GoName))
	}
	ft := l.funcType(sym.Sig, names)
	ft.Func = l.pos(d.S)
	if len(sym.Sig.TypeParams) > 0 {
		ft.TypeParams = l.typeParamList(sym.Sig.TypeParams)
	}
	if extParam != nil {
		ft.Params.List = append([]*ast.Field{extParam}, ft.Params.List...)
	}
	fd := &ast.FuncDecl{Doc: docGroup(d.Doc), Recv: recv, Name: ast.NewIdent(name), Type: ft}
	l.mark(fd.Name, d.Name.S)
	l.mark(fd, d.S)
	body := fl.funcBody(d, fi)
	fd.Body = body
	l.decls = append(l.decls, fd)
	if sym.IsMain && sym.Sig.Throws {
		l.decls = append(l.decls, l.mainWrapper(d, name))
	}
	if sym.IsTest && sym.Sig.Throws {
		l.decls = append(l.decls, l.testWrapper(d, sym, name))
	}
}

// freshPkgName returns a package-level name based on base that no
// declaration uses, and reserves it.
func (l *fileLowerer) freshPkgName(base string) string {
	name := base
	for i := 2; l.pkgNames[name]; i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	l.pkgNames[name] = true
	return name
}

// testWrapper generates the entry point go test runs for a throwing test,
// benchmark, or fuzz target. It fails with the returned error:
//
//	func TestCreate(t *testing.T) {
//		if err := testCreate(t); err != nil {
//			t.Fatal(err)
//		}
//	}
//
// The wrapper's statements are attributed to the Lyth declaration, so the
// failure reports the test's declaration line.
func (l *fileLowerer) testWrapper(d *syntax.FunDecl, sym *check.FuncSym, body string) *ast.FuncDecl {
	param := "t"
	switch {
	case strings.HasPrefix(sym.GoName, "Benchmark"):
		param = "b"
	case strings.HasPrefix(sym.GoName, "Fuzz"):
		param = "f"
	}
	call := &ast.CallExpr{Fun: ast.NewIdent(body), Args: []ast.Expr{ast.NewIdent(param)}}
	ifs := &ast.IfStmt{
		Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent("err")}, Tok: token.DEFINE, Rhs: []ast.Expr{call}},
		Cond: &ast.BinaryExpr{X: ast.NewIdent("err"), Op: token.NEQ, Y: ast.NewIdent("nil")},
		Body: &ast.BlockStmt{List: []ast.Stmt{
			&ast.ExprStmt{X: &ast.CallExpr{Fun: sel(param, "Fatal"), Args: []ast.Expr{ast.NewIdent("err")}}},
		}},
	}
	params := &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(param)}, Type: l.typeExpr(sym.Sig.Params[0])}}}
	fd := &ast.FuncDecl{Name: ast.NewIdent(sym.GoName), Type: &ast.FuncType{Func: l.pos(d.S), Params: params},
		Body: &ast.BlockStmt{List: []ast.Stmt{ifs}}}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n != nil {
			l.mark(n, d.Name.S)
		}
		return true
	})
	ast.Inspect(fd.Type, func(n ast.Node) bool {
		if n != nil {
			l.markSynth(n, d.Name.S)
		}
		return true
	})
	l.markSynth(fd, d.Name.S)
	l.markSynth(fd.Name, d.Name.S)
	return fd
}

// mainRunName picks the name of the function holding the body of a
// throwing main.
func (l *fileLowerer) mainRunName() string {
	if l.runName != "" {
		return l.runName
	}
	name := "run"
	for i := 2; l.pkgNames[name]; i++ {
		name = fmt.Sprintf("run%d", i)
	}
	l.runName = name
	return name
}

// mainWrapper generates func main() that reports a returned error.
func (l *fileLowerer) mainWrapper(d *syntax.FunDecl, run string) *ast.FuncDecl {
	fmtPkg := l.useImport("fmt", "fmt")
	osPkg := l.useImport("os", "os")
	errID := ast.NewIdent("err")
	body := &ast.BlockStmt{List: []ast.Stmt{
		&ast.IfStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{errID}, Tok: token.DEFINE, Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent(run)}}},
			Cond: &ast.BinaryExpr{X: ast.NewIdent("err"), Op: token.NEQ, Y: ast.NewIdent("nil")},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ExprStmt{X: &ast.CallExpr{Fun: sel(fmtPkg, "Fprintln"), Args: []ast.Expr{sel(osPkg, "Stderr"), strLit("error:"), ast.NewIdent("err")}}},
				&ast.ExprStmt{X: &ast.CallExpr{Fun: sel(osPkg, "Exit"), Args: []ast.Expr{intLit(1)}}},
			}},
		},
	}}
	fd := &ast.FuncDecl{Name: ast.NewIdent("main"), Type: &ast.FuncType{Func: l.pos(d.S), Params: &ast.FieldList{}}, Body: body}
	l.markSynth(fd, d.Name.S)
	ast.Inspect(body, func(n ast.Node) bool {
		if n != nil {
			l.markSynth(n, d.Name.S)
		}
		return true
	})
	return fd
}

func (l *fileLowerer) propDecl(d *syntax.PropDecl) {
	v, ok := l.pkg.Scope.LookupLocal(d.Name.Name).(*check.VarSym)
	if !ok {
		return
	}
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(v.GoName)}}
	l.mark(spec.Names[0], d.Name.S)
	if d.Value != nil {
		fl := newFnLower(l, &types.Signature{}, false)
		fl.initializer = true
		var stmts []ast.Stmt
		fl.stmts = &stmts
		x := fl.exprAs(d.Value, v.Type)
		if len(stmts) > 0 {
			// Package-level initializers cannot contain statements: wrap them.
			stmts = append(stmts, &ast.ReturnStmt{Results: []ast.Expr{x}})
			x = &ast.CallExpr{Fun: &ast.FuncLit{
				Type: &ast.FuncType{Params: &ast.FieldList{}, Results: &ast.FieldList{List: []*ast.Field{{Type: l.typeExpr(v.Type)}}}},
				Body: &ast.BlockStmt{List: stmts},
			}}
			spec.Type = nil
		} else if needsExplicitType(l.info, d.Value, v.Type) {
			spec.Type = l.typeExpr(v.Type)
		}
		spec.Values = []ast.Expr{x}
	} else {
		spec.Type = l.typeExpr(v.Type)
	}
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.VAR, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
}

func (l *fileLowerer) constDecl(d *syntax.ConstDecl) {
	k, ok := l.pkg.Scope.LookupLocal(d.Name.Name).(*check.ConstSym)
	if !ok || k.Val == nil {
		return
	}
	fl := newFnLower(l, &types.Signature{}, false)
	fl.initializer = true
	var stmts []ast.Stmt
	fl.stmts = &stmts
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(k.GoName)}, Values: []ast.Expr{fl.expr(d.Value)}}
	l.mark(spec.Names[0], d.Name.S)
	if d.Type != nil {
		spec.Type = l.typeExpr(k.Type)
	}
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.CONST, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
}

// needsExplicitType reports whether `x := e` would give x a different Go
// type than the declared Lyth type t.
func needsExplicitType(info *check.Info, e syntax.Expr, t types.Type) bool {
	tv, ok := info.Types[e]
	if !ok {
		return true
	}
	if r, ok := tv.Type.(*types.Result); ok && len(r.Raw) == 1 {
		tv.Type = r.Raw[0]
	}
	want := types.Lower(t)
	if tv.Value != nil {
		// Constants take their default Go type with :=.
		def := gotypes.Default(constKindType(tv.Value))
		return !gotypes.Identical(def, want)
	}
	if conv, ok := info.Implicit[e]; ok && conv != check.ConvNone {
		if conv == check.ConvNilIface {
			return !gotypes.Identical(types.Lower(info.IfaceTargets[e]), want)
		}
		return !gotypes.Identical(types.Lower(convertedType(tv.Type, conv)), want)
	}
	if tv.Type == types.Null {
		return true
	}
	return !gotypes.Identical(types.Lower(tv.Type), want)
}

func convertedType(t types.Type, conv check.ConversionKind) types.Type {
	switch conv {
	case check.ConvBox:
		return &types.Nullable{Elem: t}
	case check.ConvUnbox:
		return types.Base(t)
	}
	return t
}

func sel(pkg, name string) *ast.SelectorExpr {
	return &ast.SelectorExpr{X: ast.NewIdent(pkg), Sel: ast.NewIdent(name)}
}

func strLit(s string) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(s)}
}

func intLit(n int) ast.Expr {
	if n < 0 {
		return &ast.UnaryExpr{Op: token.SUB, X: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(-n)}}
	}
	return &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(n)}
}
