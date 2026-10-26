package check

import (
	"fmt"
	"go/token"
	gotypes "go/types"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// Importer resolves an import path to a package. It returns an error
// message for packages that cannot be loaded.
type Importer func(path string) (*Package, string)

// Config configures a check.
type Config struct {
	Sink    *diag.Sink
	Import  Importer
	DirName string // directory base name, for the implicit package name
}

// Checker holds the state of checking one package.
type Checker struct {
	// lythDecls caches, by package path, the declarations of other Lyth
	// packages by Go name (nil for Go packages), for lythDecl.
	lythDecls map[string]map[string]*types.TypeDecl
	// inConstraint is set while resolving a type parameter's constraint,
	// where constraint-only interfaces (Comparable, cmp.Ordered) may appear.
	inConstraint bool
	// callee is the function expression of the call being checked: only
	// there may a selector name an extension function.
	callee syntax.Expr
	// selectorX is the bare name left of a selector being checked (log in
	// log.Println), where an import may be meant.
	selectorX syntax.Expr
	conf      *Config
	pkg       *Package
	info      *Info
	sink      *diag.Sink
	universe  *Scope
	pkgScope  *Scope
	fileScope map[*syntax.File]*Scope
	scope     *Scope
	fn        *funcCtx
	shadow    *gotypes.Package
	goNames   map[string]source.Span
	resolving []*VarSym
	preArgs   map[syntax.Expr]*operand // arguments already checked during inference
	aliases   []*TypeSym
	damaged   map[*source.File][]source.Span // syntax.File.Damaged by source file
	misspelt  map[*source.File]bool          // files with syntax errors the parser read past (Go and Kotlin spellings)
	// While the package's types are being defined (resolveTypeDecls), a
	// constraint that names a Lyth type without its definition yet
	// (struct Cache[S: Store[int]], struct Box[S: Plain]) is set once all
	// types are defined.
	defining           map[*types.TypeDecl]bool
	pendingConstraints []func()
	// While declarations are resolved (types, then functions and methods),
	// instances written in them (a field Box[mem], a parameter Cache[T])
	// are validated against their constraints only once every type has its
	// definition and every method is declared: until then mem may not have
	// the methods its constraint asks for.
	declaring     bool
	pendingChecks []func()
}

// funcCtx describes the function or lambda whose body is being checked.
type funcCtx struct {
	sig        *types.Signature
	fallible   bool // `?` and `throw` allowed
	rawTuple   bool // results end in Error (not declared throws)
	isLambda   bool
	infer      bool         // lambda result type is inferred
	inferred   []types.Type // collected return types for inference
	recv       *VarSym
	recvDecl   *types.TypeDecl
	loopDepth  int
	labels     []*syntax.Ident // labels of the enclosing loops, innermost last
	isMain     bool
	name       string
	declSpan   source.Span
	locals     []*VarSym
	throwsSpan source.Span
	noJumps    bool // defer/go blocks
	// deferOf is the function whose error a deferred block's ? and throw
	// set (when it has none yet), and deferStmt that defer statement.
	deferOf   *funcCtx
	deferStmt *syntax.DeferStmt
	// deferErr says a deferred call or block may fail the function: its Go
	// error result is then named, so that the deferred code can set it.
	deferErr bool
	// reassigned holds the names the enclosing function body may assign
	// after declaring them; nil when unknown (nothing is settled then).
	reassigned map[string]bool
}

// Check type-checks the files of one Lyth package.
func Check(conf *Config, pkgPath string, files []*syntax.File) *Package {
	c := &Checker{
		conf:      conf,
		sink:      conf.Sink,
		info:      newInfo(),
		fileScope: map[*syntax.File]*Scope{},
		goNames:   map[string]source.Span{},
	}
	c.pkg = &Package{Path: pkgPath, Lyth: true, Files: files, Info: c.info, Imports: map[string]*Package{}, Extensions: map[string]*FuncSym{}}
	c.pkg.Name = c.packageName(files)
	c.shadow = gotypes.NewPackage(pkgPath, c.pkg.Name)
	c.pkg.Go = c.shadow
	c.universe = universe()
	c.pkgScope = newScope(c.universe, PackageScope)
	c.pkg.Scope = c.pkgScope
	c.scope = c.pkgScope

	for _, f := range files {
		c.fileScope[f] = newScope(c.pkgScope, FileScope)
		if len(f.Damaged) > 0 {
			if c.damaged == nil {
				c.damaged = map[*source.File][]source.Span{}
			}
			c.damaged[f.Src] = f.Damaged
		}
	}
	for _, d := range c.sink.All() {
		if d.Severity == diag.Error && d.Code.IsSyntax() && d.Span.IsValid() {
			if c.misspelt == nil {
				c.misspelt = map[*source.File]bool{}
			}
			c.misspelt[d.Span.File] = true
		}
	}
	c.collect(files)
	for _, f := range files {
		c.resolveImports(f)
	}
	c.declaring = true
	c.resolveTypeDecls()
	c.resolveFuncDecls()
	c.finishEnums()
	c.validatePendingInstances()
	c.checkConformance()
	c.resolveGlobals()
	c.checkBodies()
	c.reportUnusedImports()
	return c.pkg
}

// lift converts a Go type to a Lyth type. A Go package of the module may
// use the types of a Lyth package (a bridge, build/bridge.go); in Go seen
// from Lyth, those are the Lyth declarations again.
func (c *Checker) lift(t gotypes.Type) types.Type {
	return types.Lifter{Decl: c.lythDecl}.Lift(t)
}

func (c *Checker) liftSig(s *gotypes.Signature) *types.Signature {
	return types.Lifter{Decl: c.lythDecl}.LiftSignature(s)
}

// lythDecl returns the Lyth declaration a Go named type of another Lyth
// package stands for, or nil.
func (c *Checker) lythDecl(n *gotypes.Named) *types.TypeDecl {
	obj := n.Origin().Obj()
	pkg := obj.Pkg()
	if pkg == nil || pkg == c.shadow || c.conf.Import == nil {
		return nil
	}
	decls, ok := c.lythDecls[pkg.Path()]
	if !ok {
		if lp, _ := c.conf.Import(pkg.Path()); lp != nil && lp.Lyth {
			decls = map[string]*types.TypeDecl{}
			for _, ts := range lp.Types {
				if ts.Decl != nil {
					decls[ts.Decl.GoName] = ts.Decl
				}
			}
		}
		if c.lythDecls == nil {
			c.lythDecls = map[string]map[string]*types.TypeDecl{}
		}
		c.lythDecls[pkg.Path()] = decls
	}
	return decls[obj.Name()]
}

// errorf reports an error, except inside code the parser recovered from,
// where it would only describe the parser's guess; a detached diagnostic is
// returned then.
func (c *Checker) errorf(code diag.Code, span source.Span, format string, args ...any) *diag.Diagnostic {
	for _, d := range c.damaged[span.File] {
		if span.Start >= d.Start && span.Start <= d.End {
			return &diag.Diagnostic{}
		}
	}
	return c.sink.Errorf(code, span, format, args...)
}

// warnf reports a warning unless the file has syntax errors: whether a name
// is used, for example, depends on code that did not parse, or that the
// parser skipped after reporting a Go or Kotlin spelling (P{X: n}, "$name").
func (c *Checker) warnf(code diag.Code, span source.Span, format string, args ...any) *diag.Diagnostic {
	if len(c.damaged[span.File]) > 0 || c.misspelt[span.File] {
		return &diag.Diagnostic{}
	}
	return c.sink.Warnf(code, span, format, args...)
}

// packageName determines the package name from package clauses, main
// functions, or the directory name.
func (c *Checker) packageName(files []*syntax.File) string {
	name := ""
	var first *syntax.Ident
	for _, f := range files {
		if f.Package == nil {
			continue
		}
		if first == nil {
			first = f.Package
			name = f.Package.Name
		} else if f.Package.Name != name {
			c.errorf(diag.EDuplicate, f.Package.S, "package %s conflicts with package %s declared in %s", f.Package.Name, name, first.S.File.Name).
				WithRelated(first.S, "package %s declared here", name)
		}
	}
	if name != "" {
		return name
	}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*syntax.FunDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
				return "main"
			}
		}
	}
	dir := c.conf.DirName
	if dir == "" {
		return "main"
	}
	dir = strings.ReplaceAll(dir, "-", "_")
	if !isIdent(dir) || syntax.IsKeywordName(dir) {
		if len(files) > 0 {
			c.errorf(diag.EUnexpectedToken, source.Span{File: files[0].Src, Start: 0, End: 0},
				"cannot derive a package name from directory %q; add a package clause", c.conf.DirName)
		}
		return "main"
	}
	return dir
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Go names

