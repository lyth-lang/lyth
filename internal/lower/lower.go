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
	"slices"
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
	// Comments are the comments inside function bodies, which emit places
	// next to the generated code of the Lyth lines around them.
	Comments []BodyComment
}

// BodyComment is a comment inside a function body.
type BodyComment struct {
	Text     string
	Line     int  // the comment's Lyth line
	Trailing bool // it follows code on its line
	// The innermost block holding the comment: its first and last lines,
	// and the statement before the comment (to follow when no code
	// follows the comment in the block).
	BlockStart, BlockEnd int
	After                source.Span
	// Detached marks a block of comments outside function bodies that is
	// no declaration's doc (a copyright header, a section heading): it is
	// kept apart by blank lines, above the next declaration or field.
	Detached bool
	Head     bool // detached and above the package clause
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
			case *syntax.StructDecl, *syntax.InterfaceDecl, *syntax.PropDecl, *syntax.ConstDecl, *syntax.TypeAliasDecl, *syntax.TypeDefDecl:
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
	case *syntax.TypeDefDecl:
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
	imports      map[string]string
	importNames  map[string]string
	usedImports  map[string]bool
	blank        []string
	decls        []ast.Decl
	runName      string
	bodyComments []BodyComment
	recvNames    map[*types.TypeDecl]string
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
	if f.Package != nil {
		file.Doc = l.memberDoc(packageDoc(f), f.Package.S)
		file.Package = l.pos(f.Package.S)
	}
	file.Decls = append(file.Decls, l.decls...)
	parenHeaderLiterals(file)
	flattenElseIf(file)
	l.keepDetachedComments(f)
	return &File{Src: f.Src, GoName: GoFileName(f.Src.Base()), AST: file, Fset: fset, Spans: l.spans, Synthetic: l.synthetic, Build: f.BuildConstraint, Comments: l.bodyComments}
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
	// gofmt sorts an import block by path, blank imports included.
	sort.SliceStable(gd.Specs, func(i, j int) bool {
		return gd.Specs[i].(*ast.ImportSpec).Path.Value < gd.Specs[j].(*ast.ImportSpec).Path.Value
	})
	if len(gd.Specs) == 1 {
		gd.Lparen, gd.Rparen = 0, 0
	}
	return []ast.Decl{gd}
}

// emptyStructType returns struct{}. Braces at one position print on one
// line; without positions go/printer would break the line between them.
func emptyStructType() *ast.StructType {
	return &ast.StructType{Fields: &ast.FieldList{Opening: 1, Closing: 1}}
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

// declTypeExpr lowers the declared type te, resolved as t, keeping the Lyth
// type aliases it names (mws: []Middleware is []Middleware in Go too, not the
// function type), which the resolved type no longer records.
func (l *fileLowerer) declTypeExpr(te syntax.TypeExpr, t types.Type) ast.Expr {
	if te == nil || !l.namesAlias(te) {
		return l.typeExpr(t)
	}
	switch x := te.(type) {
	case *syntax.NamedType:
		ts, _ := l.info.Uses[x.Name].(*check.TypeSym)
		if ts == nil || ts.Alias == nil || ts.Pkg == nil {
			break
		}
		if ts.Pkg.Path == l.pkg.Path {
			return ast.NewIdent(ts.GoName)
		}
		return sel(l.useImport(ts.Pkg.Path, ts.Pkg.Name), ts.GoName)
	case *syntax.NullableType:
		base := types.Base(t)
		if types.IsNilable(base) {
			return l.declTypeExpr(x.Elem, base)
		}
		return &ast.StarExpr{X: l.declTypeExpr(x.Elem, base)}
	case *syntax.PointerType:
		if p, ok := types.Base(t).(*types.Pointer); ok {
			return &ast.StarExpr{X: l.declTypeExpr(x.Elem, p.Elem)}
		}
	case *syntax.SliceType:
		if s, ok := types.Base(t).(*types.Slice); ok {
			return &ast.ArrayType{Elt: l.declTypeExpr(x.Elem, s.Elem)}
		}
	case *syntax.MapType:
		if m, ok := types.Base(t).(*types.Map); ok {
			return &ast.MapType{Key: l.declTypeExpr(x.Key, m.Key), Value: l.declTypeExpr(x.Value, m.Elem)}
		}
	}
	return l.typeExpr(t)
}

// namesAlias reports whether te names a Lyth type alias, outside function
// types (whose parameters keep their resolved types).
func (l *fileLowerer) namesAlias(te syntax.TypeExpr) bool {
	switch x := te.(type) {
	case *syntax.NamedType:
		ts, _ := l.info.Uses[x.Name].(*check.TypeSym)
		return ts != nil && ts.Alias != nil
	case *syntax.NullableType:
		return l.namesAlias(x.Elem)
	case *syntax.PointerType:
		return l.namesAlias(x.Elem)
	case *syntax.SliceType:
		return l.namesAlias(x.Elem)
	case *syntax.MapType:
		return l.namesAlias(x.Key) || l.namesAlias(x.Value)
	}
	return false
}

func (l *fileLowerer) goTypeExpr(t gotypes.Type) ast.Expr {
	switch t := t.(type) {
	case *gotypes.Basic:
		if t.Kind() == gotypes.UnsafePointer {
			return sel(l.useImport("unsafe", "unsafe"), "Pointer")
		}
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
		if t.NumFields() == 0 {
			return emptyStructType()
		}
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
	if _, ok := gt.(*gotypes.TypeParam); ok {
		// A type parameter's underlying type is its constraint, an
		// interface, whose zero value nil is not a value of T.
		return &ast.StarExpr{X: &ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{l.goTypeExpr(gt)}}}
	}
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
	}
	return ast.NewIdent("nil")
}

