package check

import (
	"fmt"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// scopeFor returns the file scope for a span's file.
func (c *Checker) scopeFor(span source.Span) *Scope {
	for f, s := range c.fileScope {
		if f.Src == span.File {
			return s
		}
	}
	return c.pkgScope
}

// withScope runs fn with c.scope set to s.
func (c *Checker) withScope(s *Scope, fn func()) {
	save := c.scope
	c.scope = s
	fn()
	c.scope = save
}

// ---------------------------------------------------------------------------
// Type declarations

// aliasType resolves a typealias on first use.
func (c *Checker) aliasType(ts *TypeSym) types.Type {
	if ts.Type != nil {
		return ts.Type
	}
	if ts.aliasBusy {
		c.errorf(diag.EInvalidUse, ts.Alias.Name.S, "type alias %s refers to itself", ts.name)
		ts.Type = types.Invalid
		return ts.Type
	}
	ts.aliasBusy = true
	c.withScope(c.scopeFor(ts.Alias.S), func() { ts.Type = c.resolveType(ts.Alias.Type) })
	ts.aliasBusy = false
	if ts.Type == types.Unit {
		c.errorf(diag.EInvalidUse, ts.Alias.Type.Span(), "unit cannot be aliased")
		ts.Type = types.Invalid
	}
	return ts.Type
}

func (c *Checker) resolveTypeDecls() {
	c.defining = map[*types.TypeDecl]bool{}
	for _, ts := range c.pkg.Types {
		if ts.Decl != nil {
			c.defining[ts.Decl] = true
		}
	}
	defer c.finishPending()
	// Type parameters first: aliases, fields, and signatures instantiate
	// generic types, which needs the constraints.
	for _, ts := range c.pkg.Types {
		var tps []*syntax.TypeParam
		var span source.Span
		switch d := ts.syn.(type) {
		case *syntax.StructDecl:
			tps, span = d.TypeParams, d.S
		case *syntax.InterfaceDecl:
			tps, span = d.TypeParams, d.S
		}
		if len(tps) > 0 && ts.Decl != nil {
			ts.tscope = c.typeParamScope(tps, ts.Decl.TypeParams, c.scopeFor(span))
		}
	}
	for _, a := range c.aliases {
		c.aliasType(a)
	}
	// Fields and interface methods first; shadow underlying types after, so
	// that recursive references through pointers see the shadow objects.
	for _, ts := range c.pkg.Types {
		scopeOf := func(span source.Span) *Scope {
			if ts.tscope != nil {
				return ts.tscope
			}
			return c.scopeFor(span)
		}
		switch d := ts.syn.(type) {
		case *syntax.StructDecl:
			c.withScope(scopeOf(d.S), func() { c.resolveStruct(ts.Decl, d) })
		case *syntax.InterfaceDecl:
			c.withScope(scopeOf(d.S), func() { c.resolveInterface(ts.Decl, d) })
		case *syntax.EnumDecl:
			c.resolveEnum(ts.Decl, d)
		}
	}
	c.checkEmbedCycles()
	var ifaces []*gotypes.Interface
	for _, ts := range c.pkg.Types {
		decl := ts.Decl
		switch decl.Kind {
		case types.StructDecl:
			fields := make([]*gotypes.Var, len(decl.Fields))
			tags := make([]string, len(decl.Fields))
			for i, f := range decl.Fields {
				fields[i] = gotypes.NewField(token.NoPos, c.shadow, f.GoName, types.Lower(f.Type), false)
				tags[i] = f.Tag
			}
			decl.Shadow.SetUnderlying(gotypes.NewStruct(fields, tags))
		case types.InterfaceDecl:
			var funcs []*gotypes.Func
			for _, m := range decl.Methods {
				funcs = append(funcs, gotypes.NewFunc(token.NoPos, c.shadow, m.GoName, types.LowerSignature(m.Sig, nil)))
			}
			if decl.Sealed {
				funcs = append(funcs, gotypes.NewFunc(token.NoPos, c.shadow, decl.Marker, gotypes.NewSignatureType(nil, nil, nil, nil, nil, false)))
			}
			var embeds []gotypes.Type
			for _, e := range decl.Embeds {
				// An embed that failed to resolve was reported already.
				if g := types.Lower(e); g != nil && e != types.Invalid {
					embeds = append(embeds, g)
				}
			}
			iface := gotypes.NewInterfaceType(funcs, embeds)
			decl.Shadow.SetUnderlying(iface)
			ifaces = append(ifaces, iface)
		case types.EnumDecl:
			decl.Shadow.SetUnderlying(gotypes.Typ[gotypes.Int])
			recv := gotypes.NewVar(token.NoPos, c.shadow, "", decl.Shadow)
			for _, m := range decl.Methods {
				decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, m.GoName, types.LowerSignature(m.Sig, recv)))
			}
		}
	}
	// An interface's method set includes its embedded interfaces', which may
	// be declared later in the package: complete them once all are defined.
	for _, iface := range ifaces {
		iface.Complete()
	}
}

// finishPending sets the constraints that waited for the package's types
// to be defined.
func (c *Checker) finishPending() {
	c.defining = nil
	for _, set := range c.pendingConstraints {
		set()
	}
	c.pendingConstraints = nil
}

// validatePendingInstances validates the instances written in
// declarations, now that every type is defined and every method declared.
func (c *Checker) validatePendingInstances() {
	c.declaring = false
	pending := c.pendingChecks
	c.pendingChecks = nil
	for _, check := range pending {
		check()
	}
}

// undefined reports whether t names a Lyth type whose definition is not set
// yet: go/types cannot compute the type set of such a constraint, nor
// expand an instance of it.
func (c *Checker) undefined(t types.Type) bool {
	n, ok := types.Base(t).(*types.Named)
	if !ok || n.Decl == nil || c.defining == nil {
		return false
	}
	if c.defining[n.Decl] {
		return true
	}
	for _, a := range n.Args {
		if c.undefined(a) {
			return true
		}
	}
	return false
}

// resolveEnum records the entries of an enum and its generated String
// method. Entry i is the Go constant <Type><Entry> with value i.
func (c *Checker) resolveEnum(decl *types.TypeDecl, d *syntax.EnumDecl) {
	if len(d.Entries) == 0 {
		c.errorf(diag.EInvalidUse, d.Name.S, "enum %s has no entries", d.Name.Name)
	}
	for i, id := range d.Entries {
		if old := decl.LookupEntry(id.Name); old != nil {
			c.errorf(diag.EDuplicate, id.S, "enum %s already has an entry %s", decl.Name, id.Name).WithRelated(old.Span, "previous entry")
			continue
		}
		e := &types.EnumEntry{Name: id.Name, GoName: decl.GoName + exportedName(id.Name), Value: i, Decl: decl, Span: id.S}
		c.claimGoName(e.GoName, id.S, decl.Name+"."+id.Name)
		decl.Entries = append(decl.Entries, e)
	}
	decl.Methods = append(decl.Methods, &types.Method{Name: "String", GoName: "String", Sig: &types.Signature{Results: []types.Type{types.String}}, Pub: true, Span: d.Name.S})
}