var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true, "default": true,
	"defer": true, "else": true, "fallthrough": true, "for": true, "func": true, "go": true,
	"goto": true, "if": true, "import": true, "interface": true, "map": true, "package": true,
	"range": true, "return": true, "select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// IsGoReserved reports whether name is a Go keyword or predeclared identifier.
func IsGoReserved(name string) bool {
	return goKeywords[name] || gotypes.Universe.Lookup(name) != nil
}

// GoName computes the Go name of a Lyth declaration: the Lyth name itself, as
// in Go a name that starts with an uppercase letter is exported. Unexported
// names that are Go keywords or predeclared identifiers get a trailing `_`.
func GoName(lythName string) string {
	if !Exported(lythName) && (IsGoReserved(lythName) || lythName == "init") {
		return lythName + "_"
	}
	return lythName
}

// Exported reports whether a Lyth name is exported (starts with an uppercase
// letter), Go's rule.
func Exported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

func (c *Checker) claimGoName(goName string, span source.Span, lythName string) {
	if old, ok := c.goNames[goName]; ok {
		c.errorf(diag.EDuplicate, span, "%s would be named %s in Go, which is already used", lythName, goName).
			WithRelated(old, "%s declared here", strings.TrimSpace(old.Text()))
		return
	}
	c.goNames[goName] = span
}