// ---------------------------------------------------------------------------
// Declarations

// docWithDirectives returns a declaration's doc comment followed by its
// //go: directives, where gofmt puts them: after a // line when there is
// doc text.
func docWithDirectives(doc string, dirs []syntax.Directive) *ast.CommentGroup {
	g := docGroup(doc)
	if len(dirs) == 0 {
		return g
	}
	if g == nil {
		g = &ast.CommentGroup{}
	} else {
		g.List = append(g.List, &ast.Comment{Text: "//"})
	}
	for _, d := range dirs {
		g.List = append(g.List, &ast.Comment{Text: d.Text})
	}
	return g
}

// useEmbed makes the generated file import embed, which Go requires of a
// file with //go:embed directives.
func (l *fileLowerer) useEmbed() {
	if _, ok := l.imports["embed"]; ok {
		l.usedImports["embed"] = true
		return
	}
	if !slices.Contains(l.blank, "embed") {
		l.blank = append(l.blank, "embed")
	}
}

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
	case *syntax.TypeDefDecl:
		l.definedDecl(d)
	}
}

// definedDecl lowers type Name Underlying to the same Go declaration.
func (l *fileLowerer) definedDecl(d *syntax.TypeDefDecl) {
	ts := l.typeSym(d.Name.Name)
	if ts == nil || ts.Decl == nil || ts.Decl.Underlying == nil {
		return
	}
	spec := &ast.TypeSpec{Name: ast.NewIdent(ts.Decl.GoName), Type: l.typeExpr(ts.Decl.Underlying)}
	if len(ts.Decl.TypeParams) > 0 {
		spec.TypeParams = l.typeParamList(ts.Decl.TypeParams)
	}
	l.mark(spec.Name, d.Name.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.TYPE, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
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
	spec := &ast.TypeSpec{Name: ast.NewIdent(ts.Decl.GoName), Type: l.structType(ts.Decl, d)}
	if len(ts.Decl.TypeParams) > 0 {
		spec.TypeParams = l.typeParamList(ts.Decl.TypeParams)
	}
	l.mark(spec.Name, d.Name.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.TYPE, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
	l.conformance(d, ts)
}

// structType lowers the fields of a struct declaration.
func (l *fileLowerer) structType(decl *types.TypeDecl, d *syntax.StructDecl) *ast.StructType {
	st := &ast.StructType{Fields: &ast.FieldList{Opening: l.pos(d.S), Closing: l.endPos(d.S)}}
	for i, f := range decl.Fields {
		fld := &ast.Field{Names: []*ast.Ident{ast.NewIdent(f.GoName)}, Type: l.typeExpr(f.Type)}
		if i < len(d.Fields) && !f.Embedded {
			fld.Type = l.declTypeExpr(d.Fields[i].Type, f.Type)
		}
		if f.Tag != "" {
			fld.Tag = tagLit(f.Tag)
		}
		if i < len(d.Fields) {
			fld.Doc = l.memberDoc(d.Fields[i].Doc, d.Fields[i].S)
			fld.Comment = l.lineComment(d.Fields[i].S)
			fld.Names[0].NamePos = l.pos(d.Fields[i].S)
			l.mark(fld, d.Fields[i].S)
			l.mark(fld.Names[0], f.Span)
		}
		if f.Embedded {
			// Go names an embedded field after its type (ADR 0043).
			fld.Names = nil
		}
		st.Fields.List = append(st.Fields.List, fld)
	}
	return st
}

// conformance lowers `struct T : I` to Go's compile-time assertion
// var _ I = T{}, or var _ I = (*T)(nil) when only *T implements I.
func (l *fileLowerer) conformance(d *syntax.StructDecl, ts *check.TypeSym) {
	confs := l.info.Conforms[d]
	if len(confs) == 0 {
		return
	}
	if len(ts.Decl.TypeParams) > 0 {
		// A generic struct has no Go assertion without type arguments; the
		// checker has verified the declaration.
		l.sealedMarkers(d, ts, confs)
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
	l.sealedMarkers(d, ts, confs)
}

// sealedMarkers gives a member of sealed interfaces their marker methods:
// func (Leaf[T]) isNode() {}.
func (l *fileLowerer) sealedMarkers(d *syntax.StructDecl, ts *check.TypeSym, confs []check.Conformance) {
	var recv ast.Expr = ast.NewIdent(ts.Decl.GoName)
	if tps := ts.Decl.TypeParams; len(tps) > 0 {
		var names []ast.Expr
		for _, tp := range tps {
			names = append(names, ast.NewIdent(tp.Name))
		}
		if len(names) == 1 {
			recv = &ast.IndexExpr{X: recv, Index: names[0]}
		} else {
			recv = &ast.IndexListExpr{X: recv, Indices: names}
		}
	}
	for _, cf := range confs {
		sd := types.SealedOf(cf.Iface)
		if sd == nil {
			continue
		}
		pos := l.endPos(d.S)
		fd := &ast.FuncDecl{
			Recv: &ast.FieldList{List: []*ast.Field{{Type: recv}}},
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
// and a String method that returns the entry's name. A string-backed enum
// lowers to type Plan string with a typed constant per entry. Unless the enum
// declares them, MarshalText and UnmarshalText carry the entries' wire texts
// (ADR 0035); a string-backed enum needs only UnmarshalText.
func (l *fileLowerer) enumDecl(d *syntax.EnumDecl) {
	ts := l.typeSym(d.Name.Name)
	if ts == nil || ts.Decl == nil {
		return
	}
	decl := ts.Decl
	under := "int"
	if decl.Backing != nil {
		under = "string"
	}
	spec := &ast.TypeSpec{Name: ast.NewIdent(decl.GoName), Type: ast.NewIdent(under)}
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
		if i < len(d.Docs) && i < len(d.Entries) && d.Docs[i] != "" {
			// The doc and the name at their Lyth lines, so that go/printer
			// puts the doc above the entry.
			vs.Doc = l.memberDoc(d.Docs[i], d.Entries[i].S)
			vs.Names[0].NamePos = l.pos(d.Entries[i].S)
		}
		if i < len(d.Entries) {
			vs.Names[0].NamePos = l.pos(d.Entries[i].S)
			vs.Comment = l.lineComment(d.Entries[i].S)
		}
		switch {
		case decl.Backing != nil:
			vs.Type, vs.Values = ast.NewIdent(decl.GoName), []ast.Expr{strLit(e.Wire)}
		case i == 0:
			vs.Type, vs.Values = ast.NewIdent(decl.GoName), []ast.Expr{ast.NewIdent("iota")}
		}
		consts.Specs = append(consts.Specs, vs)
	}
	// The entries stay unmapped: a //line directive between two of them
	// (one per line where Lyth has them on one line) would break gofmt's
	// alignment of the block.
	l.markSynth(consts, d.S)
	l.decls = append(l.decls, consts)

	recv := strings.ToLower(decl.GoName[:1])
	method := func(doc string, ptr bool, name string, params, results []*ast.Field, body ...ast.Stmt) {
		var rt ast.Expr = ast.NewIdent(decl.GoName)
		if ptr {
			rt = &ast.StarExpr{X: rt}
		}
		fd := &ast.FuncDecl{
			Doc:  &ast.CommentGroup{List: []*ast.Comment{{Text: "// " + doc}}},
			Recv: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(recv)}, Type: rt}}},
			Name: ast.NewIdent(name),
			Type: &ast.FuncType{Params: &ast.FieldList{List: params}, Results: &ast.FieldList{List: results}},
			Body: &ast.BlockStmt{List: body},
		}
		markTree(l, fd, d.S)
		l.decls = append(l.decls, fd)
	}
	call := func(fun ast.Expr, args ...ast.Expr) *ast.CallExpr { return &ast.CallExpr{Fun: fun, Args: args} }
	sel := func(pkg, name string) ast.Expr {
		return &ast.SelectorExpr{X: ast.NewIdent(pkg), Sel: ast.NewIdent(name)}
	}
	ret := func(xs ...ast.Expr) ast.Stmt { return &ast.ReturnStmt{Results: xs} }
	field := func(name string, t ast.Expr) *ast.Field {
		f := &ast.Field{Type: t}
		if name != "" {
			f.Names = []*ast.Ident{ast.NewIdent(name)}
		}
		return f
	}
	bytesType := func() ast.Expr { return &ast.ArrayType{Elt: ast.NewIdent("byte")} }
	stringResult := []*ast.Field{field("", ast.NewIdent("string"))}

	if decl.Backing != nil {
		method("String returns the value of the "+decl.Name+" entry.", false, "String", nil, stringResult,
			ret(call(ast.NewIdent("string"), ast.NewIdent(recv))))
		if decl.TextMethods {
			// The value switch accepts exactly the declared entries.
			fmtPkg := l.useImport("fmt", "fmt")
			var list []ast.Expr
			for _, e := range decl.Entries {
				list = append(list, ast.NewIdent(e.GoName))
			}
			v := "v"
			if recv == v {
				v = "x"
			}
			sw := &ast.SwitchStmt{
				Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(v)}, Tok: token.DEFINE, Rhs: []ast.Expr{call(ast.NewIdent(decl.GoName), ast.NewIdent("text"))}},
				Tag:  ast.NewIdent(v),
				Body: &ast.BlockStmt{List: []ast.Stmt{&ast.CaseClause{List: list, Body: []ast.Stmt{
					&ast.AssignStmt{Lhs: []ast.Expr{&ast.StarExpr{X: ast.NewIdent(recv)}}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(v)}},
					ret(ast.NewIdent("nil")),
				}}}},
			}
			method("UnmarshalText sets "+recv+" to the "+decl.Name+" entry with the given value.", true, "UnmarshalText",
				[]*ast.Field{field("text", bytesType())}, []*ast.Field{field("", ast.NewIdent("error"))},
				sw, ret(call(sel(fmtPkg, "Errorf"), strLit("invalid "+decl.Name+" %q"), ast.NewIdent("text"))))
		}
		return
	}

	sw := &ast.SwitchStmt{Tag: ast.NewIdent(recv), Body: &ast.BlockStmt{}}
	for _, e := range decl.Entries {
		sw.Body.List = append(sw.Body.List, &ast.CaseClause{List: []ast.Expr{ast.NewIdent(e.GoName)},
			Body: []ast.Stmt{ret(strLit(e.Name))}})
	}
	strconvPkg := l.useImport("strconv", "strconv")
	asInt := call(ast.NewIdent("int"), ast.NewIdent(recv))
	// Color(7) for a value that is not an entry, as Go's stringer prints.
	other := &ast.BinaryExpr{Op: token.ADD,
		X: &ast.BinaryExpr{Op: token.ADD, X: strLit(decl.Name + "("), Y: call(sel(strconvPkg, "Itoa"), asInt)},
		Y: strLit(")")}
	method("String returns the name of the "+decl.Name+" entry.", false, "String", nil, stringResult, sw, ret(other))
	if !decl.TextMethods {
		return
	}

	fmtPkg := l.useImport("fmt", "fmt")
	marshal := &ast.SwitchStmt{Tag: ast.NewIdent(recv), Body: &ast.BlockStmt{}}
	unmarshal := &ast.SwitchStmt{Tag: call(ast.NewIdent("string"), ast.NewIdent("text")), Body: &ast.BlockStmt{}}
	for _, e := range decl.Entries {
		marshal.Body.List = append(marshal.Body.List, &ast.CaseClause{List: []ast.Expr{ast.NewIdent(e.GoName)},
			Body: []ast.Stmt{ret(call(bytesType(), strLit(e.Wire)), ast.NewIdent("nil"))}})
		unmarshal.Body.List = append(unmarshal.Body.List, &ast.CaseClause{List: []ast.Expr{strLit(e.Wire)},
			Body: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{&ast.StarExpr{X: ast.NewIdent(recv)}}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(e.GoName)}}}})
	}
	unmarshal.Body.List = append(unmarshal.Body.List, &ast.CaseClause{
		Body: []ast.Stmt{ret(call(sel(fmtPkg, "Errorf"), strLit("invalid "+decl.Name+" %q"), ast.NewIdent("text")))}})
	method("MarshalText returns the wire name of the "+decl.Name+" entry.", false, "MarshalText",
		nil, []*ast.Field{field("", bytesType()), field("", ast.NewIdent("error"))},
		marshal, ret(ast.NewIdent("nil"), call(sel(fmtPkg, "Errorf"), strLit("invalid "+decl.Name+" %d"), call(ast.NewIdent("int"), ast.NewIdent(recv)))))
	method("UnmarshalText sets "+recv+" to the "+decl.Name+" entry with the given wire name.", true, "UnmarshalText",
		[]*ast.Field{field("text", bytesType())}, []*ast.Field{field("", ast.NewIdent("error"))},
		unmarshal, ret(ast.NewIdent("nil")))
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
			fld.Comment = l.lineComment(d.Methods[i].S)
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
// freeRecvName returns name, or when the method already uses it (a
// parameter p in a method of prefixed), a variant Go code would choose.
func (fl *fnLower) freeRecvName(name string) string {
	free := func(n string) bool { return !fl.taken[n] && !fl.reserved[n] && !check.IsGoReserved(n) }
	if free(name) {
		return name
	}
	for _, n := range []string{name + "v", "recv", "self_"} {
		if free(n) {
			return n
		}
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s%d", name, i); free(n) {
			return n
		}
	}
}