func (c *Checker) resolveStruct(decl *types.TypeDecl, d *syntax.StructDecl) {
	seen := map[string]source.Span{}
	goSeen := map[string]source.Span{}
	for _, sf := range d.Fields {
		if sf.Name == nil {
			continue
		}
		name := sf.Name.Name
		if name == "_" {
			if sf.Name.S.Len() > 0 {
				c.errorf(diag.EInvalidUse, sf.Name.S, "struct fields cannot be named _")
			}
			continue
		}
		if old, ok := seen[name]; ok {
			c.errorf(diag.EDuplicate, sf.Name.S, "field %s is already declared", name).WithRelated(old, "previous declaration")
			continue
		}
		seen[name] = sf.Name.S
		t := c.resolveType(sf.Type)
		if t == types.Unit {
			c.errorf(diag.EInvalidUse, sf.Type.Span(), "unit cannot be used as a field type")
			t = types.Invalid
		}
		f := &types.Field{Name: name, Type: t, Private: !Exported(name), Span: sf.Name.S}
		f.GoName = GoName(name)
		if old, ok := goSeen[f.GoName]; ok {
			c.errorf(diag.EDuplicate, sf.Name.S, "field %s would be named %s in Go, which another field already uses", name, f.GoName).
				WithRelated(old, "conflicting field")
			continue
		}
		goSeen[f.GoName] = sf.Name.S
		f.Tag = c.fieldTag(sf, f)
		decl.Fields = append(decl.Fields, f)
	}
}

// fieldTag computes the Go struct tag, adding the automatic json key for
// exported fields whose tag does not mention json (ADR 0013, ADR 0022).
func (c *Checker) fieldTag(sf *syntax.Field, f *types.Field) string {
	tag := sf.Tag
	if sf.HasTag {
		if _, ok := reflect.StructTag(tag).Lookup("json"); !ok && !validTag(tag) {
			c.warnf(diag.WUnnecessary, sf.TagSpan, "struct tag is not in the conventional key:\"value\" format")
		}
	}
	if f.Private {
		return tag
	}
	if _, ok := reflect.StructTag(tag).Lookup("json"); ok {
		return tag
	}
	auto := "json:" + strconv.Quote(JSONName(f.Name))
	if tag == "" {
		return auto
	}
	return auto + " " + tag
}

// JSONName is the automatic json key of an exported field: the name with its
// leading uppercase run lowered, keeping the last letter of an initialism
// that starts the next word (Name -> name, ID -> id, UserID -> userID,
// HTTPServer -> httpServer).
func JSONName(name string) string {
	rs := []rune(name)
	n := 0
	for n < len(rs) && unicode.IsUpper(rs[n]) {
		n++
	}
	if n > 1 && n < len(rs) && unicode.IsLower(rs[n]) {
		n-- // the last uppercase letter starts the next word
	}
	if n == 0 {
		n = 1
	}
	for i := 0; i < n && i < len(rs); i++ {
		rs[i] = unicode.ToLower(rs[i])
	}
	return string(rs)
}

// validTag reports whether tag follows the key:"value" convention.
func validTag(tag string) bool {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return false
		}
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return false
		}
		if _, err := strconv.Unquote(tag[:i+1]); err != nil {
			return false
		}
		tag = tag[i+1:]
	}
	return true
}

func (c *Checker) resolveInterface(decl *types.TypeDecl, d *syntax.InterfaceDecl) {
	seen := map[string]source.Span{}
	for _, m := range d.Methods {
		if old, ok := seen[m.Name.Name]; ok {
			c.errorf(diag.EDuplicate, m.Name.S, "method %s is already declared", m.Name.Name).WithRelated(old, "previous declaration")
			continue
		}
		seen[m.Name.Name] = m.Name.S
		for _, prm := range m.Params {
			if prm.Default != nil {
				c.errorf(diag.EUnsupported, prm.Default.Span(), "interface methods cannot have default values").
					WithNote("declare defaults on the functions that implement or call the method")
			}
		}
		sig := c.signature(m.Params, m.Result, m.Throws, nil)
		decl.Methods = append(decl.Methods, &types.Method{Name: m.Name.Name, GoName: GoName(m.Name.Name), Sig: sig, Pub: Exported(m.Name.Name), Span: m.Name.S})
	}
	for _, e := range d.Embeds {
		t := c.resolveType(e)
		if t != types.Invalid && !types.IsInterface(t) {
			c.errorf(diag.ETypeMismatch, e.Span(), "%s is not an interface and cannot be embedded in interface %s", t, decl.Name)
			continue
		}
		decl.Embeds = append(decl.Embeds, t)
	}
}

// signature resolves a function signature.
func (c *Checker) signature(params []*syntax.Param, result *syntax.ResultType, throws bool, names *[]string) *types.Signature {
	sig := &types.Signature{Throws: throws}
	for i, p := range params {
		t := c.resolveType(p.Type)
		if t == types.Unit {
			c.errorf(diag.EInvalidUse, p.Type.Span(), "unit cannot be used as a parameter type")
			t = types.Invalid
		}
		if p.Variadic {
			if i != len(params)-1 {
				c.errorf(diag.EInvalidUse, p.S, "only the last parameter can be variadic")
			} else {
				sig.Variadic = true
			}
			t = &types.Slice{Elem: t}
		}
		sig.Params = append(sig.Params, t)
		sig.ParamNames = append(sig.ParamNames, p.Name.Name)
	}
	if result != nil {
		for _, rt := range result.Types {
			t := c.resolveType(rt)
			if t == types.Unit {
				if len(result.Types) > 1 {
					c.errorf(diag.EInvalidUse, rt.Span(), "unit cannot appear in a result list")
				}
				continue
			}
			sig.Results = append(sig.Results, t)
		}
	}
	return sig
}

// ---------------------------------------------------------------------------
// Functions and methods