// ---------------------------------------------------------------------------
// Collection

func (c *Checker) declare(scope *Scope, sym Symbol, span source.Span) bool {
	if old := scope.Insert(sym); old != nil {
		d := c.errorf(diag.EDuplicate, span, "%s is already declared in this %s", sym.Name(), scopeWord(scope))
		if old.DeclSpan().IsValid() {
			d.WithRelated(old.DeclSpan(), "previous declaration of %s", sym.Name())
		}
		return false
	}
	return true
}

func scopeWord(s *Scope) string {
	switch s.kind {
	case PackageScope:
		return "package"
	case FileScope:
		return "file"
	}
	return "scope"
}

// collect declares all top-level symbols.
func (c *Checker) collect(files []*syntax.File) {
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.StructDecl:
				c.collectType(d.Name, types.StructDecl, d, d.TypeParams)
			case *syntax.InterfaceDecl:
				c.collectType(d.Name, types.InterfaceDecl, d, d.TypeParams)
				if d.Sealed {
					decl := c.pkg.Types[len(c.pkg.Types)-1].Decl
					decl.Sealed = true
					decl.Marker = "is" + exportedName(decl.GoName)
				}
			case *syntax.EnumDecl:
				c.collectType(d.Name, types.EnumDecl, d, nil)
			case *syntax.TypeDefDecl:
				c.collectType(d.Name, types.DefinedDecl, d, d.TypeParams)
			}
		}
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.FunDecl:
				c.funcDirectives(d)
				if d.Recv != nil {
					continue // methods are attached in resolveFuncDecls
				}
				sym := &FuncSym{name: d.Name.Name, Pub: Exported(d.Name.Name), Pkg: c.pkg, Decl: d, Span: d.Name.S}
				sym.GoName = GoName(sym.name)
				if IsTestFile(f) && goTestName(sym.name) {
					// go test finds these by their exported Go names.
					sym.IsTest = true
					sym.GoName = sym.name
				}
				if sym.name == "main" && d.Recv == nil {
					sym.IsMain = true
					sym.GoName = "main"
					c.pkg.HasMain = true
				}
				if sym.name == "init" {
					// Go runs every init function of a package before main; a
					// package may have several, and nothing can refer to one,
					// so they are not declared in the package scope.
					sym.IsInit = true
					sym.GoName = "init"
					c.pkg.Funcs = append(c.pkg.Funcs, sym)
					continue
				}
				if c.declare(c.pkgScope, sym, d.Name.S) {
					c.claimGoName(sym.GoName, d.Name.S, sym.name)
				}
				c.pkg.Funcs = append(c.pkg.Funcs, sym)
			case *syntax.PropDecl:
				sym := &VarSym{name: d.Name.Name, Mutable: d.Mutable, Kind: GlobalVar, Span: d.Name.S, Pub: Exported(d.Name.Name), Pkg: c.pkg, Decl: d}
				sym.GoName = GoName(sym.name)
				if c.declare(c.pkgScope, sym, d.Name.S) {
					c.claimGoName(sym.GoName, d.Name.S, sym.name)
				}
				c.pkg.Globals = append(c.pkg.Globals, sym)
			case *syntax.TypeAliasDecl:
				sym := &TypeSym{name: d.Name.Name, Alias: d, AliasPub: Exported(d.Name.Name), Span: d.Name.S, Pkg: c.pkg, syn: d}
				sym.GoName = GoName(d.Name.Name)
				if c.declare(c.pkgScope, sym, d.Name.S) {
					c.claimGoName(sym.GoName, d.Name.S, sym.name)
				}
				c.aliases = append(c.aliases, sym)
			case *syntax.ConstDecl:
				sym := &ConstSym{name: d.Name.Name, Span: d.Name.S, Pub: Exported(d.Name.Name), Pkg: c.pkg, Decl: d}
				sym.GoName = GoName(sym.name)
				if c.declare(c.pkgScope, sym, d.Name.S) {
					c.claimGoName(sym.GoName, d.Name.S, sym.name)
				}
				c.pkg.Globals = append(c.pkg.Globals, sym)
			}
		}
	}
}

