package lsp

import (
	"go/token"
	gotypes "go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// unit is what is compiled to analyze a file: its package (with the Lyth
// packages it imports), or the whole module for references and renames.
type unit struct {
	key     string
	workDir string
	target  build.Target
}

func unitFor(path string, wholeModule bool) unit {
	dir := filepath.Dir(path)
	if mod, err := build.FindModule(dir); err == nil {
		if wholeModule {
			return unit{key: "module:" + mod.Root, workDir: mod.Root}
		}
		return unit{key: "package:" + dir, workDir: dir, target: build.Target{File: path}}
	}
	// A file outside any module is compiled on its own.
	return unit{key: "file:" + path, workDir: dir, target: build.Target{File: path}}
}

// analysis is the result of compiling a unit.
type analysis struct {
	gen    int
	sess   *build.Session
	err    error // a problem that is not about the code (toolchain, I/O)
	files  map[string]*fileData
	occ    map[string][]*occurrence // by file path, sorted by offset
	defs   map[any][]*occurrence    // declarations by key
	goFset *token.FileSet
}

// fileData is one source file of an analysis.
type fileData struct {
	path   string
	src    *source.File
	syn    *syntax.File
	pkg    *build.Package
	info   *check.Info // nil when the package was not checked
	idx    *lineIndex
	goCode []byte // the generated Go, when the package compiled without errors
}

// occurrence is an identifier that names a declaration, identified by key.
type occurrence struct {
	file       *fileData
	start, end int
	key        any
	def        bool
	id         *syntax.Ident
}

// compile analyzes u with the given editor contents and writes nothing.
func compile(u unit, overlay map[string][]byte) *analysis {
	cfg := &build.Config{WorkDir: u.workDir, Tests: true, NoWrite: true, Overlay: overlay, Stdout: io.Discard, Stderr: io.Discard}
	sess, err := build.Compile(cfg, u.target)
	a := &analysis{sess: sess, err: err, files: map[string]*fileData{}, occ: map[string][]*occurrence{}, defs: map[any][]*occurrence{}}
	if sess == nil {
		return a
	}
	a.goFset = sess.GoFset()
	for _, p := range sess.Packages {
		gen := map[string][]byte{}
		for _, g := range p.Generated {
			gen[g.Name] = g.Code
		}
		for i, f := range p.Files {
			fd := &fileData{path: f.Path, src: f, syn: p.Syntax[i], pkg: p, idx: newLineIndex(f.Content), goCode: gen[generatedName(f.Path)]}
			if p.Checked != nil {
				fd.info = p.Checked.Info
			}
			a.files[f.Path] = fd
		}
	}
	a.index()
	return a
}

// generatedName is the name of the Go file generated for a .lyth file.
func generatedName(path string) string {
	base := filepath.Base(path)
	if stem, ok := strings.CutSuffix(base, "_test.lyth"); ok {
		return stem + ".lyth_test.go"
	}
	return base + ".go"
}

// index records every identifier that names a declaration.
func (a *analysis) index() {
	add := func(id *syntax.Ident, k any, def bool) {
		if id == nil || k == nil || !id.S.IsValid() || id.S.Len() == 0 {
			return
		}
		fd := a.files[id.S.File.Path]
		if fd == nil {
			return
		}
		o := &occurrence{file: fd, start: id.S.Start, end: id.S.End, key: k, def: def, id: id}
		a.occ[fd.path] = append(a.occ[fd.path], o)
		if def {
			a.defs[k] = append(a.defs[k], o)
		}
	}
	seen := map[*check.Info]bool{}
	for _, fd := range a.files {
		info := fd.info
		if info == nil || seen[info] {
			continue
		}
		seen[info] = true
		for id, sym := range info.Defs {
			add(id, key(sym), true)
		}
		for id, sym := range info.Uses {
			add(id, key(sym), false)
		}
		for sel, s := range info.Selections {
			if s.Kind == check.SelExtension {
				if ext, ok := s.Obj.(*check.ExtCall); ok {
					add(sel.Sel, key(ext.Func), false)
				}
				continue
			}
			add(sel.Sel, key(s.Obj), false)
		}
		for e, entry := range info.Entries {
			switch x := e.(type) {
			case *syntax.SelectorExpr:
				add(x.Sel, entry, false)
			case *syntax.Ident:
				add(x, entry, false)
			}
		}
		for call, ci := range info.Calls {
			a.namedArgs(call, ci, info, add)
		}
	}
	// Declarations: the checker keeps top-level names in the package scope,
	// and fields, interface methods, enum entries, and methods on the types.
	for _, fd := range a.files {
		if fd.info == nil || fd.pkg.Checked == nil {
			continue
		}
		for _, d := range fd.syn.Decls {
			name := declName(d)
			if name == nil {
				continue
			}
			sym := topLevel(fd.pkg.Checked, d, name)
			if sym == nil {
				continue
			}
			if sp := declSpan(key(sym)); !sp.IsValid() || sp.File.Path != fd.path || sp.Start > name.S.Start || name.S.End > sp.End {
				continue
			}
			add(name, key(sym), true)
			td, _ := key(sym).(*types.TypeDecl)
			switch d := d.(type) {
			case *syntax.StructDecl:
				if td != nil {
					for _, f := range d.Fields {
						if tf := td.LookupField(f.Name.Name); tf != nil {
							add(f.Name, tf, true)
						}
					}
				}
			case *syntax.InterfaceDecl:
				if td != nil {
					for _, m := range d.Methods {
						for _, tm := range td.Methods {
							if tm.Name == m.Name.Name {
								add(m.Name, key(tm), true)
							}
						}
					}
				}
			case *syntax.EnumDecl:
				if td != nil {
					for _, e := range d.Entries {
						for _, te := range td.Entries {
							if te.Name == e.Name {
								add(e, te, true)
							}
						}
					}
				}
			case *syntax.FunDecl:
				if f, ok := sym.(*check.FuncSym); ok && d.Recv != nil && d.Recv.Name != nil && f.Recv != nil {
					add(d.Recv.Name, f.Recv, false)
				}
			}
		}
	}
	for _, list := range a.occ {
		sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })
	}
}