// recvNameFor returns the receiver name of the methods of decl: the first of
// its initial, initial+"v", recv, ... that no method of the package uses for
// something else, so that all of them name the receiver alike (staticcheck's
// ST1016).
func (l *fileLowerer) recvNameFor(decl *types.TypeDecl) string {
	if n, ok := l.recvNames[decl]; ok {
		return n
	}
	base := strings.ToLower(decl.Name[:1])
	var methods []*fnLower
	for d, fi := range l.info.Funcs {
		if fi.Sym != nil && fi.Sym.Recv == decl && fi.Sym.ExtRecv == nil && fi.Sym.Sig != nil {
			fl := newFnLower(l, fi.Sym.Sig, false)
			fl.collectNames(d)
			methods = append(methods, fl)
		}
	}
	name := base
	candidates := []string{base, base + "v", "recv", "self_"}
	for i := 2; i < 10; i++ {
		candidates = append(candidates, fmt.Sprintf("%s%d", base, i))
	}
	for _, c := range candidates {
		free := !check.IsGoReserved(c)
		for _, fl := range methods {
			free = free && !fl.taken[c] && !fl.reserved[c]
		}
		if free {
			name = c
			break
		}
	}
	if l.recvNames == nil {
		l.recvNames = map[*types.TypeDecl]string{}
	}
	l.recvNames[decl] = name
	return name
}

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
// declAliases gives the parameters and results of ft, lowered from sig, the
// type aliases d's declaration names (h: Handler stays h Handler).
func (l *fileLowerer) declAliases(ft *ast.FuncType, sig *types.Signature, d *syntax.FunDecl) {
	if len(d.Params) == len(sig.Params) && len(ft.Params.List) == len(sig.Params) {
		for i, p := range d.Params {
			if p.Type == nil || !l.namesAlias(p.Type) {
				continue
			}
			if sig.Variadic && i == len(sig.Params)-1 {
				if s, ok := sig.Params[i].(*types.Slice); ok {
					ft.Params.List[i].Type = &ast.Ellipsis{Elt: l.declTypeExpr(p.Type, s.Elem)}
				}
				continue
			}
			ft.Params.List[i].Type = l.declTypeExpr(p.Type, sig.Params[i])
		}
	}
	if d.Result != nil && ft.Results != nil && len(d.Result.Types) == len(sig.Results) && len(ft.Results.List) >= len(sig.Results) {
		for i, r := range d.Result.Types {
			if l.namesAlias(r) {
				ft.Results.List[i].Type = l.declTypeExpr(r, sig.Results[i])
			}
		}
	}
}

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
		rname := fl.freeRecvName(recvParamName(sym.ExtRecv))
		fl.recvName, fl.recvVar = rname, fi.Recv
		fl.locals[fi.Recv] = rname
		fl.taken[rname] = true
		extParam = &ast.Field{Names: []*ast.Ident{ast.NewIdent(rname)}, Type: l.typeExpr(sym.ExtRecv)}
	} else if fi.Recv != nil {
		rname := fl.freeRecvName(l.recvNameFor(sym.Recv))
		fl.recvName, fl.recvVar = rname, fi.Recv
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
	l.declAliases(ft, sym.Sig, d)
	if fi.DeferErr && d.Body != nil && ft.Results != nil {
		// Deferred code sets the error result, which Go lets only a named
		// result do: func f() (_ T, err error).
		fl.deferErr = fl.deferResultName(d)
		for i, r := range ft.Results.List {
			r.Names = []*ast.Ident{ast.NewIdent("_")}
			if i == len(ft.Results.List)-1 {
				r.Names[0].Name = fl.deferErr
			}
		}
	}
	if len(sym.Sig.TypeParams) > 0 {
		ft.TypeParams = l.typeParamList(sym.Sig.TypeParams)
	}
	if extParam != nil {
		ft.Params.List = append([]*ast.Field{extParam}, ft.Params.List...)
	}
	fd := &ast.FuncDecl{Doc: docWithDirectives(d.Doc, d.Directives), Recv: recv, Name: ast.NewIdent(name), Type: ft}
	l.mark(fd.Name, d.Name.S)
	l.mark(fd, d.S)
	body := fl.funcBody(d, fi)
	fd.Body = body
	if d.Body != nil {
		l.keepBodyComments(d.Body)
	}
	l.decls = append(l.decls, fd)
	if sym.IsMain && sym.Sig.Throws {
		l.decls = append(l.decls, l.mainWrapper(d, name))
	}
	if sym.IsTest && sym.Sig.Throws {
		l.decls = append(l.decls, l.testWrapper(d, sym, name))
	}
}