func (c *Checker) collectType(name *syntax.Ident, kind types.DeclKind, syn syntax.Decl, tps []*syntax.TypeParam) {
	decl := &types.TypeDecl{Name: name.Name, Pub: Exported(name.Name), Kind: kind, PkgPath: c.pkg.Path, PkgName: c.pkg.Name, Span: name.S}
	decl.GoName = GoName(name.Name)
	tn := gotypes.NewTypeName(token.NoPos, c.shadow, decl.GoName, nil)
	decl.Shadow = gotypes.NewNamed(tn, nil, nil)
	if len(tps) > 0 {
		// The parameters exist from the start so that the shadow type is
		// generic before anything instantiates it; their constraints are
		// resolved with the other types (resolveTypeDecls).
		gps := make([]*gotypes.TypeParam, len(tps))
		for i, tp := range tps {
			gps[i] = gotypes.NewTypeParam(gotypes.NewTypeName(token.NoPos, c.shadow, tp.Name.Name, nil), nil)
			decl.TypeParams = append(decl.TypeParams, &types.TypeParam{Go: gps[i], Name: tp.Name.Name, Constraint: types.Any})
		}
		decl.Shadow.SetTypeParams(gps)
	}
	c.shadow.Scope().Insert(tn)
	sym := &TypeSym{name: name.Name, Type: &types.Named{Decl: decl}, Decl: decl, Span: name.S, Pkg: c.pkg, syn: syn}
	if c.declare(c.pkgScope, sym, name.S) {
		c.claimGoName(decl.GoName, name.S, name.Name)
	}
	c.pkg.Types = append(c.pkg.Types, sym)
}

// ---------------------------------------------------------------------------
// Imports

func (c *Checker) resolveImports(f *syntax.File) {
	fs := c.fileScope[f]
	for _, imp := range f.Imports {
		if imp.Path == "" {
			continue
		}
		pkg, errMsg := c.conf.Import(imp.Path)
		if pkg == nil {
			d := c.errorf(diag.EImport, imp.PathSpan, "cannot import %q", imp.Path)
			if errMsg != "" {
				d.WithNote("%s", errMsg)
				if strings.Contains(errMsg, "no required module provides package") || strings.Contains(errMsg, "go get") {
					d.WithHint("add the module with: go get %s", imp.Path)
				}
			}
			// Declare a placeholder so uses do not cascade into "undefined".
			name := path.Base(imp.Path)
			if imp.Alias != nil {
				name = imp.Alias.Name
			}
			fs.Insert(&PkgSym{name: name, Import: imp, Used: true})
			continue
		}
		c.pkg.Imports[imp.Path] = pkg
		name := pkg.Name
		if imp.Alias != nil {
			name = imp.Alias.Name
			if name == "_" {
				continue
			}
		}
		sym := &PkgSym{name: name, Pkg: pkg, Import: imp}
		if old := fs.Insert(sym); old != nil {
			c.errorf(diag.EDuplicate, imp.S, "%s is imported twice", name).WithHint("rename one import: name \"path\"")
		}
		if other := c.pkgScope.LookupLocal(name); other != nil {
			c.errorf(diag.EDuplicate, imp.S, "import name %s conflicts with the declaration of %s", name, name).
				WithRelated(other.DeclSpan(), "%s declared here", name).
				WithHint("rename the import: name \"path\"")
		}
	}
}