// namedArgs records named arguments as uses of the field or parameter they
// name: User(Name = "a"), connect("db", timeout = t).
func (a *analysis) namedArgs(call *syntax.CallExpr, ci *check.CallInfo, info *check.Info, add func(*syntax.Ident, any, bool)) {
	named := false
	for _, arg := range call.Args {
		named = named || arg.Name != nil
	}
	if !named {
		return
	}
	if ci.Kind == check.CallConstruct {
		n, ok := types.Base(ci.Target).(*types.Named)
		if !ok || n.Decl == nil {
			return
		}
		for _, arg := range call.Args {
			if arg.Name != nil {
				if f := n.Decl.LookupField(arg.Name.Name); f != nil {
					add(arg.Name, f, false)
				}
			}
		}
		return
	}
	var fn *check.FuncSym
	switch f := unparen(call.Fun).(type) {
	case *syntax.Ident:
		fn, _ = info.Uses[f].(*check.FuncSym)
	case *syntax.SelectorExpr:
		if s := info.Selections[f]; s != nil {
			switch o := s.Obj.(type) {
			case *check.FuncSym:
				fn = o
			case *types.Method:
				fn, _ = o.Sym.(*check.FuncSym)
			}
		}
	}
	if fn == nil || fn.Decl == nil || fn.Pkg == nil || fn.Pkg.Info == nil {
		return
	}
	for _, arg := range call.Args {
		if arg.Name == nil {
			continue
		}
		for _, p := range fn.Decl.Params {
			if p.Name != nil && p.Name.Name == arg.Name.Name {
				add(arg.Name, key(fn.Pkg.Info.Defs[p.Name]), false)
			}
		}
	}
}