func (c *Checker) resolveFuncDecls() {
	// Plain functions.
	for _, fs := range c.pkg.Funcs {
		d := fs.Decl
		scope := c.scopeFor(d.S)
		var tparams []*types.TypeParam
		if len(d.TypeParams) > 0 {
			scope, tparams = c.typeParams(d.TypeParams, scope)
			fs.tscope = scope
		}
		c.withScope(scope, func() {
			fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil)
		})
		if len(tparams) > 0 {
			genericSignature(fs.Sig, tparams)
		}
		if fs.IsMain {
			if len(d.Params) > 0 || len(fs.Sig.Results) > 0 {
				c.errorf(diag.EInvalidUse, d.Name.S, "func main must have no parameters and no results").
					WithNote("read arguments with os.Args; report failure by declaring func main() throws")
			}
		}
		if fs.IsTest {
			c.checkTestFunc(fs)
		}
		if inferable(fs) {
			fs.infer = inferPending
		}
	}
	// Methods.
	for _, f := range c.pkg.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*syntax.FunDecl)
			if !ok || fd.Recv == nil {
				continue
			}
			c.withScope(c.fileScope[f], func() { c.resolveMethod(fd) })
		}
	}
	// Results inferred from bodies, now that every signature they may use
	// exists. Bodies referring to other inferred functions infer those first.
	for _, fs := range c.pkg.Funcs {
		c.ensureInferred(fs)
	}
	for _, fs := range c.pkg.Funcs {
		c.checkDefaults(fs)
	}
}

// typeParams declares the type parameters of a generic function in a new
// scope inside parent and returns the scope with the parameters. Each is a
// go/types type parameter of the shadow package, so instantiation and
// constraint satisfaction are Go's. A missing constraint is Any.
func (c *Checker) typeParams(tps []*syntax.TypeParam, parent *Scope) (*Scope, []*types.TypeParam) {
	out := make([]*types.TypeParam, len(tps))
	for i, tp := range tps {
		tn := gotypes.NewTypeName(token.NoPos, c.shadow, tp.Name.Name, nil)
		out[i] = &types.TypeParam{Go: gotypes.NewTypeParam(tn, nil), Name: tp.Name.Name}
	}
	return c.typeParamScope(tps, out, parent), out
}

// typeParamScope declares type parameters out, written as tps, in a new
// scope inside parent and resolves their constraints there.
func (c *Checker) typeParamScope(tps []*syntax.TypeParam, out []*types.TypeParam, parent *Scope) *Scope {
	scope := newScope(parent, BlockScope)
	for i, tp := range tps {
		sym := &TypeSym{name: tp.Name.Name, Type: out[i], Span: tp.Name.S, Pkg: c.pkg}
		c.declare(scope, sym, tp.Name.S)
		c.info.Defs[tp.Name] = sym
	}
	c.withScope(scope, func() {
		for i, tp := range tps {
			var constraint types.Type = types.Any
			if tp.Constraint != nil {
				save := c.inConstraint
				c.inConstraint = true
				t := c.resolveType(tp.Constraint)
				c.inConstraint = save
				switch {
				case t == types.Invalid:
				case !types.IsInterface(t) || types.IsNullable(t):
					c.errorf(diag.ETypeMismatch, tp.Constraint.Span(), "the constraint of %s must be an interface, not %s", tp.Name.Name, t).
						WithHint("constrain it with an interface such as Comparable, cmp.Ordered, or fmt.Stringer")
				default:
					constraint = types.Base(t)
				}
			}
			out[i].Constraint = constraint
			if c.undefined(constraint) {
				tp, gc := out[i].Go, types.Lower(constraint)
				c.pendingConstraints = append(c.pendingConstraints, func() { tp.SetConstraint(gc) })
			} else {
				out[i].Go.SetConstraint(types.Lower(constraint))
			}
		}
	})
	return scope
}

// genericSignature makes sig the signature of a generic function with type
// parameters tparams, with a go/types signature for instantiation.
func genericSignature(sig *types.Signature, tparams []*types.TypeParam) {
	sig.TypeParams = tparams
	gs := types.LowerSignature(sig, nil)
	gps := make([]*gotypes.TypeParam, len(tparams))
	for i, tp := range tparams {
		gps[i] = tp.Go
	}
	sig.GoSig = gotypes.NewSignatureType(nil, nil, gps, gs.Params(), gs.Results(), gs.Variadic())
}

// checkDefaults checks the default values of fs's parameters. A default is
// evaluated at each call site that leaves the parameter out, so it must be
// a simple expression that does not refer to other parameters, and a pub
// function's defaults may only use public names (callers in other packages
// evaluate them).
func (c *Checker) checkDefaults(fs *FuncSym) {
	d := fs.Decl
	if d == nil || fs.Sig == nil {
		return
	}
	if len(fs.Sig.TypeParams) > 0 {
		for _, p := range d.Params {
			if p.Default != nil {
				c.errorf(diag.EUnsupported, p.Default.Span(), "generic functions cannot have default values yet")
				return
			}
		}
	}
	params := map[string]bool{}
	for _, p := range d.Params {
		params[p.Name.Name] = true
	}
	for i, p := range d.Params {
		if p.Default == nil {
			continue
		}
		if p.Variadic {
			c.errorf(diag.EInvalidUse, p.Default.Span(), "a variadic parameter cannot have a default value; leaving it out passes no values")
			continue
		}
		if bad := complexDefault(p.Default); bad != nil {
			c.errorf(diag.EInvalidUse, bad.Span(), "a default value must be a simple expression, without ?, catch, if, when, ?:, ?., !!, or casts").
				WithNote("defaults are evaluated at every call site that leaves the parameter out")
			continue
		}
		if id := paramReference(p.Default, params); id != nil {
			c.errorf(diag.EInvalidUse, id.S, "a default value cannot refer to parameter %s", id.Name).
				WithNote("defaults are evaluated at the call site, before the call")
			continue
		}
		saveScope, saveFn := c.scope, c.fn
		c.scope = newScope(c.scopeFor(d.S), FuncScope)
		c.fn = c.initCtx(d.S)
		c.fn.name = "default value"
		c.scope.fn = c.fn
		x := c.value(p.Default, fs.Sig.Params[i])
		c.assign(x, fs.Sig.Params[i], "default value of "+p.Name.Name)
		c.scope, c.fn = saveScope, saveFn
		if fs.Pub {
			syntax.Walk(p.Default, func(n syntax.Node) {
				id, ok := n.(*syntax.Ident)
				if !ok {
					return
				}
				if name, private := c.privateDecl(c.info.Uses[id]); private {
					c.errorf(diag.EPrivate, id.S, "the default value of exported %s uses unexported %s", fs.name, name).
						WithNote("callers in other packages evaluate default values, so they can only use exported names")
				}
			})
		}
	}
}

// privateDecl reports whether sym is an unexported declaration of this package.
func (c *Checker) privateDecl(sym any) (string, bool) {
	switch s := sym.(type) {
	case *FuncSym:
		return s.name, !s.Pub && s.Pkg == c.pkg
	case *ConstSym:
		return s.name, !s.Pub && s.Pkg == c.pkg
	case *VarSym:
		return s.name, s.Kind == GlobalVar && !s.Pub && s.Pkg == c.pkg
	case *TypeSym:
		return s.name, s.Decl != nil && !s.Decl.Pub && s.Pkg == c.pkg
	}
	return "", false
}