func (c *Checker) reportUnusedImports() {
	for _, f := range c.pkg.Files {
		for _, sym := range c.fileScope[f].Symbols() {
			if p, ok := sym.(*PkgSym); ok && !p.Used && p.Pkg != nil {
				c.warnf(diag.WUnusedImport, p.Import.PathSpan, "%q imported and not used", p.Import.Path)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Universe

var builtinNames = map[string]BuiltinKind{
	"len": BuiltinLen, "cap": BuiltinCap, "append": BuiltinAppend, "copy": BuiltinCopy,
	"delete": BuiltinDelete, "make": BuiltinMake, "new": BuiltinNew, "panic": BuiltinPanic,
	"recover": BuiltinRecover, "min": BuiltinMin, "max": BuiltinMax, "clear": BuiltinClear,
	"close": BuiltinClose, "print": BuiltinPrint, "println": BuiltinPrintln,
}

func universe() *Scope {
	u := newScope(nil, UniverseScope)
	for _, b := range types.BasicTypes {
		u.Insert(&TypeSym{name: b.Name, Type: b})
	}
	u.Insert(&TypeSym{name: "any", Type: types.Any})
	u.Insert(&TypeSym{name: "error", Type: types.Error})
	u.Insert(&TypeSym{name: "comparable", Type: types.Comparable})
	u.Insert(&TypeSym{name: "unit", Type: types.Unit})
	// Old and Kotlin spellings of collection types, reported with Go's.
	for _, g := range []string{"List", "MutableList", "ArrayList", "Map", "HashMap", "Chan", "SendChan", "ReceiveChan"} {
		u.Insert(&TypeSym{name: g, Generic: g})
	}
	names := make([]string, 0, len(builtinNames))
	for n := range builtinNames {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		u.Insert(&BuiltinSym{name: n, Kind: builtinNames[n]})
	}
	return u
}

// lookup resolves a name from the current scope. Receiver members are not
// in scope: they are reached through self (ADR 0038).
func (c *Checker) lookup(name string) Symbol {
	for s := c.scope; s != nil; s = s.parent {
		if sym := s.syms[name]; sym != nil {
			return sym
		}
	}
	return nil
}

// lookupSkippingMembers is lookup; the name remains from when members were
// in scope by name.
func (c *Checker) lookupSkippingMembers(name string) Symbol { return c.lookup(name) }

// implicitMember returns the receiver member a bare name meant before ADR
// 0038, when members were in scope at the method's own level (inside a
// lambda or a go/defer block, the enclosing locals and parameters came
// first). Such a name is an error whose fix writes self., even where it now
// resolves to something else, so no meaning changes silently.
func (c *Checker) implicitMember(name string) *MemberSym {
	for s := c.scope; s != nil; s = s.parent {
		if s.syms[name] != nil {
			return nil
		}
		if s.kind == FuncScope && s.fn != nil && !s.fn.isLambda && s.fn.recv != nil && s.fn.recvDecl != nil {
			return c.receiverMember(s.fn, name)
		}
	}
	return nil
}

// callsOther reports whether id, the callee of a call, names something in
// scope that a field of the receiver shares its name with: Config(Name = "x")
// constructs the type Config, and max(a, b) calls the builtin, in a method of
// a struct with a field Config or max. Calling a field that is not a function
// never compiled, so the member cannot have been meant.
func (c *Checker) callsOther(id *syntax.Ident, m *MemberSym) bool {
	if c.callee != syntax.Expr(id) || m.Field == nil || signatureOf(m.Field.Type) != nil {
		return false
	}
	return c.lookup(id.Name) != nil
}

// otherMeaning describes what a bare name that meant a member before ADR
// 0038 means now, when that meaning is also valid where the name stands: a
// package-level variable, constant, or function anywhere, and a builtin, a
// type, or an import where it is called or selected from. It is "" when the
// name can only have meant the member.
func (c *Checker) otherMeaning(id *syntax.Ident) string {
	switch s := c.lookup(id.Name).(type) {
	case *VarSym:
		return "the package-level variable " + id.Name
	case *ConstSym:
		return "the constant " + id.Name
	case *FuncSym:
		return "the function " + id.Name
	case *BuiltinSym:
		if c.callee == syntax.Expr(id) {
			return "the builtin " + id.Name
		}
	case *TypeSym:
		if c.callee == syntax.Expr(id) {
			return "the type " + id.Name
		}
	case *PkgSym:
		if c.selectorX == syntax.Expr(id) && s.Pkg != nil {
			return "the import " + id.Name
		}
	}
	return ""
}

// memberHas reports whether the receiver member m has a field or method
// named sel; a method value has none.
func (c *Checker) memberHas(m *MemberSym, sel *syntax.Ident) bool {
	if m.Field == nil {
		return false
	}
	t := types.Base(m.Field.Type)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Base(p.Elem)
	}
	return c.quietMember(t, sel.Name, &syntax.Ident{Name: sel.Name, S: sel.S}) != nil
}

// bareMember reports a bare name that means a receiver member (ADR 0038).
//
// The edit is a fix (lyth fix applies it) only where the name cannot mean
// anything else: code written before ADR 0038 meant the member, but code
// written since may mean the package-level name, so there the edit is a hint.
// It reports whether the name is ambiguous, which the caller treats as an
// invalid operand: checking on with either meaning would add errors the
// author's choice may remove.
func (c *Checker) bareMember(id *syntax.Ident, m *MemberSym) (ambiguous bool) {
	kind := "field"
	if m.Method != nil {
		kind = "method"
	}
	if other := c.otherMeaning(id); other != "" {
		c.errorf(diag.EUndefined, id.S, "%s is a %s of self and also names %s; write self.%s for the %s", id.Name, kind, other, id.Name, kind).
			WithEdit(id.S, "self."+id.Name, "if the %s is meant: self.%s", kind, id.Name).
			WithNote("before ADR 0038 a bare %s meant the %s; lyth fix leaves the choice to you because it may mean %s", id.Name, kind, other)
		return true
	}
	c.errorf(diag.EUndefined, id.S, "%s is a %s of self; write self.%s", id.Name, kind, id.Name).
		WithFix(id.S, "self."+id.Name, "members are reached through self: self.%s", id.Name)
	return false
}

func (c *Checker) receiverMember(fn *funcCtx, name string) *MemberSym {
	d := fn.recvDecl
	if f := d.LookupField(name); f != nil {
		return &MemberSym{Recv: fn.recv, Field: f, GoName: f.GoName, Type: f.Type}
	}
	if m := d.LookupMethod(name); m != nil {
		if fs, ok := m.Sym.(*FuncSym); ok {
			c.ensureInferred(fs)
		}
		return &MemberSym{Recv: fn.recv, Method: m, GoName: m.GoName, Type: m.Sig}
	}
	return nil
}

// suggest returns the most similar visible name, for "did you mean" hints.
func (c *Checker) suggest(name string) string {
	best, bestDist := "", suggestLimit(name)
	consider := func(n string) {
		if n == name || n == "_" {
			return
		}
		if d := editDistance(strings.ToLower(n), strings.ToLower(name)); d < bestDist {
			best, bestDist = n, d
		}
	}
	for s := c.scope; s != nil; s = s.parent {
		for n, sym := range s.syms {
			if ts, ok := sym.(*TypeSym); ok && ts.Generic != "" {
				continue // List, Map: old spellings, never a suggestion
			}
			consider(n)
		}
		if s.kind == FuncScope && s.fn != nil && s.fn.recvDecl != nil {
			for _, f := range s.fn.recvDecl.Fields {
				consider(f.Name)
			}
			for _, m := range s.fn.recvDecl.Methods {
				consider(m.Name)
			}
		}
	}
	return best
}

// suggestLimit is the edit distance below which a name is suggested for
// name: one edit for short names (foo is not bool), two for longer ones.
func suggestLimit(name string) int {
	if len(name) <= 4 {
		return 2
	}
	return 3
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// IsTestFile reports whether f is a test file, compiled only by lyth test.
func IsTestFile(f *syntax.File) bool {
	return f.Src != nil && strings.HasSuffix(f.Src.Base(), "_test.lyth")
}

// goTestName reports whether go test runs a function with this name:
// Test, Benchmark, Fuzz, or Example followed by nothing or by a character
// that is not a lower-case letter.
func goTestName(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			r, _ := utf8.DecodeRuneInString(rest)
			return rest == "" || !unicode.IsLower(r)
		}
	}
	return false
}