func unparen(e syntax.Expr) syntax.Expr {
	return syntax.Unparen(e)
}

// topLevel returns the symbol a top-level declaration declares: from the
// package scope, or for a method from its receiver type, or for an
// extension function from the package's extensions.
func topLevel(p *check.Package, d syntax.Decl, name *syntax.Ident) any {
	f, ok := d.(*syntax.FunDecl)
	if !ok || f.Recv == nil {
		if p.Scope == nil {
			return nil
		}
		return p.Scope.LookupLocal(name.Name)
	}
	if ext := p.Extensions[name.Name]; ext != nil && ext.Span.Start == name.S.Start {
		return ext
	}
	if f.Recv.Name == nil || p.Scope == nil {
		return nil
	}
	ts, ok := p.Scope.LookupLocal(f.Recv.Name.Name).(*check.TypeSym)
	if !ok || ts.Decl == nil {
		return nil
	}
	for _, m := range ts.Decl.Methods {
		if m.Name == name.Name && m.Span.Start == name.S.Start {
			return key(m)
		}
	}
	return nil
}

// key identifies a declaration across the ways it is reached: a type's
// symbol and its Named type, a method's symbol and its type entry, a Go
// object and its generic origin.
func key(x any) any {
	switch v := x.(type) {
	case nil:
		return nil
	case *check.TypeSym:
		if v.Decl != nil {
			return v.Decl
		}
		return v
	case *check.GoObj:
		return key(v.Obj)
	case *check.MemberSym:
		// A field or method of the receiver used by bare name in a method.
		if v.Field != nil {
			return v.Field
		}
		if v.Method != nil {
			return key(v.Method)
		}
	case *types.Method:
		if f, ok := v.Sym.(*check.FuncSym); ok && f != nil {
			return f
		}
		return v
	case *types.Named:
		if v.Decl != nil {
			return v.Decl
		}
		if v.Go != nil {
			return v.Go.Obj()
		}
	case *gotypes.Func:
		return v.Origin()
	case *gotypes.Var:
		return v.Origin()
	case *gotypes.TypeName:
		if n, ok := v.Type().(*gotypes.Named); ok {
			return n.Origin().Obj()
		}
	}
	return x
}

// at returns the occurrence at byte offset off in a file, if any.
func (a *analysis) at(path string, off int) *occurrence {
	for _, o := range a.occ[path] {
		if o.start <= off && off <= o.end {
			return o
		}
		if o.start > off {
			break
		}
	}
	return nil
}

// refs returns every occurrence of k.
func (a *analysis) refs(k any) []*occurrence {
	var out []*occurrence
	for _, list := range a.occ {
		for _, o := range list {
			if o.key == k {
				out = append(out, o)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file.path != out[j].file.path {
			return out[i].file.path < out[j].file.path
		}
		return out[i].start < out[j].start
	})
	return out
}

// declSpan returns where k is declared in Lyth source.
func declSpan(k any) source.Span {
	switch v := k.(type) {
	case check.Symbol:
		return v.DeclSpan()
	case *types.TypeDecl:
		return v.Span
	case *types.Field:
		return v.Span
	case *types.Method:
		return v.Span
	case *types.EnumEntry:
		return v.Span
	}
	return source.Span{}
}

// goPosition returns the file, line, and column (1-based, in bytes) of a Go
// declaration, from the export data's positions.
func (a *analysis) goPosition(obj gotypes.Object) (string, int, int, bool) {
	if a.goFset == nil || !obj.Pos().IsValid() {
		return "", 0, 0, false
	}
	p := a.goFset.Position(obj.Pos())
	name := p.Filename
	if rest, ok := strings.CutPrefix(name, "$GOROOT"); ok {
		name = filepath.Join(goroot(), filepath.FromSlash(rest))
	}
	if name == "" || p.Line == 0 {
		return "", 0, 0, false
	}
	if _, err := os.Stat(name); err != nil {
		return "", 0, 0, false
	}
	return name, p.Line, p.Column, true
}