// keepBodyComments records the comments inside a function body (an
// example's "// Output:" among them) for emit. Comments inside a local
// struct are left out: its fields carry their own docs.
func (l *fileLowerer) keepBodyComments(b *syntax.Block) {
	var skip []source.Span
	var blocks []*syntax.Block
	syntax.Walk(b, func(n syntax.Node) {
		switch n := n.(type) {
		case *syntax.StructDecl:
			skip = append(skip, n.S)
		case *syntax.Block:
			blocks = append(blocks, n)
		}
	})
	for _, c := range l.src.Comments {
		if c.Span.Start <= b.S.Start || c.Span.End >= b.S.End || slices.ContainsFunc(skip, func(s source.Span) bool { return s.Contains(c.Span) }) {
			continue
		}
		in := b
		for _, bl := range blocks {
			if bl.S.Contains(c.Span) && in.S.Contains(bl.S) {
				in = bl
			}
		}
		bc := BodyComment{Text: c.Text, Line: c.Span.StartPos().Line, Trailing: !l.ownLine(c.Span),
			BlockStart: in.S.StartPos().Line, BlockEnd: in.S.EndPos().Line}
		for _, st := range in.Stmts {
			if st.Span().End <= c.Span.Start {
				bc.After = st.Span()
			}
		}
		l.bodyComments = append(l.bodyComments, bc)
	}
}