// complexDefault returns the first part of a default value that would need
// statements at the call site, or nil.
func complexDefault(e syntax.Expr) syntax.Node {
	var bad syntax.Node
	syntax.Walk(e, func(n syntax.Node) {
		if bad != nil {
			return
		}
		switch x := n.(type) {
		case *syntax.TryExpr, *syntax.CatchExpr, *syntax.IfExpr, *syntax.WhenExpr, *syntax.ElvisExpr,
			*syntax.NotNullExpr, *syntax.IsExpr, *syntax.InExpr, *syntax.JumpExpr:
			bad = n
		case *syntax.AsExpr:
			bad = n
		case *syntax.SelectorExpr:
			if x.Safe {
				bad = n
			}
		}
	})
	return bad
}

// paramReference returns an identifier in e naming one of params, outside
// lambdas (whose own parameters may reuse the names).
func paramReference(e syntax.Expr, params map[string]bool) *syntax.Ident {
	var found *syntax.Ident
	var visit func(syntax.Node)
	visit = func(n syntax.Node) {
		if found != nil {
			return
		}
		switch x := n.(type) {
		case *syntax.LambdaExpr:
			return
		case *syntax.Ident:
			if params[x.Name] {
				found = x
			}
		case *syntax.SelectorExpr:
			visit(x.X) // x.Sel names a member, not a parameter
			return
		}
		syntax.Children(n, visit)
	}
	visit(e)
	return found
}

// inferable reports whether fs's result type comes from its body: a
// non-throwing function or method with an expression body and no declared
// result. Only unexported functions may rely on it (an exported function's
// result is part of its API, so leaving it out is an error), but exported ones are inferred
// too, so that their callers see the type instead of follow-on errors.
func inferable(fs *FuncSym) bool {
	d := fs.Decl
	return d != nil && d.ExprBody != nil && d.Result == nil && !d.Throws && !fs.IsMain && !fs.IsTest && len(d.TypeParams) == 0
}

// ensureInferred infers fs's result type if it is still pending, by checking
// its body now.
func (c *Checker) ensureInferred(fs *FuncSym) {
	switch fs.infer {
	case inferPending:
		fs.infer = inferRunning
		c.funcBody(fs)
		fs.bodyChecked = true
		fs.infer = inferDone
		if fs.Recv != nil {
			c.addShadowMethod(fs)
		}
	case inferRunning:
		if !fs.inferCycle {
			fs.inferCycle = true
			c.errorf(diag.ECannotInfer, fs.Span, "cannot infer the result type of %s: it depends on itself", fs.name).
				WithHint("declare it: func %s(...): Type = ...", fs.name)
		}
	}
}

// checkTestFunc checks the signature go test requires of fs. Tests,
// benchmarks, and fuzz targets may throw: the generated wrapper reports the
// error with Fatal.
func (c *Checker) checkTestFunc(fs *FuncSym) {
	var param, form string
	throwsOK := true
	switch name := fs.name; {
	case name == "TestMain":
		param, form, throwsOK = "*testing.M", "(m: *testing.M)", false
	case strings.HasPrefix(name, "Test"):
		param, form = "*testing.T", "(t: *testing.T)"
	case strings.HasPrefix(name, "Benchmark"):
		param, form = "*testing.B", "(b: *testing.B)"
	case strings.HasPrefix(name, "Fuzz"):
		param, form = "*testing.F", "(f: *testing.F)"
	default:
		form, throwsOK = "()", false // examples
	}
	sig := fs.Sig
	ok := len(sig.Results) == 0 && (throwsOK || !sig.Throws)
	if param == "" {
		ok = ok && len(sig.Params) == 0
	} else {
		ok = ok && len(sig.Params) == 1 && types.Lower(sig.Params[0]).String() == param
	}
	if !ok {
		d := c.errorf(diag.EInvalidUse, fs.Span, "%s is run by go test, so it must be declared func %s%s", fs.name, fs.name, form)
		if throwsOK {
			d.WithNote("it may throw: a thrown error fails the test")
		}
	}
}

func (c *Checker) resolveMethod(d *syntax.FunDecl) {
	// A struct or enum of this package gets a method; any other receiver
	// type an extension function.
	var ts *TypeSym
	if d.Recv.Name != nil {
		ts, _ = c.lookup(d.Recv.Name.Name).(*TypeSym)
	}
	if ts == nil || ts.Decl == nil || ts.Pkg != c.pkg || ts.Decl.Kind == types.InterfaceDecl {
		c.resolveExtension(d)
		return
	}
	recvName := d.Recv.Name.Name
	c.info.Uses[d.Recv.Name] = ts
	decl := ts.Decl
	if len(d.TypeParams) > 0 {
		span := d.TypeParams[0].S.Join(d.TypeParams[len(d.TypeParams)-1].S)
		if len(decl.TypeParams) > 0 {
			c.errorf(diag.EInvalidUse, span, "the receiver %s[...] already names the type parameters of %s", recvName, recvName).
				WithHint("remove the type parameter list: func %s[%s].%s(...)", recvName, typeParamNames(decl.TypeParams), d.Name.Name)
		} else {
			c.errorf(diag.EUnsupported, span, "a method cannot declare its own type parameters: Go has no generic methods").
				WithHint("declare a generic function that takes the %s as a parameter", recvName)
		}
	}
	// A generic type's methods name its type parameters: func Page[T].First().
	var tscope *Scope
	var recvArgs []types.Type
	switch {
	case len(decl.TypeParams) != len(d.Recv.TypeArgs) && len(decl.TypeParams) > 0:
		c.errorf(diag.ETypeArgs, d.Recv.S, "%s is generic: declare the method as func %s[%s].%s(...)", recvName, recvName, typeParamNames(decl.TypeParams), d.Name.Name)
		return
	case len(d.Recv.TypeArgs) > 0 && len(decl.TypeParams) == 0:
		c.errorf(diag.ETypeArgs, d.Recv.S, "%s is not generic", recvName)
		return
	case len(decl.TypeParams) > 0:
		tscope = newScope(c.scope, BlockScope)
		for i, ta := range d.Recv.TypeArgs {
			nt, ok := ta.(*syntax.NamedType)
			if !ok || nt.Pkg != nil || len(nt.Args) > 0 {
				c.errorf(diag.ETypeArgs, ta.Span(), "the receiver names the type parameters of %s; write a name such as %s", recvName, decl.TypeParams[i].Name)
				return
			}
			sym := &TypeSym{name: nt.Name.Name, Type: decl.TypeParams[i], Span: nt.Name.S, Pkg: c.pkg}
			c.declare(tscope, sym, nt.Name.S)
			c.info.Defs[nt.Name] = sym
			recvArgs = append(recvArgs, decl.TypeParams[i])
		}
	}
	name := d.Name.Name
	pub := Exported(name)
	fs := &FuncSym{name: name, Pub: pub, Pkg: c.pkg, Decl: d, Recv: decl, RecvArgs: recvArgs, PtrRecv: d.Recv.Pointer, Span: d.Name.S, tscope: tscope}
	fs.GoName = GoName(name)
	if tscope != nil {
		c.withScope(tscope, func() { fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil) })
	} else {
		fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil)
	}
	if old := decl.LookupMethod(name); old != nil {
		if decl.Kind == types.EnumDecl && old.Sym == nil {
			c.errorf(diag.EDuplicate, d.Name.S, "enum %s already has a %s method: it returns the entry's name", recvName, name).
				WithRelated(old.Span, "generated for this enum")
			return
		}
		c.errorf(diag.EDuplicate, d.Name.S, "method %s.%s is already declared", recvName, name).WithRelated(old.Span, "previous declaration")
		return
	}
	if f := decl.LookupField(name); f != nil {
		c.errorf(diag.EDuplicate, d.Name.S, "%s has both a field and a method named %s", recvName, name).WithRelated(f.Span, "field declared here")
		return
	}
	for _, f := range decl.Fields {
		if f.GoName == fs.GoName {
			c.errorf(diag.EDuplicate, d.Name.S, "method %s would be named %s in Go, which conflicts with field %s", name, fs.GoName, f.Name).WithRelated(f.Span, "field declared here")
			return
		}
	}
	for _, m := range decl.Methods {
		if m.GoName == fs.GoName {
			c.errorf(diag.EDuplicate, d.Name.S, "method %s would be named %s in Go, which conflicts with method %s", name, fs.GoName, m.Name).WithRelated(m.Span, "method declared here")
			return
		}
	}
	m := &types.Method{Name: name, GoName: fs.GoName, Sig: fs.Sig, PtrRecv: d.Recv.Pointer, Pub: pub, Span: d.Name.S, Sym: fs}
	decl.Methods = append(decl.Methods, m)
	if inferable(fs) {
		fs.infer = inferPending // its shadow method waits for the result type
	} else {
		c.addShadowMethod(fs)
	}
	c.pkg.Funcs = append(c.pkg.Funcs, fs)
}