// keepDetachedComments records the blocks of // comments outside function
// bodies that a blank line separates from the code below them.
func (l *fileLowerer) keepDetachedComments(f *syntax.File) {
	var funcs []source.Span
	for _, d := range f.Decls {
		if fd, ok := d.(*syntax.FunDecl); ok {
			funcs = append(funcs, fd.S)
		}
	}
	src := f.Src
	var block []syntax.Comment
	flush := func() {
		if len(block) == 0 {
			return
		}
		first, last := block[0].Span.StartPos().Line, block[len(block)-1].Span.EndPos().Line
		if last >= src.LineCount() || src.LineBlank(last+1) {
			var text []string
			for _, c := range block {
				text = append(text, c.Text)
			}
			head := f.Package != nil && block[0].Span.Start < f.Package.S.Start
			l.bodyComments = append(l.bodyComments, BodyComment{Text: strings.Join(text, "\n"), Line: first, Detached: true, Head: head})
		}
		block = nil
	}
	for _, c := range f.Comments {
		if !strings.HasPrefix(c.Text, "//") || strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(c.Text, "//line ") ||
			!l.ownLine(c.Span) || slices.ContainsFunc(funcs, func(s source.Span) bool { return s.Contains(c.Span) }) {
			flush()
			continue
		}
		if len(block) > 0 && c.Span.StartPos().Line != block[len(block)-1].Span.EndPos().Line+1 {
			flush()
		}
		block = append(block, c)
	}
	flush()
}

// packageDoc returns the comment directly above the package clause.
func packageDoc(f *syntax.File) string {
	if f.Package == nil {
		return ""
	}
	var lines []string
	expect := f.Package.S.StartPos().Line - 1
	for i := len(f.Comments) - 1; i >= 0; i-- {
		c := f.Comments[i]
		if c.Span.Start >= f.Package.S.Start {
			continue
		}
		if c.Span.EndPos().Line != expect || !strings.HasPrefix(c.Text, "//") || strings.HasPrefix(c.Text, "//go:") {
			break
		}
		lines = append([]string{c.Text}, lines...)
		expect--
	}
	return strings.Join(lines, "\n")
}

// ownLine reports whether nothing but space precedes span s on its line.
func (l *fileLowerer) ownLine(s source.Span) bool {
	content := l.src.Src.Content
	start := s.Start
	for start > 0 && content[start-1] != '\n' {
		start--
	}
	return strings.TrimSpace(string(content[start:s.Start])) == ""
}

// keepLineComment records for emit the comment that follows the code of a
// package-level declaration on its last line (const max = 10 // per page).
func (l *fileLowerer) keepLineComment(s source.Span) {
	if g := l.lineComment(s); g != nil {
		l.bodyComments = append(l.bodyComments, BodyComment{Text: g.List[0].Text, Line: s.EndPos().Line, Trailing: true})
	}
}