// resolveExtension declares an extension function: a function on a type that
// is not a struct or enum of this package (String, []T, Map<K, V>, a Go or
// interface type), called like a method and lowered to a Go function that
// takes the receiver as its first parameter. Its body reaches the receiver
// through this.
func (c *Checker) resolveExtension(d *syntax.FunDecl) {
	scope := c.scope
	var tparams []*types.TypeParam
	if len(d.TypeParams) > 0 {
		scope, tparams = c.typeParams(d.TypeParams, scope)
	}
	name := d.Name.Name
	fs := &FuncSym{name: name, Pub: Exported(name), Pkg: c.pkg, Decl: d, Span: d.Name.S}
	fs.GoName = GoName(name)
	if tparams != nil {
		fs.tscope = scope
	}
	var recv types.Type
	c.withScope(scope, func() {
		recv = c.resolveType(d.Recv.Type)
		if d.Recv.Pointer && recv != types.Invalid {
			recv = &types.Pointer{Elem: recv}
		}
		fs.Sig = c.signature(d.Params, d.Result, d.Throws, nil)
	})
	switch {
	case recv == types.Invalid:
		recv = nil
	case types.IsNullable(recv):
		c.errorf(diag.EUnsupported, d.Recv.Type.Span(), "an extension function cannot have a nullable receiver yet")
		recv = nil
	default:
		if _, bare := types.Base(recv).(*types.TypeParam); bare {
			c.errorf(diag.EInvalidUse, d.Recv.Type.Span(), "an extension's receiver cannot be a bare type parameter: it would apply to every type")
			recv = nil
		}
	}
	fs.ExtRecv = recv
	if recv == nil {
		fs.ExtRecv = types.Invalid
	}
	if tparams != nil {
		// The go/types signature of the Go function, receiver first.
		full := &types.Signature{Params: append([]types.Type{fs.ExtRecv}, fs.Sig.Params...), Variadic: fs.Sig.Variadic, Results: fs.Sig.Results, Throws: fs.Sig.Throws}
		genericSignature(full, tparams)
		fs.Sig.TypeParams, fs.Sig.GoSig = tparams, full.GoSig
	}
	if old := c.pkg.Extensions[name]; old != nil {
		c.errorf(diag.EDuplicate, d.Name.S, "extension %s is already declared for %s; Go has one function per name", name, old.ExtRecv).
			WithRelated(old.Span, "previous declaration")
	} else if recv != nil {
		c.pkg.Extensions[name] = fs
		c.claimGoName(fs.GoName, d.Name.S, name)
	}
	if inferable(fs) {
		fs.infer = inferPending
	}
	c.pkg.Funcs = append(c.pkg.Funcs, fs)
}

// matchReceiver reports whether an extension with receiver type recv (in
// terms of type parameters tps) applies to a value of type t, and binds the
// type parameters. A pointer receiver applies to a value through its address
// and a value receiver to a pointer through it; an interface receiver applies
// to the types that implement it.
func (c *Checker) matchReceiver(recv types.Type, tps []*types.TypeParam, t types.Type) (bindings, *ExtCall, bool) {
	t = types.DefaultType(types.Base(t)) // "hi".Shout() on String
	try := func(recv, t types.Type) (bindings, bool) {
		bind := bindings{}
		if len(tps) > 0 {
			unify(recv, t, bind)
		}
		if types.Identical(subst(recv, bind), t) {
			return bind, true
		}
		if len(tps) == 0 && types.IsInterface(recv) && !types.IsInterface(t) && c.implements(t, recv) {
			return bind, true
		}
		return nil, false
	}
	if bind, ok := try(recv, t); ok {
		return bind, &ExtCall{}, true
	}
	if p, ok := recv.(*types.Pointer); ok {
		if bind, ok := try(p.Elem, t); ok {
			return bind, &ExtCall{Addr: true}, true
		}
	}
	if p, ok := t.(*types.Pointer); ok {
		if _, recvPtr := recv.(*types.Pointer); !recvPtr {
			if bind, ok := try(recv, types.Base(p.Elem)); ok {
				return bind, &ExtCall{Deref: true}, true
			}
		}
	}
	return nil, nil, false
}