// lineComment returns the comment that follows the code of span s on its
// last line (X: int // pixels), for go/printer's line comment slot.
func (l *fileLowerer) lineComment(s source.Span) *ast.CommentGroup {
	if !s.IsValid() || s.File != l.src.Src {
		return nil
	}
	line := s.EndPos().Line
	for _, c := range l.src.Comments {
		if c.Span.Start >= s.End && c.Span.StartPos().Line == line && strings.HasPrefix(c.Text, "//") {
			return &ast.CommentGroup{List: []*ast.Comment{{Slash: l.pos(c.Span), Text: c.Text}}}
		}
		if c.Span.Start > s.End && c.Span.StartPos().Line > line {
			break
		}
	}
	return nil
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
		fl.collectNames(d.Value) // lambdas in the initializer have locals too
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
	for _, dir := range d.Directives {
		if dir.Name() == "embed" {
			l.useEmbed()
		}
	}
	l.keepLineComment(d.S)
	gd := &ast.GenDecl{Doc: docWithDirectives(d.Doc, d.Directives), Tok: token.VAR, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
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
	l.keepLineComment(d.S)
	gd := &ast.GenDecl{Doc: docGroup(d.Doc), Tok: token.CONST, TokPos: l.pos(d.S), Specs: []ast.Spec{spec}}
	l.mark(gd, d.S)
	l.decls = append(l.decls, gd)
}

// needsExplicitType reports whether `x := e` would give x a different Go
// type than the declared Lyth type t.
func needsExplicitType(info *check.Info, e syntax.Expr, t types.Type) bool {
	if _, ok := syntax.Unparen(e).(*syntax.LambdaExpr); ok {
		if _, named := types.Lower(t).(*gotypes.Named); named {
			return true // a func literal has no name: h := func... is not an http.HandlerFunc
		}
	}
	tv, ok := info.Types[e]
	if !ok {
		return true
	}
	if r, ok := tv.Type.(*types.Result); ok && len(r.Raw) == 1 {
		tv.Type = r.Raw[0]
	}
	want := types.Lower(t)
	if tv.Value != nil && info.Implicit[e] == check.ConvNone {
		// Constants take their default Go type with :=, which depends on
		// how the constant is written (10 is an int even where a float64
		// is declared). A boxed constant, new(float64(10)), has its type.
		nat := sourceConstType(info, e)
		if nat == nil {
			return true
		}
		return !gotypes.Identical(gotypes.Default(nat), want)
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
	case check.ConvBox, check.ConvReuse:
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

// sourceConstType returns the Go type that the constant expression e has as
// written, before any conversion the checker applied: an untyped kind for
// 10 or 2.5, or the type of a typed constant. It returns nil when it cannot
// tell.
func sourceConstType(info *check.Info, e syntax.Expr) gotypes.Type {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.BasicLit:
		switch x.Kind {
		case syntax.INT:
			return gotypes.Typ[gotypes.UntypedInt]
		case syntax.FLOAT:
			return gotypes.Typ[gotypes.UntypedFloat]
		case syntax.RUNE:
			return gotypes.Typ[gotypes.UntypedRune]
		case syntax.STRING, syntax.RAWSTRING:
			return gotypes.Typ[gotypes.UntypedString]
		}
	case *syntax.TemplateLit:
		return gotypes.Typ[gotypes.UntypedString]
	case *syntax.BoolLit:
		return gotypes.Typ[gotypes.UntypedBool]
	case *syntax.CallExpr:
		// int64(5), time.Duration(2), len("abc"): typed constants.
		if ci := info.Calls[x]; ci != nil {
			switch {
			case ci.Kind == check.CallConvert && ci.Target != nil:
				return types.Lower(ci.Target)
			case ci.Kind == check.CallBuiltin && (ci.Builtin == check.BuiltinLen || ci.Builtin == check.BuiltinCap):
				return gotypes.Typ[gotypes.Int]
			}
		}
	case *syntax.UnaryExpr:
		return sourceConstType(info, x.X)
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ, syntax.LAND, syntax.LOR:
			return gotypes.Typ[gotypes.UntypedBool]
		case syntax.SHL, syntax.SHR:
			return sourceConstType(info, x.X)
		}
		a, b := sourceConstType(info, x.X), sourceConstType(info, x.Y)
		if a == nil || b == nil {
			return nil
		}
		ua, ub := isUntypedGo(a), isUntypedGo(b)
		switch {
		case !ua:
			return a
		case !ub:
			return b
		}
		// Both untyped: the later kind of int, rune, float, complex wins.
		if a.(*gotypes.Basic).Kind() > b.(*gotypes.Basic).Kind() {
			return a
		}
		return b
	case *syntax.Ident:
		switch s := info.Uses[x].(type) {
		case *check.ConstSym:
			if s.Type != nil && !types.IsUntyped(s.Type) {
				return types.Lower(s.Type)
			}
			if s.Decl != nil && s.Decl.Type == nil && (s.Pkg == nil || s.Pkg.Info == info) {
				return sourceConstType(info, s.Decl.Value)
			}
			return nil
		}
		if x.Name == "true" || x.Name == "false" {
			return gotypes.Typ[gotypes.UntypedBool]
		}
	case *syntax.SelectorExpr:
		if info.Entries[x] != nil {
			return types.Lower(info.TypeOf(x)) // Color.Red
		}
		if obj, ok := info.Uses[x.Sel].(*check.GoObj); ok {
			if k, ok := obj.Obj.(*gotypes.Const); ok {
				return k.Type()
			}
		}
		if s, ok := info.Uses[x.Sel].(*check.ConstSym); ok && s.Type != nil && !types.IsUntyped(s.Type) {
			return types.Lower(s.Type)
		}
	}
	return nil
}

func isUntypedGo(t gotypes.Type) bool {
	b, ok := t.(*gotypes.Basic)
	return ok && b.Info()&gotypes.IsUntyped != 0
}

// parenHeaderLiterals parenthesizes composite literals of a named type in
// statement headers, where Go would read their { as the body's:
// if p == (P{X: 1}) { ... }.
func parenHeaderLiterals(f *ast.File) {
	var fix func(e ast.Expr) ast.Expr
	fix = func(e ast.Expr) ast.Expr {
		switch x := e.(type) {
		case *ast.CompositeLit:
			switch x.Type.(type) {
			case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
				return &ast.ParenExpr{X: x}
			}
		case *ast.BinaryExpr:
			x.X, x.Y = fix(x.X), fix(x.Y)
		case *ast.UnaryExpr:
			x.X = fix(x.X)
		case *ast.StarExpr:
			x.X = fix(x.X)
		case *ast.SelectorExpr:
			x.X = fix(x.X)
		case *ast.IndexExpr:
			x.X = fix(x.X)
		case *ast.CallExpr:
			x.Fun = fix(x.Fun) // arguments are inside parentheses already
		case *ast.TypeAssertExpr:
			x.X = fix(x.X)
		}
		return e
	}
	fixStmt := func(s ast.Stmt) {
		switch s := s.(type) {
		case *ast.AssignStmt:
			for i := range s.Rhs {
				s.Rhs[i] = fix(s.Rhs[i])
			}
		case *ast.ExprStmt:
			s.X = fix(s.X)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.IfStmt:
			fixStmt(s.Init)
			s.Cond = fix(s.Cond)
		case *ast.ForStmt:
			fixStmt(s.Init)
			if s.Cond != nil {
				s.Cond = fix(s.Cond)
			}
			fixStmt(s.Post)
		case *ast.SwitchStmt:
			fixStmt(s.Init)
			if s.Tag != nil {
				s.Tag = fix(s.Tag)
			}
		case *ast.TypeSwitchStmt:
			fixStmt(s.Init)
			fixStmt(s.Assign)
		case *ast.RangeStmt:
			s.X = fix(s.X)
		}
		return true
	})
}

// flattenElseIf turns else { if ... } into else if ..., as Go is written:
// typed catch chains and nested ifs lower to an else holding one if.
func flattenElseIf(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.IfStmt); ok {
			for {
				b, ok := s.Else.(*ast.BlockStmt)
				if !ok || len(b.List) != 1 {
					break
				}
				inner, ok := b.List[0].(*ast.IfStmt)
				if !ok {
					break
				}
				s.Else = inner
				s = inner
			}
		}
		return true
	})
}