// findExtension finds the extension function name for a receiver of type t:
// declared in this package or exported by a Lyth package the file imports.
// Two applicable extensions of different packages are ambiguous, which it
// reports (reported is then true).
func (c *Checker) findExtension(t types.Type, name string, id *syntax.Ident) (found *ExtCall, reported bool) {
	type candidate struct {
		fs  *FuncSym
		pkg *Package
		ps  *PkgSym
	}
	var cands []candidate
	if fs := c.pkg.Extensions[name]; fs != nil {
		cands = append(cands, candidate{fs, c.pkg, nil})
	}
	for s := c.scope; s != nil; s = s.parent {
		if s.kind != FileScope {
			continue
		}
		for _, sym := range s.order {
			if ps, ok := sym.(*PkgSym); ok && ps.Pkg != nil && ps.Pkg.Lyth && ps.Pkg != c.pkg {
				if fs := ps.Pkg.Extensions[name]; fs != nil && fs.Pub {
					cands = append(cands, candidate{fs, ps.Pkg, ps})
				}
			}
		}
	}
	for _, cand := range cands {
		bind, ext, ok := c.matchReceiver(cand.fs.ExtRecv, cand.fs.Sig.TypeParams, t)
		if !ok {
			continue
		}
		if cand.ps != nil {
			cand.ps.Used = true
		}
		if found != nil {
			c.errorf(diag.EDuplicate, id.S, "%s is ambiguous: packages %s and %s both declare it for %s", name, found.Pkg.Name, cand.pkg.Name, types.DefaultType(types.Base(t)))
			reported = true
			continue
		}
		ext.Func, ext.Pkg, ext.Bind = cand.fs, cand.pkg, bind
		found = ext
	}
	if reported {
		return nil, true
	}
	return found, false
}

// addShadowMethod declares method fs on its type in the shadow go/types
// package, for interface satisfaction and validation.
func (c *Checker) addShadowMethod(fs *FuncSym) {
	decl := fs.Recv
	sig := fs.Sig
	var recvType gotypes.Type = decl.Shadow
	var rtps []*gotypes.TypeParam
	if len(decl.TypeParams) > 0 {
		// Go binds the type parameters of a method's receiver anew:
		// func (p Page[T]) First() T.
		bind := bindings{}
		targs := make([]gotypes.Type, len(decl.TypeParams))
		for i, tp := range decl.TypeParams {
			fresh := gotypes.NewTypeParam(gotypes.NewTypeName(token.NoPos, c.shadow, tp.Name, nil), tp.Go.Constraint())
			rtps = append(rtps, fresh)
			targs[i] = fresh
			bind[tp.Go] = &types.TypeParam{Go: fresh, Name: tp.Name, Constraint: tp.Constraint}
		}
		inst, err := gotypes.Instantiate(nil, decl.Shadow, targs, false)
		if err != nil {
			return
		}
		recvType = inst
		sig = substSig(sig, bind)
	}
	if fs.PtrRecv {
		recvType = gotypes.NewPointer(recvType)
	}
	recvVar := gotypes.NewVar(token.NoPos, c.shadow, "this", recvType)
	gs := types.LowerSignature(sig, recvVar)
	if rtps != nil {
		gs = gotypes.NewSignatureType(recvVar, rtps, nil, gs.Params(), gs.Results(), gs.Variadic())
	}
	decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, fs.GoName, gs))
}

// ---------------------------------------------------------------------------
// Globals and constants

func (c *Checker) resolveGlobals() {
	for _, g := range c.pkg.Globals {
		switch g := g.(type) {
		case *VarSym:
			c.globalType(g)
		case *ConstSym:
			c.constValue(g)
		}
	}
}

// initCtx returns a function context for package-level initializers.
func (c *Checker) initCtx(span source.Span) *funcCtx {
	return &funcCtx{name: "package initializer", declSpan: span}
}

// globalType resolves a global's type, checking its initializer on demand.
func (c *Checker) globalType(v *VarSym) types.Type {
	switch v.state {
	case 2:
		return v.Type
	case 1:
		c.errorf(diag.EInvalidUse, v.Span, "initialization cycle: %s refers to itself", v.name)
		v.Type = types.Invalid
		return v.Type
	}
	v.state = 1
	d := v.Decl
	saveScope, saveFn := c.scope, c.fn
	c.scope = newScope(c.scopeFor(d.S), FuncScope)
	c.fn = c.initCtx(d.S)
	c.scope.fn = c.fn
	var want types.Type
	if d.Type != nil {
		want = c.resolveType(d.Type)
	}
	if d.Value != nil {
		x := c.expr(d.Value, want)
		if x.mode == ModeNoValue {
			c.errorf(diag.EInvalidUse, d.Value.Span(), "%s does not produce a value", d.Value.Span().Short())
			x = invalid(d.Value)
		} else {
			x = c.singleValue(x)
		}
		if want != nil {
			c.assign(x, want, "initializer of "+v.name)
			v.Type = want
		} else {
			v.Type = c.defaultValueType(x, "initializer of "+v.name)
		}
	} else if want != nil {
		v.Type = want
	} else {
		v.Type = types.Invalid
	}
	c.scope, c.fn = saveScope, saveFn
	v.state = 2
	return v.Type
}

// constValue resolves a constant's type and value.
func (c *Checker) constValue(k *ConstSym) {
	switch k.state {
	case 2:
		return
	case 1:
		c.errorf(diag.EInvalidUse, k.Span, "constant %s refers to itself", k.name)
		k.Type = types.Invalid
		return
	}
	k.state = 1
	d := k.Decl
	saveScope, saveFn := c.scope, c.fn
	c.scope = newScope(c.scopeFor(d.S), FuncScope)
	c.fn = c.initCtx(d.S)
	var want types.Type
	if d.Type != nil {
		want = c.resolveType(d.Type)
	}
	x := c.expr(d.Value, want)
	switch {
	case x.mode == ModeInvalid:
		k.Type = types.Invalid
	case x.val == nil:
		c.errorf(diag.EInvalidUse, d.Value.Span(), "const initializer must be a constant expression").
			WithHint("use 'var %s = ...' for values computed at run time", k.name)
		k.Type = types.Invalid
	default:
		if want != nil {
			if b := types.BasicOf(want); b == nil {
				c.errorf(diag.ETypeMismatch, d.Type.Span(), "constants must have a basic type, not %s", want)
				want = types.Invalid
			} else {
				c.assign(x, want, "constant "+k.name)
				x.val = c.convertConst(x.val, want)
			}
			k.Type = want
		} else {
			k.Type = x.typ
		}
		k.Val = x.val
	}
	c.scope, c.fn = saveScope, saveFn
	k.state = 2
}

func (c *Checker) convertConst(v constant.Value, t types.Type) constant.Value {
	switch {
	case types.IsInteger(t):
		return constant.ToInt(v)
	case types.IsFloat(t):
		return constant.ToFloat(v)
	}
	return v
}

// ---------------------------------------------------------------------------
// Bodies

func (c *Checker) checkBodies() {
	for _, fs := range c.pkg.Funcs {
		if fs.Sig == nil || fs.Decl == nil || fs.bodyChecked {
			continue
		}
		c.funcBody(fs)
	}
}

func (c *Checker) funcBody(sym *FuncSym) {
	d := sym.Decl
	parent := c.scopeFor(d.S)
	if sym.tscope != nil {
		parent = sym.tscope
	}
	scope := newScope(parent, FuncScope)
	fn := &funcCtx{sig: sym.Sig, name: sym.name, isMain: sym.IsMain, declSpan: d.Name.S, throwsSpan: d.ThrowsSpan}
	fn.fallible, fn.rawTuple = fallibleResults(sym.Sig)
	scope.fn = fn
	info := &FuncInfo{Sym: sym}
	if sym.ExtRecv != nil {
		rv := &VarSym{name: "self", Type: sym.ExtRecv, Kind: ReceiverVar, Span: d.Recv.S, Implicit: true, Used: true}
		scope.Insert(rv)
		fn.recv = rv // members through this; there is no implicit member scope
		info.Recv = rv
	}
	if sym.Recv != nil {
		var rt types.Type = &types.Named{Decl: sym.Recv, Args: sym.RecvArgs}
		if sym.PtrRecv {
			rt = &types.Pointer{Elem: rt}
		}
		rv := &VarSym{name: "self", Type: rt, Kind: ReceiverVar, Span: d.Recv.S, Implicit: true, Used: true}
		scope.Insert(rv)
		fn.recv, fn.recvDecl = rv, sym.Recv
		info.Recv = rv
	}
	for i, p := range d.Params {
		pv := &VarSym{name: p.Name.Name, Type: sym.Sig.Params[i], Kind: ParamVar, Span: p.Name.S, Used: true}
		if p.Name.Name != "_" {
			c.declare(scope, pv, p.Name.S)
		}
		c.info.Defs[p.Name] = pv
		info.Params = append(info.Params, pv)
	}
	var params []string
	for _, p := range d.Params {
		params = append(params, p.Name.Name)
	}
	if d.Body != nil {
		fn.reassigned = reassignedNames(params, d.Body)
	} else if d.ExprBody != nil {
		fn.reassigned = reassignedNames(params, d.ExprBody)
	}
	saveScope, saveFn := c.scope, c.fn
	c.scope, c.fn = scope, fn
	if d.Body != nil {
		terminates := c.stmtList(d.Body.Stmts)
		if len(sym.Sig.Results) > 0 && !terminates {
			c.errorf(diag.EMissingReturn, d.Body.Rbrace, "missing return in %s", sym.name).
				WithRelated(d.Result.S, "declared to return %s", resultString(sym.Sig))
		}
	} else if d.ExprBody != nil {
		if sym.infer == inferRunning {
			c.inferBody(d.ExprBody, sym)
		} else {
			c.exprBody(d.ExprBody, sym)
		}
	}
	info.Unused = c.finishLocals(fn)
	c.scope, c.fn = saveScope, saveFn
	c.info.Funcs[d] = info
}

// inferBody checks the body of a function whose result type it infers.
func (c *Checker) inferBody(e syntax.Expr, sym *FuncSym) {
	x := c.expr(e, nil)
	setResult := func(t types.Type) {
		if !sym.inferCycle {
			sym.Sig.Results = []types.Type{t}
		}
	}
	switch {
	case x.mode == ModeInvalid:
		setResult(types.Invalid)
	case x.mode == ModeNoValue || x.typ == types.Unit || isNothing(x.typ):
		// no result
	default:
		switch t := x.typ.(type) {
		case *types.Result:
			c.errorf(diag.ECannotInfer, e.Span(), "%s can fail, so it declares its result type with throws", sym.name).
				WithHint("write func %s(...): %s throws = ...", sym.name, resultHint(t))
			setResult(types.Invalid)
		case *types.Tuple:
			c.errorf(diag.ECannotInfer, e.Span(), "%s returns several values; declare them: func %s(...): %s = ...", sym.name, sym.name, t)
			setResult(types.Invalid)
		default:
			rt := c.defaultValueType(x, "the result of "+sym.name)
			if sym.Pub && rt != types.Invalid {
				c.errorf(diag.ECannotInfer, e.Span(), "declare the result type of this expression-bodied function").
					WithHint("write func %s(...): %s = ...", sym.name, rt).
					WithNote("an exported function's result type is part of its API; unexported functions may leave it out")
			}
			setResult(rt)
		}
	}
}

func resultHint(r *types.Result) string {
	switch len(r.Success) {
	case 0:
		return "unit"
	case 1:
		return r.Success[0].String()
	}
	return (&types.Tuple{Elems: r.Success}).String()
}

// exprBody checks `= expr` function bodies.
func (c *Checker) exprBody(e syntax.Expr, sym *FuncSym) {
	sig := sym.Sig
	switch len(sig.Results) {
	case 0:
		x := c.expr(e, nil)
		if c.passThrough(x, nil) {
			return
		}
		if x.mode == ModeValue && !isNothing(x.typ) && x.typ != types.Invalid {
			if r, ok := x.typ.(*types.Result); ok && len(r.Success) == 0 {
				if call, ok := syntax.Unparen(e).(*syntax.CallExpr); ok {
					if !c.errorIgnorable(call) {
						c.unhandledError(call, r)
					}
					break
				}
			}
			d := c.errorf(diag.ECannotInfer, e.Span(), "declare the result type of this expression-bodied function").
				WithHint("write func %s(...): %s = ...", sym.name, c.defaultValueType(x, ""))
			if sym.Pub {
				d.WithNote("an exported function's result type is part of its API; unexported functions may leave it out")
			}
		}
	case 1:
		x := c.expr(e, sig.Results[0])
		if c.passThrough(x, sig.Results) {
			return
		}
		c.assign(c.singleValue(x), sig.Results[0], "return value")
	default:
		c.errorf(diag.EInvalidUse, e.Span(), "functions with multiple results need a block body")
	}
}

// fallibleResults reports whether a function with sig may use `?`/throw,
// and whether it does so through a raw tuple ending in Error.
func fallibleResults(sig *types.Signature) (fallible, raw bool) {
	if sig.Throws {
		return true, false
	}
	if n := len(sig.Results); n > 0 && types.IsErrorType(sig.Results[n-1]) && n > 1 {
		return true, true
	}
	return false, false
}

func resultString(sig *types.Signature) string {
	switch len(sig.Results) {
	case 0:
		return "unit"
	case 1:
		return sig.Results[0].String()
	}
	return (&types.Tuple{Elems: sig.Results}).String()
}

// finishLocals reports unused locals and returns them.
func (c *Checker) finishLocals(fn *funcCtx) []*VarSym {
	var unused []*VarSym
	for _, v := range fn.locals {
		if !v.Used && v.name != "_" && v.name != "it" && !v.Implicit {
			unused = append(unused, v)
			c.info.Unused[v] = true
			c.warnf(diag.WUnusedVar, v.Span, "%s is declared but never used", v.name).
				WithHint("remove it, or name it _ to discard the value")
		}
	}
	return unused
}

// checkConformance checks `struct T : I1, I2` declarations once every
// method is known: T or *T must implement each interface. The check costs
// nothing at run time; it reports a missing method at the declaration
// instead of at some distant use, and documents intent.
func (c *Checker) checkConformance() {
	for _, ts := range c.pkg.Types {
		d, ok := ts.syn.(*syntax.StructDecl)
		if !ok || len(d.Implements) == 0 || ts.Decl == nil {
			continue
		}
		self := ts.Type
		scope := c.scopeFor(d.S)
		if len(ts.Decl.TypeParams) > 0 {
			self = &types.Named{Decl: ts.Decl, Args: typeParamTypes(ts.Decl.TypeParams)}
			scope = ts.tscope
		}
		c.withScope(scope, func() {
			ptr := &types.Pointer{Elem: self}
			seen := map[string]bool{}
			for _, te := range d.Implements {
				it := c.resolveType(te)
				switch {
				case it == types.Invalid: // already reported
				case !types.IsInterface(it) || types.MayBeNull(it):
					c.errorf(diag.ETypeMismatch, te.Span(), "%s is not an interface; a struct can only declare interfaces it implements", it).
						WithNote("Lyth has no inheritance: share behavior through interfaces and composition")
				case seen[it.String()]:
					c.errorf(diag.EDuplicate, te.Span(), "%s is already listed", it)
				case !c.joinSealed(ts, it, te.Span()):
				case c.implements(self, it):
					c.info.Conforms[d] = append(c.info.Conforms[d], Conformance{Iface: it, Span: te.Span()})
					if sd := types.SealedOf(it); sd != nil {
						sd.Members = append(sd.Members, ts.Type)
					}
				case c.implements(ptr, it):
					c.info.Conforms[d] = append(c.info.Conforms[d], Conformance{Iface: it, Ptr: true, Span: te.Span()})
					if sd := types.SealedOf(it); sd != nil {
						sd.Members = append(sd.Members, ptr)
					}
				default:
					note, cascade := conformanceNote(ts.Decl, it)
					if cascade {
						break
					}
					if note == "" {
						_, _, note = c.assignable(ptr, it)
					}
					dg := c.errorf(diag.ENotImplement, te.Span(), "%s does not implement %s", ts.Decl.Name, it)
					if note != "" {
						dg.WithNote("%s", note)
					}
				}
				seen[it.String()] = true
			}
		})
	}
}

// joinSealed makes the struct ts a member of it when it is a sealed
// interface: the struct gets the interface's unexported marker method (a
// value receiver, so T and *T both have it). Only types of the interface's
// own package can join, as Go's unexported method requires. It reports
// false after an error.
func (c *Checker) joinSealed(ts *TypeSym, it types.Type, span source.Span) bool {
	sd := types.SealedOf(it)
	if sd == nil {
		return true
	}
	if sd.PkgPath != c.pkg.Path {
		c.errorf(diag.ENotImplement, span, "%s is sealed: only types declared in package %s can implement it", span.Text(), sd.PkgName)
		return false
	}
	for _, m := range ts.Decl.Methods {
		if m.GoName == sd.Marker {
			c.errorf(diag.EDuplicate, m.Span, "method %s conflicts with the marker method that sealed interface %s gives its members", m.Name, it)
			return false
		}
	}
	recv := gotypes.NewVar(token.NoPos, c.shadow, "", ts.Decl.Shadow)
	ts.Decl.Shadow.AddMethod(gotypes.NewFunc(token.NoPos, c.shadow, sd.Marker, gotypes.NewSignatureType(recv, nil, nil, nil, nil, false)))
	return true
}

// implements reports whether a value of type t can be used as iface.
func (c *Checker) implements(t, iface types.Type) bool {
	ok, _, _ := c.assignable(t, iface)
	return ok
}

// conformanceNote names the method that keeps *T (and so T) from
// implementing iface, the signature iface needs, and why: the method is
// missing, has another signature, or is not pub. The note is "" when the Go
// method sets agree and only Lyth-level types differ. cascade reports a
// method whose signature already failed to resolve.
func conformanceNote(decl *types.TypeDecl, iface types.Type) (note string, cascade bool) {
	gi, ok := types.Lower(iface).Underlying().(*gotypes.Interface)
	if !ok || decl.Shadow == nil {
		return "", false
	}
	m, _ := gotypes.MissingMethod(gotypes.NewPointer(decl.Shadow), gi, true)
	if m == nil {
		return "", false
	}
	name := m.Name()
	var want *types.Signature
	if n, ok := types.Base(iface).(*types.Named); ok && n.Decl != nil {
		for _, im := range n.Decl.Methods {
			if im.GoName == m.Name() {
				name, want = im.Name, im.Sig
			}
		}
	}
	if want == nil {
		// A Go method returning a trailing error is a throws method in Lyth.
		want = types.LiftSignature(m.Signature())
		if k := len(want.Results); k > 0 && types.IsErrorType(want.Results[k-1]) {
			want.Results, want.Throws = want.Results[:k-1], true
		}
	}
	for _, dm := range decl.Methods {
		switch {
		case !strings.EqualFold(dm.Name, name):
		case slices.Contains(dm.Sig.Params, types.Invalid) || slices.Contains(dm.Sig.Results, types.Invalid):
			return "", true
		case dm.GoName == m.Name():
			return fmt.Sprintf("method %s has type %s, but %s needs %s", dm.Name, dm.Sig, iface, want), false
		default:
			return fmt.Sprintf("method %s must be named %s to implement %s", dm.Name, name, iface), false
		}
	}
	return fmt.Sprintf("missing method %s: %s", name, want), false
}

// checkEmbedCycles reports interfaces of this package that embed
// themselves, directly or through other interfaces (Go: invalid recursive
// type), and removes the embedding that closes each cycle.
func (c *Checker) checkEmbedCycles() {
	state := map[*types.TypeDecl]int{} // 1 visiting, 2 done
	var visit func(d *types.TypeDecl) bool
	visit = func(d *types.TypeDecl) bool {
		switch state[d] {
		case 1:
			return true
		case 2:
			return false
		}
		state[d] = 1
		kept := d.Embeds[:0]
		for _, e := range d.Embeds {
			if n, ok := types.Base(e).(*types.Named); ok && n.Decl != nil && n.Decl.Kind == types.InterfaceDecl && visit(n.Decl) {
				c.errorf(diag.EInvalidUse, d.Span, "interface %s embeds itself through %s", d.Name, n.Decl.Name)
				continue
			}
			kept = append(kept, e)
		}
		d.Embeds = kept
		state[d] = 2
		return false
	}
	for _, ts := range c.pkg.Types {
		if ts.Decl != nil && ts.Decl.Kind == types.InterfaceDecl {
			visit(ts.Decl)
		}
	}
}
