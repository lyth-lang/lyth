package lower

import (
	"go/ast"
	"go/constant"
	"go/token"
	gotypes "go/types"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// expr lowers e, emitting any statements it needs first, and applies the
// implicit conversion recorded for e.
func (fl *fnLower) expr(e syntax.Expr) ast.Expr {
	if x, ok := fl.pre[e]; ok {
		return x
	}
	x := fl.expr0(e)
	if targs := fl.info.ValueInsts[e]; targs != nil {
		// A generic function value: cmp.Compare[int].
		idx := make([]ast.Expr, len(targs))
		for i, t := range targs {
			idx[i] = fl.l.typeExpr(t)
		}
		if len(idx) == 1 {
			x = &ast.IndexExpr{X: x, Index: idx[0]}
		} else {
			x = &ast.IndexListExpr{X: x, Indices: idx}
		}
	}
	fl.l.mark(x, e.Span())
	return fl.convert(e, x)
}

// exprAs lowers e for a context of type want (adapting untyped constants
// where Go would otherwise infer a different default type).
func (fl *fnLower) exprAs(e syntax.Expr, want types.Type) ast.Expr {
	return fl.adaptConst(e, fl.expr(e), want)
}

// adaptConst wraps untyped constants in a conversion when boxing them, so
// new(5) for an Int64? becomes new(int64(5)).
func (fl *fnLower) adaptConst(e syntax.Expr, x ast.Expr, want types.Type) ast.Expr {
	return x
}

// convert applies an implicit conversion recorded for e.
func (fl *fnLower) convert(e syntax.Expr, x ast.Expr) ast.Expr {
	switch fl.info.Implicit[e] {
	case check.ConvBox:
		return fl.box(e, x)
	case check.ConvUnbox:
		return &ast.StarExpr{X: x}
	case check.ConvAssert:
		// Smart cast by a type test: the assertion cannot fail here.
		if b := fl.castFor(e); b != nil {
			b.used = true
			return ast.NewIdent(b.ident.Name)
		}
		return &ast.TypeAssertExpr{X: x, Type: fl.l.typeExpr(fl.info.Asserts[e])}
	case check.ConvNilIface:
		src := fl.stash(e, x)
		name := fl.temp("v")
		fl.nilIfaceInto(e, src, name)
		return ast.NewIdent(name)
	}
	return x
}

// exprRaw lowers e without its implicit conversion.
func (fl *fnLower) exprRaw(e syntax.Expr) ast.Expr {
	x := fl.expr0(e)
	fl.l.mark(x, e.Span())
	return x
}

// nilIfaceInto declares name with the interface type that e converts to and
// assigns it src, e's lowered value, unless that is nil. A plain Go
// conversion would wrap a nil pointer in a non-nil interface (the typed-nil
// trap); an optional value goes in unboxed, so an Int? holds an Int:
//
//	var name error
//	if p != nil {
//		name = p
//	}
func (fl *fnLower) nilIfaceInto(e syntax.Expr, src ast.Expr, name string) {
	val := cloneIdentExpr(src)
	if optionalValue(fl.info.TypeOf(e)) {
		val = &ast.StarExpr{X: val}
	}
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(fl.info.IfaceTargets[e])}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, e.Span())
	then := &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}}}}
	fl.emitSynth(&ast.IfStmt{Cond: nilCmp(cloneIdentExpr(src), token.NEQ), Body: then}, e.Span())
}

// optionalValue reports whether t is an optional value (T? for a value type
// T), which Go represents as *T.
func optionalValue(t types.Type) bool {
	return t != nil && types.IsNullable(t) && !types.IsNilable(t)
}

// convertTo is convert for already-lowered values (kept for symmetry).
func (fl *fnLower) convertTo(e syntax.Expr, x ast.Expr) ast.Expr { return x }

// box creates an optional value from x: &T{...} for composite literals,
// new(x) otherwise (Go 1.26).
func (fl *fnLower) box(e syntax.Expr, x ast.Expr) ast.Expr {
	if cl, ok := x.(*ast.CompositeLit); ok {
		return &ast.UnaryExpr{Op: token.AND, X: cl}
	}
	if tv := fl.info.Types[e]; tv.Value != nil {
		// Typed constant: make the type explicit unless it is the default.
		want := types.Lower(tv.Type)
		if nat := sourceConstType(fl.info, e); nat == nil || !gotypes.Identical(gotypes.Default(nat), want) {
			x = &ast.CallExpr{Fun: fl.l.typeExpr(tv.Type), Args: []ast.Expr{x}}
		}
	}
	return &ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{x}}
}

// exprList lowers operands left to right. When a later operand emits
// statements, earlier operands with side effects are spilled into
// temporaries first so that evaluation order is preserved. lvalue marks
// operands that must stay addressable (assignment targets).
func (fl *fnLower) exprList(es []syntax.Expr, lvalue []bool) []ast.Expr {
	out := make([]ast.Expr, len(es))
	for i, e := range es {
		mark := len(*fl.stmts)
		var x ast.Expr
		if lvalue != nil && lvalue[i] {
			x = fl.lvalue(e)
		} else {
			x = fl.expr(e)
		}
		if len(*fl.stmts) > mark && i > 0 {
			var spills []ast.Stmt
			for j := 0; j < i; j++ {
				if lvalue != nil && lvalue[j] {
					// An assignment target stays addressable: only the calls
					// inside it run first (xs[i()] = f()?).
					out[j] = fl.spillTarget(es[j], out[j], &spills)
					continue
				}
				if fl.pure(es[j], out[j]) || fl.callFree(es[j]) {
					continue
				}
				tmp := fl.temp("v")
				spill := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{out[j]}}
				fl.l.mark(spill, es[j].Span())
				spills = append(spills, spill)
				out[j] = ast.NewIdent(tmp)
			}
			if len(spills) > 0 {
				rest := append([]ast.Stmt(nil), (*fl.stmts)[mark:]...)
				*fl.stmts = append(append((*fl.stmts)[:mark], spills...), rest...)
			}
		}
		out[i] = x
	}
	return out
}

// lvalue lowers an assignment target.
func (fl *fnLower) lvalue(e syntax.Expr) ast.Expr {
	if id, ok := syntax.Unparen(e).(*syntax.Ident); ok {
		x := fl.ident(id)
		fl.l.mark(x, e.Span())
		return x
	}
	if ix, ok := syntax.Unparen(e).(*syntax.IndexExpr); ok && fl.info.IndexInsts[ix] == nil {
		// m[k] = v: the element itself, never a boxed read.
		xs := fl.exprList([]syntax.Expr{ix.X, ix.Index}, nil)
		x := &ast.IndexExpr{X: xs[0], Index: xs[1]}
		fl.l.mark(x, e.Span())
		return x
	}
	x := fl.expr0(e)
	fl.l.mark(x, e.Span())
	return x
}

// pure reports whether a lowered operand can be evaluated after other
// operands' side effects without changing the result.
func (fl *fnLower) pure(e syntax.Expr, x ast.Expr) bool {
	if tv, ok := fl.info.Types[e]; ok && tv.Value != nil {
		return true
	}
	switch x := x.(type) {
	case *ast.BasicLit, *ast.FuncLit:
		return true
	case *ast.Ident:
		id := identOf(e)
		if id == nil {
			// A compound Lyth expression lowered to a bare identifier is a
			// temporary the lowering just assigned, or `this`.
			return true
		}
		switch s := fl.info.Uses[id].(type) {
		case *check.VarSym:
			return s.Settled()
		case *check.ConstSym, *check.FuncSym:
			return true
		}
		return x.Name == "nil" || x.Name == "true" || x.Name == "false"
	case *ast.SelectorExpr:
		// fmt.Println, time.Second: a package's function or constant.
		if sel, ok := syntax.Unparen(e).(*syntax.SelectorExpr); ok {
			if id := identOf(sel.X); id != nil {
				if _, isPkg := fl.info.Uses[id].(*check.PkgSym); isPkg {
					if obj, ok := fl.info.Uses[sel.Sel].(*check.GoObj); ok {
						switch obj.Obj.(type) {
						case *gotypes.Func, *gotypes.Const, *gotypes.TypeName:
							return true
						}
						return false
					}
					switch fl.info.Uses[sel.Sel].(type) {
					case *check.FuncSym, *check.ConstSym:
						return true
					}
				}
			}
		}
	}
	return false
}

// spillTarget evaluates the calls inside the assignment target e (lowered
// to g) into temporaries appended to spills, keeping the target
// addressable: the index of xs[i()], the map key of m[k()], the pointer of
// p().f or *p(). Go evaluates those operands before the right side.
func (fl *fnLower) spillTarget(e syntax.Expr, g ast.Expr, spills *[]ast.Stmt) ast.Expr {
	if fl.callFree(e) {
		return g
	}
	spill := func(syn syntax.Expr, x ast.Expr) ast.Expr {
		tmp := fl.temp("v")
		st := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{x}}
		fl.l.mark(st, syn.Span())
		*spills = append(*spills, st)
		return ast.NewIdent(tmp)
	}
	for {
		p, ok := g.(*ast.ParenExpr)
		if !ok {
			break
		}
		g = p.X
	}
	switch s := syntax.Unparen(e).(type) {
	case *syntax.IndexExpr:
		gi, ok := g.(*ast.IndexExpr)
		if !ok {
			return g
		}
		if !fl.callFree(s.X) {
			if _, isArray := types.Base(fl.info.TypeOf(s.X)).(*types.Array); isArray {
				gi.X = fl.spillTarget(s.X, gi.X, spills) // the array itself, not a copy
			} else {
				gi.X = spill(s.X, gi.X) // a slice or map: the same backing store
			}
		}
		if !fl.callFree(s.Index) {
			gi.Index = spill(s.Index, gi.Index)
		}
		return gi
	case *syntax.SelectorExpr:
		gs, ok := g.(*ast.SelectorExpr)
		if !ok {
			return g
		}
		if _, isPtr := types.Base(fl.info.TypeOf(s.X)).(*types.Pointer); isPtr {
			gs.X = spill(s.X, gs.X)
		} else {
			gs.X = fl.spillTarget(s.X, gs.X, spills)
		}
		return gs
	case *syntax.UnaryExpr:
		if gs, ok := g.(*ast.StarExpr); ok && s.Op == syntax.MUL {
			gs.X = spill(s.X, gs.X)
			return gs
		}
	}
	return g
}

// callFree reports whether evaluating e calls nothing: no function or
// method call, receive, ? or catch. Go runs the calls of an expression in
// lexical order and reads plain operands (variables, fields, elements) after
// them, so an operand before a call that lowering hoists into statements is
// evaluated first only when it calls something; a plain read stays where Go
// reads it. total + f()? then reads total after f, as total + f() does
// (language.md §6, ADR 0045).
func (fl *fnLower) callFree(e syntax.Expr) bool {
	free := true
	syntax.Walk(e, func(n syntax.Node) {
		if !free {
			return
		}
		switch n := n.(type) {
		case *syntax.CallExpr:
			ci := fl.info.Calls[n]
			if ci == nil {
				free = false
				return
			}
			switch ci.Kind {
			case check.CallConstruct, check.CallConvert:
			case check.CallBuiltin:
				switch ci.Builtin {
				case check.BuiltinLen, check.BuiltinCap, check.BuiltinMin, check.BuiltinMax:
				default:
					free = false
				}
			default:
				free = false
			}
		case *syntax.TryExpr, *syntax.CatchExpr, *syntax.IfExpr, *syntax.WhenExpr:
			free = false
		}
	})
	return free
}

func identOf(e syntax.Expr) *syntax.Ident {
	id, _ := syntax.Unparen(e).(*syntax.Ident)
	return id
}

func (fl *fnLower) expr0(e syntax.Expr) ast.Expr {
	if entry := fl.info.Entries[e]; entry != nil {
		return fl.l.entryRef(entry)
	}
	switch e := e.(type) {
	case *syntax.Ident:
		return fl.ident(e)
	case *syntax.BasicLit:
		return fl.basicLit(e)
	case *syntax.TemplateLit:
		return fl.template(e)
	case *syntax.BoolLit:
		if tv := fl.info.Types[e]; tv.Value != nil {
			return ast.NewIdent(strconv.FormatBool(constant.BoolVal(tv.Value)))
		}
		return ast.NewIdent(strconv.FormatBool(e.Value))
	case *syntax.EmptyStructLit:
		return &ast.CompositeLit{Type: emptyStructType()}
	case *syntax.NullLit:
		return ast.NewIdent("nil")
	case *syntax.ThisExpr:
		return ast.NewIdent(fl.recvName)
	case *syntax.ParenExpr:
		return &ast.ParenExpr{X: fl.expr(e.X)}
	case *syntax.UnaryExpr:
		return fl.unary(e)
	case *syntax.BinaryExpr:
		return fl.binary(e)
	case *syntax.ElvisExpr:
		return fl.valueViaTemp(e)
	case *syntax.CallExpr:
		return fl.call(e)
	case *syntax.SelectorExpr:
		return fl.selector(e)
	case *syntax.IndexExpr:
		if inst := fl.info.IndexInsts[e]; inst != nil {
			return fl.expr(inst)
		}
		xs := fl.exprList([]syntax.Expr{e.X, e.Index}, nil)
		ix := &ast.IndexExpr{X: xs[0], Index: xs[1]}
		if m, ok := check.CollectionOf(fl.info.TypeOf(e.X)).(*types.Map); ok && check.Zeroless(m.Elem) {
			return fl.boxRead(ix, m.Elem, e.S)
		}
		return ix
	case *syntax.SliceExpr:
		ops := []syntax.Expr{e.X}
		if e.Lo != nil {
			ops = append(ops, e.Lo)
		}
		if e.Hi != nil {
			ops = append(ops, e.Hi)
		}
		xs := fl.exprList(ops, nil)
		se := &ast.SliceExpr{X: xs[0]}
		k := 1
		if e.Lo != nil {
			se.Low = xs[k]
			k++
		}
		if e.Hi != nil {
			se.High = xs[k]
		}
		return se
	case *syntax.TryExpr:
		return fl.tryValue(e)
	case *syntax.CatchExpr:
		return fl.valueViaTemp(e)
	case *syntax.NotNullExpr:
		return fl.notNull(e)
	case *syntax.IfExpr, *syntax.WhenExpr:
		return fl.valueViaTemp(e)
	case *syntax.LambdaExpr:
		return fl.lambda(e)
	case *syntax.ListLit:
		return fl.listLit(e)
	case *syntax.MapLit:
		return fl.mapLit(e)
	case *syntax.JumpExpr:
		fl.jump(e)
		return ast.NewIdent("nil")
	case *syntax.InstantiateExpr:
		if targs, ok := fl.info.FuncInsts[e]; ok {
			// errors.AsType<*NotFound> -> errors.AsType[*NotFound]
			x := fl.expr(e.X)
			idx := make([]ast.Expr, len(targs))
			for i, t := range targs {
				idx[i] = fl.l.typeExpr(t)
			}
			if len(idx) == 1 {
				return &ast.IndexExpr{X: x, Index: idx[0]}
			}
			return &ast.IndexListExpr{X: x, Indices: idx}
		}
		if t := fl.info.TypeOf(e); t != nil {
			return fl.l.typeExpr(t)
		}
	case *syntax.TypeOperand:
		if t := fl.info.TypeOf(e); t != nil {
			return fl.l.typeExpr(t)
		}
	case *syntax.IsExpr:
		return fl.isExpr(e)
	case *syntax.InExpr:
		return fl.inExpr(e)
	case *syntax.AsExpr:
		return fl.asExpr(e)
	}
	return ast.NewIdent("nil")
}

// valueViaTemp lowers an expression that needs control flow into a
// temporary and returns the temporary.
func (fl *fnLower) valueViaTemp(e syntax.Expr) ast.Expr {
	t := fl.info.TypeOf(e)
	if t == nil || t == types.Nothing || t == types.Unit {
		fl.exprStmt(e)
		return ast.NewIdent("nil")
	}
	name := fl.temp(tempBase(e))
	if c, ok := syntax.Unparen(e).(*syntax.CatchExpr); ok && fl.simpleCatch(c) {
		if info := fl.info.Tries[c]; info != nil && len(info.Success) == 1 {
			fl.catchInto(c, name, true, c.S)
			return ast.NewIdent(name)
		}
	}
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(t)}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, e.Span())
	fl.exprInto(e, ast.NewIdent(name), t)
	return ast.NewIdent(name)
}

func tempBase(e syntax.Expr) string {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.CallExpr:
		return calleeBase(e.Fun)
	case *syntax.TryExpr:
		if c, ok := syntax.Unparen(e.X).(*syntax.CallExpr); ok {
			return calleeBase(c.Fun)
		}
	case *syntax.CatchExpr:
		if c, ok := syntax.Unparen(e.X).(*syntax.CallExpr); ok {
			return calleeBase(c.Fun)
		}
	case *syntax.SelectorExpr:
		return lowerFirst(e.Sel.Name)
	}
	return "v"
}

func calleeBase(f syntax.Expr) string {
	switch f := syntax.Unparen(f).(type) {
	case *syntax.Ident:
		return lowerFirst(f.Name)
	case *syntax.SelectorExpr:
		name := f.Sel.Name
		for _, p := range []string{"Get", "get", "Find", "find", "Load", "load", "Read", "read", "Parse", "parse", "New", "new"} {
			if rest, ok := strings.CutPrefix(name, p); ok && rest != "" {
				return lowerFirst(rest)
			}
		}
		return lowerFirst(name)
	}
	return "v"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	// Lower the leading run of upper-case letters (URL -> url, HTTPClient -> httpClient).
	r := []rune(s)
	i := 0
	for i < len(r) && r[i] >= 'A' && r[i] <= 'Z' {
		i++
	}
	switch {
	case i == 0:
		return s
	case i == 1 || i == len(r):
		return strings.ToLower(string(r[:i])) + string(r[i:])
	default:
		return strings.ToLower(string(r[:i-1])) + string(r[i-1:])
	}
}

// ---------------------------------------------------------------------------
// Identifiers and literals

func (fl *fnLower) ident(e *syntax.Ident) ast.Expr {
	switch s := fl.info.Uses[e].(type) {
	case *check.VarSym:
		switch s.Kind {
		case check.GlobalVar:
			return fl.pkgRef(s.Pkg, s.GoName)
		case check.ReceiverVar:
			return ast.NewIdent(fl.recvName)
		}
		if name, ok := fl.locals[s]; ok {
			return ast.NewIdent(name)
		}
		return ast.NewIdent(fl.declareLocal(s))
	case *check.ConstSym:
		return fl.pkgRef(s.Pkg, s.GoName)
	case *check.FuncSym:
		return fl.pkgRef(s.Pkg, s.GoName)
	case *check.MemberSym:
		return &ast.SelectorExpr{X: ast.NewIdent(fl.recvName), Sel: ast.NewIdent(s.GoName)}
	case *check.TypeSym:
		return fl.l.typeExpr(s.Type)
	}
	return ast.NewIdent(e.Name)
}

func (fl *fnLower) pkgRef(p *check.Package, name string) ast.Expr {
	if p == nil || p.Path == fl.l.pkg.Path {
		return ast.NewIdent(name)
	}
	return &ast.SelectorExpr{X: ast.NewIdent(fl.l.useImport(p.Path, p.Name)), Sel: ast.NewIdent(name)}
}

func (fl *fnLower) basicLit(e *syntax.BasicLit) ast.Expr {
	switch e.Kind {
	case syntax.INT:
		return &ast.BasicLit{Kind: token.INT, Value: e.Raw}
	case syntax.FLOAT:
		return &ast.BasicLit{Kind: token.FLOAT, Value: e.Raw}
	case syntax.RUNE:
		r, _ := utf8.DecodeRuneInString(e.Value)
		return &ast.BasicLit{Kind: token.CHAR, Value: strconv.QuoteRune(r)}
	}
	if e.Kind == syntax.RAWSTRING {
		return goRawString(e.Value)
	}
	return goString(e.Value)
}

// goString returns an interpreted Go string literal: "line\n".
func goString(s string) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(s)}
}

// goRawString returns a Go raw string literal for the text of a Lyth raw
// string, keeping its line breaks, or an interpreted one when Go's raw
// strings cannot hold the text.
func goRawString(s string) *ast.BasicLit {
	if strings.ContainsAny(s, "`\r") || !utf8.ValidString(s) {
		return goString(s)
	}
	return &ast.BasicLit{Kind: token.STRING, Value: "`" + s + "`"}
}

// template lowers a string template to concatenation, converting numbers
// with strconv and other values with fmt.Sprint.
func (fl *fnLower) template(e *syntax.TemplateLit) ast.Expr {
	var parts []ast.Expr
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			parts = append(parts, goString(text.String()))
			text.Reset()
		}
	}
	var exprs []syntax.Expr
	for _, p := range e.Parts {
		if p.X != nil {
			exprs = append(exprs, p.X)
		}
	}
	lowered := fl.exprList(exprs, nil)
	k := 0
	for _, p := range e.Parts {
		if p.X == nil {
			text.WriteString(p.Text)
			continue
		}
		tv := fl.info.Types[p.X]
		x := lowered[k]
		k++
		if tv.Value != nil && fl.foldableConst(p.X, tv.Type) {
			switch tv.Value.Kind() {
			case constant.String:
				text.WriteString(constant.StringVal(tv.Value))
				continue
			case constant.Int, constant.Bool:
				if !types.Identical(types.Base(tv.Type), types.Rune) {
					text.WriteString(tv.Value.ExactString())
					continue
				}
			}
		}
		flush()
		parts = append(parts, fl.stringify(p.X, x, tv.Type))
	}
	flush()
	if len(parts) == 0 {
		return goString("")
	}
	out := parts[0]
	if len(parts) == 1 {
		if _, isLit := out.(*ast.BasicLit); !isLit && !types.IsString(types.Base(fl.info.TypeOf(exprs[0]))) {
			return out
		}
		return out
	}
	for _, p := range parts[1:] {
		out = &ast.BinaryExpr{X: out, Op: token.ADD, Y: p}
	}
	return out
}

// stringify converts a lowered value to a string expression.
func (fl *fnLower) stringify(e syntax.Expr, x ast.Expr, t types.Type) ast.Expr {
	if !types.IsNullable(t) && hasStringMethod(t) {
		// Like fmt, a type with its own String method (time.Duration,
		// slog.Level, a Lyth type implementing fmt.Stringer) prints with it.
		return &ast.CallExpr{Fun: &ast.SelectorExpr{X: x, Sel: ast.NewIdent("String")}}
	}
	if types.IsNullable(t) {
		// Lyth prints null for absent values.
		tmp := fl.temp("s")
		inner := types.Base(t)
		val := x
		if !types.IsNilable(inner) {
			val = &ast.StarExpr{X: x}
		}
		if !fl.pure(e, x) {
			v := fl.temp("v")
			fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(v)}, Tok: token.DEFINE, Rhs: []ast.Expr{x}}, e.Span())
			x = ast.NewIdent(v)
			val = x
			if !types.IsNilable(inner) {
				val = &ast.StarExpr{X: ast.NewIdent(v)}
			}
		}
		decl := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{goString("null")}}
		fl.emitSynth(decl, e.Span())
		ifs := &ast.IfStmt{Cond: &ast.BinaryExpr{X: cloneIdentExpr(x), Op: token.NEQ, Y: ast.NewIdent("nil")},
			Body: &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.ASSIGN, Rhs: []ast.Expr{fl.stringify(e, val, inner)}}}}}
		fl.emitSynth(ifs, e.Span())
		return ast.NewIdent(tmp)
	}
	b := types.BasicOf(t)
	strconvPkg := func() string { return fl.l.useImport("strconv", "strconv") }
	if b != nil {
		named := !isBasicType(types.Base(t))
		switch {
		case types.IsString(b):
			if named {
				return &ast.CallExpr{Fun: ast.NewIdent("string"), Args: []ast.Expr{x}}
			}
			return x
		case b == types.Rune || b.Kind == types.Int32Kind && b.Name == "rune":
			return &ast.CallExpr{Fun: ast.NewIdent("string"), Args: []ast.Expr{x}}
		case b.Kind == types.IntKind && !named:
			return &ast.CallExpr{Fun: sel(strconvPkg(), "Itoa"), Args: []ast.Expr{x}}
		case types.IsInteger(b) && !types.IsUnsigned(b):
			if b.Kind != types.Int64Kind || named {
				x = &ast.CallExpr{Fun: ast.NewIdent("int64"), Args: []ast.Expr{x}}
			}
			return &ast.CallExpr{Fun: sel(strconvPkg(), "FormatInt"), Args: []ast.Expr{x, intLit(10)}}
		case types.IsUnsigned(b):
			if b.Kind != types.UInt64Kind || named {
				x = &ast.CallExpr{Fun: ast.NewIdent("uint64"), Args: []ast.Expr{x}}
			}
			return &ast.CallExpr{Fun: sel(strconvPkg(), "FormatUint"), Args: []ast.Expr{x, intLit(10)}}
		case b.Kind == types.Float64Kind:
			if named {
				x = &ast.CallExpr{Fun: ast.NewIdent("float64"), Args: []ast.Expr{x}}
			}
			return &ast.CallExpr{Fun: sel(strconvPkg(), "FormatFloat"), Args: []ast.Expr{x, &ast.BasicLit{Kind: token.CHAR, Value: "'g'"}, intLit(-1), intLit(64)}}
		case b.Kind == types.Float32Kind:
			return &ast.CallExpr{Fun: sel(strconvPkg(), "FormatFloat"), Args: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("float64"), Args: []ast.Expr{x}}, &ast.BasicLit{Kind: token.CHAR, Value: "'g'"}, intLit(-1), intLit(32)}}
		case types.IsBoolean(b) && !named:
			return &ast.CallExpr{Fun: sel(strconvPkg(), "FormatBool"), Args: []ast.Expr{x}}
		}
	}
	if types.IsErrorType(t) && !types.MayBeNull(t) {
		return &ast.CallExpr{Fun: &ast.SelectorExpr{X: x, Sel: ast.NewIdent("Error")}}
	}
	return &ast.CallExpr{Fun: sel(fl.l.useImport("fmt", "fmt"), "Sprint"), Args: []ast.Expr{x}}
}

func isBasicType(t types.Type) bool {
	_, ok := t.(*types.Basic)
	return ok
}

func cloneIdentExpr(x ast.Expr) ast.Expr {
	if id, ok := x.(*ast.Ident); ok {
		return ast.NewIdent(id.Name)
	}
	return x
}

// ---------------------------------------------------------------------------
// Operators

func (fl *fnLower) unary(e *syntax.UnaryExpr) ast.Expr {
	switch e.Op {
	case syntax.AND:
		x := fl.expr(e.X)
		if cl, ok := x.(*ast.CompositeLit); ok {
			return &ast.UnaryExpr{OpPos: fl.l.pos(e.S), Op: token.AND, X: cl}
		}
		return &ast.UnaryExpr{Op: token.AND, X: x}
	case syntax.MUL:
		return &ast.StarExpr{X: fl.expr(e.X)}
	}
	x := fl.expr(e.X)
	op := map[syntax.Kind]token.Token{syntax.SUB: token.SUB, syntax.ADD: token.ADD, syntax.NOT: token.NOT, syntax.XOR: token.XOR}[e.Op]
	if inner, ok := x.(*ast.UnaryExpr); ok && inner.Op == op && (op == token.SUB || op == token.ADD) {
		x = &ast.ParenExpr{X: x} // avoid "--x" reading as a decrement
	}
	return &ast.UnaryExpr{Op: op, X: x}
}

var goBinOps = map[syntax.Kind]token.Token{
	syntax.ADD: token.ADD, syntax.SUB: token.SUB, syntax.MUL: token.MUL, syntax.QUO: token.QUO, syntax.REM: token.REM,
	syntax.AND: token.AND, syntax.AND_NOT: token.AND_NOT, syntax.OR: token.OR, syntax.XOR: token.XOR, syntax.SHL: token.SHL, syntax.SHR: token.SHR,
	syntax.EQL: token.EQL, syntax.NEQ: token.NEQ, syntax.LSS: token.LSS, syntax.LEQ: token.LEQ,
	syntax.GTR: token.GTR, syntax.GEQ: token.GEQ, syntax.LAND: token.LAND, syntax.LOR: token.LOR,
}

// goPrec returns Go's precedence for a binary token.
func goPrec(op token.Token) int { return op.Precedence() }

func (fl *fnLower) binary(e *syntax.BinaryExpr) ast.Expr {
	if e.Op == syntax.LAND || e.Op == syntax.LOR {
		return fl.logical(e)
	}
	if e.Op == syntax.EQL || e.Op == syntax.NEQ {
		if x := fl.optionalEquality(e); x != nil {
			return x
		}
	}
	xs := fl.exprList([]syntax.Expr{e.X, e.Y}, nil)
	op := goBinOps[e.Op]
	return &ast.BinaryExpr{X: fl.parenFor(xs[0], op, false), Op: op, Y: fl.parenFor(xs[1], op, true)}
}

// parenFor parenthesizes an operand whose Go precedence is lower than op's
// (Lyth splits comparison levels that Go merges).
func (fl *fnLower) parenFor(x ast.Expr, op token.Token, right bool) ast.Expr {
	if b, ok := x.(*ast.BinaryExpr); ok {
		p := goPrec(b.Op)
		if p < goPrec(op) || right && p == goPrec(op) {
			return &ast.ParenExpr{X: x}
		}
	}
	return x
}

// logical lowers && and ||, keeping short-circuit evaluation when the right
// operand needs statements.
func (fl *fnLower) logical(e *syntax.BinaryExpr) ast.Expr {
	x := fl.expr(e.X)
	sub, stmts := fl.sub()
	y := sub.expr(e.Y)
	op := goBinOps[e.Op]
	if len(*stmts) == 0 {
		return &ast.BinaryExpr{X: fl.parenFor(x, op, false), Op: op, Y: fl.parenFor(y, op, true)}
	}
	t := fl.temp("ok")
	fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(t)}, Tok: token.DEFINE, Rhs: []ast.Expr{x}}, e.X.Span())
	var cond ast.Expr = ast.NewIdent(t)
	if e.Op == syntax.LOR {
		cond = &ast.UnaryExpr{Op: token.NOT, X: ast.NewIdent(t)}
	}
	body := append(*stmts, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(t)}, Tok: token.ASSIGN, Rhs: []ast.Expr{y}})
	ifs := &ast.IfStmt{Cond: cond, Body: &ast.BlockStmt{List: body}}
	fl.emitSynth(ifs, e.Y.Span())
	return ast.NewIdent(t)
}

// optionalEquality lowers == and != involving optional values (T? for
// value types), comparing contents with null handling.
func (fl *fnLower) optionalEquality(e *syntax.BinaryExpr) ast.Expr {
	xt, yt := fl.info.TypeOf(e.X), fl.info.TypeOf(e.Y)
	isOpt := func(t types.Type) bool { return t != nil && types.IsNullable(t) && !types.IsNilable(t) }
	if xt == types.Null || yt == types.Null || !(isOpt(xt) || isOpt(yt)) {
		return nil
	}
	xs := fl.exprList([]syntax.Expr{e.X, e.Y}, nil)
	// An optional operand is read twice; the other once, after the nil
	// check, so only an impure one is evaluated first: n == 3 stays n == 3.
	keep := func(e syntax.Expr, x ast.Expr, opt bool) ast.Expr {
		if opt || !fl.pure(e, x) {
			return fl.stash(e, x)
		}
		return x
	}
	a, b := keep(e.X, xs[0], isOpt(xt)), keep(e.Y, xs[1], isOpt(yt))
	var eq ast.Expr
	switch {
	case isOpt(xt) && isOpt(yt):
		// (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
		bothNil := &ast.BinaryExpr{X: nilCmp(a, token.EQL), Op: token.LAND, Y: nilCmp(b, token.EQL)}
		both := &ast.BinaryExpr{X: &ast.BinaryExpr{X: nilCmp(a, token.NEQ), Op: token.LAND, Y: nilCmp(b, token.NEQ)}, Op: token.LAND,
			Y: &ast.BinaryExpr{X: &ast.StarExpr{X: a}, Op: token.EQL, Y: &ast.StarExpr{X: b}}}
		eq = &ast.BinaryExpr{X: &ast.ParenExpr{X: bothNil}, Op: token.LOR, Y: &ast.ParenExpr{X: both}}
	case isOpt(xt), isOpt(yt):
		// n == 3: n != nil && *n == 3; n != 3: n == nil || *n != 3.
		opt := a
		var lhs, rhs ast.Expr = &ast.StarExpr{X: a}, b
		if !isOpt(xt) {
			opt = b
			lhs, rhs = a, &ast.StarExpr{X: b}
		}
		if e.Op == syntax.NEQ {
			return &ast.BinaryExpr{X: nilCmp(opt, token.EQL), Op: token.LOR, Y: &ast.BinaryExpr{X: lhs, Op: token.NEQ, Y: rhs}}
		}
		return &ast.BinaryExpr{X: nilCmp(opt, token.NEQ), Op: token.LAND, Y: &ast.BinaryExpr{X: lhs, Op: token.EQL, Y: rhs}}
	}
	if e.Op == syntax.NEQ {
		return &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: eq}}
	}
	return eq
}

func nilCmp(x ast.Expr, op token.Token) ast.Expr {
	return &ast.BinaryExpr{X: cloneIdentExpr(x), Op: op, Y: ast.NewIdent("nil")}
}

// stash stores a value in a temporary unless it is a plain identifier, so
// it can be referenced several times.
func (fl *fnLower) stash(e syntax.Expr, x ast.Expr) ast.Expr {
	if id, ok := x.(*ast.Ident); ok {
		return id
	}
	tmp := fl.temp("v")
	fl.emit(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{x}}, e.Span())
	return ast.NewIdent(tmp)
}

// ---------------------------------------------------------------------------
// Null handling

// elvisInto assigns `x ?: y` to target.
func (fl *fnLower) elvisInto(e *syntax.ElvisExpr, target ast.Expr, targetType types.Type) {
	if fl.elvisSafeChain(e, target, targetType) {
		return
	}
	xt := fl.info.TypeOf(e.X)
	if xt != nil && !types.MayBeNull(xt) && !types.IsNilable(xt) && !isCollection(xt) {
		// The checker warned that the left side is never null (a string,
		// a struct): Go cannot compare it with nil, and the right side
		// never runs.
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{target}, Tok: token.ASSIGN, Rhs: []ast.Expr{fl.expr(e.X)}}, e.S)
		return
	}
	x := fl.stash(e.X, fl.expr(e.X))
	// a ?: b with b optional too is optional: it keeps a's box.
	unbox := optionalValue(xt) && !optionalValue(fl.info.TypeOf(e))
	val := ast.Expr(cloneIdentExpr(x))
	if unbox {
		val = &ast.StarExpr{X: cloneIdentExpr(x)}
	}
	if fl.info.TypeOf(e.Y) == types.Nothing {
		sub, stmts := fl.sub()
		sub.exprStmt(e.Y)
		fl.emitSynthShallow(&ast.IfStmt{If: fl.l.pos(e.S), Cond: nilCmp(x, token.EQL), Body: &ast.BlockStmt{List: *stmts}}, e.OpSpan)
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{target}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}}, e.S)
		return
	}
	then := &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{target}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}}}}
	sub, stmts := fl.sub()
	sub.exprInto(e.Y, cloneIdentExpr(target), targetType)
	markTree(fl.l, then, e.OpSpan)
	ifs := &ast.IfStmt{If: fl.l.pos(e.S), Cond: nilCmp(x, token.NEQ), Body: then, Else: &ast.BlockStmt{List: *stmts}}
	fl.emitSynthShallow(ifs, e.OpSpan)
}

// elvisSafeChain lowers `a?.f ?: y` without boxing f: the last hop of the
// safe chain assigns straight into the target.
func (fl *fnLower) elvisSafeChain(e *syntax.ElvisExpr, target ast.Expr, targetType types.Type) bool {
	selExpr, ok := syntax.Unparen(e.X).(*syntax.SelectorExpr)
	if !ok || fl.info.TypeOf(e.Y) == types.Nothing {
		return false
	}
	sel := fl.info.Selections[selExpr]
	if sel == nil || !sel.Safe || sel.Kind != check.SelField || fl.info.Implicit[e.X] != check.ConvNone {
		return false
	}
	if types.MayBeNull(sel.Type) {
		return false // the field itself may be null: keep the general form
	}
	recv := fl.stash(selExpr.X, fl.receiver(selExpr))
	val := &ast.SelectorExpr{X: cloneIdentExpr(recv), Sel: ast.NewIdent(sel.GoName)}
	then := &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{target}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}}}}
	markTree(fl.l, then, selExpr.S)
	sub, stmts := fl.sub()
	sub.exprInto(e.Y, cloneIdentExpr(target), targetType)
	fl.emitSynthShallow(&ast.IfStmt{If: fl.l.pos(e.S), Cond: nilCmp(recv, token.NEQ), Body: then, Else: &ast.BlockStmt{List: *stmts}}, e.OpSpan)
	return true
}

func (fl *fnLower) notNull(e *syntax.NotNullExpr) ast.Expr {
	xt := fl.info.TypeOf(e.X)
	x := fl.stash(e.X, fl.expr(e.X))
	if xt != nil && types.MayBeNull(xt) {
		pos := e.OpSpan.StartPos()
		msg := "lyth: unexpected null (" + e.OpSpan.File.Base() + ":" + strconv.Itoa(pos.Line) + ":" + strconv.Itoa(pos.Col) + ")"
		ifs := &ast.IfStmt{Cond: nilCmp(x, token.EQL), Body: &ast.BlockStmt{List: []ast.Stmt{
			&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("panic"), Args: []ast.Expr{strLit(msg)}}}}}}
		fl.emitSynth(ifs, e.OpSpan)
		if types.IsNullable(xt) && !types.IsNilable(xt) {
			return &ast.StarExpr{X: cloneIdentExpr(x)}
		}
	}
	return cloneIdentExpr(x)
}

// ---------------------------------------------------------------------------
// Errors: ? and catch

// simpleTry reports whether e's operand is a call (or error value) that
// can be assigned directly with :=.
func (fl *fnLower) simpleTry(e *syntax.TryExpr) bool {
	return fl.info.Tries[e] != nil
}

func (fl *fnLower) simpleCatch(e *syntax.CatchExpr) bool {
	return fl.info.Tries[e] != nil
}

// tryValue lowers `x?` in expression position and returns the success value.
func (fl *fnLower) tryValue(e *syntax.TryExpr) ast.Expr {
	info := fl.info.Tries[e]
	if info == nil {
		return ast.NewIdent("nil")
	}
	switch len(info.Success) {
	case 0:
		fl.tryInto(e, nil, false, e.S)
		return ast.NewIdent("nil")
	case 1:
		name := fl.temp(tempBase(e))
		fl.tryInto(e, []string{name}, true, e.S)
		return ast.NewIdent(name)
	}
	names := make([]string, len(info.Success))
	for i := range names {
		names[i] = fl.temp("v")
	}
	fl.tryInto(e, names, true, e.S)
	return ast.NewIdent(names[0])
}

// tryInto lowers `x?`, binding the success values to names (which are new
// variables when define is set; "_" discards).
//
//	v, err := call
//	if err != nil {
//		return <zero values>, err
//	}
func (fl *fnLower) tryInto(e *syntax.TryExpr, names []string, define bool, span source.Span) {
	info := fl.info.Tries[e]
	if info == nil {
		return
	}
	if clauses := typedChain(e.X); clauses != nil {
		fl.catchChain(clauses, nil, e.OpSpan, names, define, fl.info.TypeOf(e), span)
		return
	}
	operand := fl.expr(e.X)
	opSpan := e.OpSpan
	if _, isResult := fl.info.TypeOf(e.X).(*types.Result); !isResult {
		// `err?` on an Error value.
		v := fl.stash(e.X, operand)
		fl.emitSynth(&ast.IfStmt{If: fl.l.pos(span), Cond: nilCmp(v, token.NEQ), Body: &ast.BlockStmt{List: fl.failStmts(cloneIdentExpr(v))}}, opSpan)
		return
	}
	errName := fl.errVar()
	discard := true
	for _, n := range names {
		discard = discard && n == "_"
	}
	if len(info.Success) == 0 || discard {
		// if err := call; err != nil { return ..., err }
		// if _, err := call; err != nil { return ..., err } (value discarded)
		init := &ast.AssignStmt{Lhs: append(blanks(len(info.Success)), ast.NewIdent(errName)), Tok: token.DEFINE, Rhs: []ast.Expr{operand}}
		fl.l.mark(init, e.X.Span())
		fail := fl.failStmts(ast.NewIdent(errName))
		for _, s := range fail {
			markTree(fl.l, s, opSpan)
		}
		ifs := &ast.IfStmt{If: fl.l.pos(span), Init: init, Cond: nilCmp(ast.NewIdent(errName), token.NEQ), Body: &ast.BlockStmt{List: fail}}
		fl.l.markSynth(ifs.Cond, opSpan)
		fl.emit(ifs, span)
		return
	}
	var lhs []ast.Expr
	for i := range info.Success {
		name := "_"
		if i < len(names) {
			name = names[i]
		}
		lhs = append(lhs, ast.NewIdent(name))
	}
	lhs = append(lhs, ast.NewIdent(errName))
	tok := token.DEFINE
	if !define {
		tok = token.ASSIGN
	}
	fl.emit(&ast.AssignStmt{Lhs: lhs, Tok: tok, TokPos: fl.l.pos(span), Rhs: []ast.Expr{operand}}, span)
	fl.emitSynth(&ast.IfStmt{If: fl.l.pos(span), Cond: nilCmp(ast.NewIdent(errName), token.NEQ), Body: &ast.BlockStmt{List: fl.failStmts(ast.NewIdent(errName))}}, opSpan)
}

// catchInto lowers `x catch (err) { ... }`, binding the value to name
// (declared when define is set; empty name discards the value).
func (fl *fnLower) catchInto(e *syntax.CatchExpr, name string, define bool, span source.Span) {
	info := fl.info.Tries[e]
	if info == nil {
		return
	}
	if clauses := typedChain(e.X); clauses != nil && e.Type == nil {
		var names []string
		if name != "" {
			names = []string{name}
		}
		fl.catchChain(clauses, e, e.CatchSpan, names, define, fl.info.TypeOf(e), span)
		return
	}
	_, isResult := fl.info.TypeOf(e.X).(*types.Result)
	scoped := !isResult || len(info.Success) == 0 || name == ""
	if !scoped && !define {
		// The target exists: catch into a new variable and assign it, so the
		// error variable is declared by := as everywhere else.
		tmp := fl.temp(tempBase(e))
		fl.catchInto(e, tmp, true, span)
		fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN, Rhs: []ast.Expr{ast.NewIdent(tmp)}}, span)
		return
	}
	if rt := fl.info.TypeOf(e); !scoped && len(info.Success) == 1 && boxes(info.Success[0], rt) {
		fl.catchBoxed(e, nil, e.CatchSpan, name, rt, span)
		return
	}
	if name == "" && e.Type == nil && len(e.Body.Stmts) == 0 {
		// `f() catch {}` discards everything, as hand-written Go does:
		// _ = f() (or _, _ = f() with a discarded value).
		n := 1
		if isResult {
			n += len(info.Success)
		}
		fl.emit(&ast.AssignStmt{Lhs: blanks(n), Tok: token.ASSIGN, TokPos: fl.l.pos(span), Rhs: []ast.Expr{fl.expr(e.X)}}, span)
		return
	}
	operand := fl.expr(e.X)
	errName := ""
	if info.Binder != nil {
		errName = fl.catchBinder(info.Binder, e.Body, scoped)
	} else {
		errName = fl.errVar()
	}
	handler := func(target ast.Expr) *ast.BlockStmt { return fl.catchHandler(e, info, target, firstOr(info.Success)) }
	if scoped {
		// if err := call; err != nil { handler }
		// if _, err := call; err != nil { handler } (value discarded)
		n := 0
		if isResult {
			n = len(info.Success)
		}
		init := &ast.AssignStmt{Lhs: append(blanks(n), ast.NewIdent(errName)), Tok: token.DEFINE, Rhs: []ast.Expr{operand}}
		cond := nilCmp(ast.NewIdent(errName), token.NEQ)
		ifs := &ast.IfStmt{If: fl.l.pos(span), Init: init, Cond: cond, Body: handler(nil)}
		fl.l.markSynth(cond, e.CatchSpan)
		fl.l.mark(init, e.X.Span())
		fl.emit(ifs, span)
		return
	}
	lhs := []ast.Expr{ast.NewIdent(name)}
	for i := 1; i < len(info.Success); i++ {
		lhs = append(lhs, ast.NewIdent("_"))
	}
	lhs = append(lhs, ast.NewIdent(errName))
	fl.emit(&ast.AssignStmt{Lhs: lhs, Tok: token.DEFINE, TokPos: fl.l.pos(span), Rhs: []ast.Expr{operand}}, span)
	ifs := &ast.IfStmt{If: fl.l.pos(e.CatchSpan), Cond: nilCmp(ast.NewIdent(errName), token.NEQ), Body: handler(ast.NewIdent(name))}
	fl.emitSynthShallow(ifs, e.CatchSpan)
}

// catchIntoNames lowers a catch of a call with several results, binding
// them to names (declared when define is set; "_" discards). The checker
// requires the handler to exit, since it cannot supply several values:
//
//	a, b, err := call
//	if err != nil {
//		handler
//	}
func (fl *fnLower) catchIntoNames(e *syntax.CatchExpr, names []string, define bool, span source.Span) {
	info := fl.info.Tries[e]
	if info == nil {
		return
	}
	if clauses := typedChain(e.X); clauses != nil && e.Type == nil {
		fl.catchChain(clauses, e, e.CatchSpan, names, define, fl.info.TypeOf(e), span)
		return
	}
	discard := true
	for _, n := range names {
		discard = discard && n == "_"
	}
	if !define && !discard {
		// Existing targets: bind fresh variables, then assign them.
		tmps := make([]string, len(names))
		for i := range names {
			tmps[i] = fl.temp("v")
		}
		fl.catchIntoNames(e, tmps, true, span)
		var lhs, rhs []ast.Expr
		for i, n := range names {
			if n != "_" {
				lhs, rhs = append(lhs, ast.NewIdent(n)), append(rhs, ast.NewIdent(tmps[i]))
			}
		}
		fl.emitSynth(&ast.AssignStmt{Lhs: lhs, Tok: token.ASSIGN, Rhs: rhs}, span)
		return
	}
	operand := fl.expr(e.X)
	errName := ""
	if info.Binder != nil {
		errName = fl.catchBinder(info.Binder, e.Body, discard)
	} else {
		errName = fl.errVar()
	}
	if discard {
		init := &ast.AssignStmt{Lhs: append(blanks(len(info.Success)), ast.NewIdent(errName)), Tok: token.DEFINE, Rhs: []ast.Expr{operand}}
		cond := nilCmp(ast.NewIdent(errName), token.NEQ)
		ifs := &ast.IfStmt{If: fl.l.pos(span), Init: init, Cond: cond, Body: fl.catchHandler(e, info, nil, nil)}
		fl.l.markSynth(cond, e.CatchSpan)
		fl.l.mark(init, e.X.Span())
		fl.emit(ifs, span)
		return
	}
	var lhs []ast.Expr
	for i := range info.Success {
		name := "_"
		if i < len(names) {
			name = names[i]
		}
		lhs = append(lhs, ast.NewIdent(name))
	}
	lhs = append(lhs, ast.NewIdent(errName))
	fl.emit(&ast.AssignStmt{Lhs: lhs, Tok: token.DEFINE, TokPos: fl.l.pos(span), Rhs: []ast.Expr{operand}}, span)
	ifs := &ast.IfStmt{If: fl.l.pos(e.CatchSpan), Cond: nilCmp(ast.NewIdent(errName), token.NEQ), Body: fl.catchHandler(e, info, nil, nil)}
	fl.emitSynthShallow(ifs, e.CatchSpan)
}

// catchHandler lowers a catch block, assigning its value, of type want, to
// target if given. While it does, the binder's Go name is in use: nested
// expansions must not declare it.
func (fl *fnLower) catchHandler(e *syntax.CatchExpr, info *check.TryInfo, target ast.Expr, want types.Type) *ast.BlockStmt {
	if v := info.Binder; v != nil {
		if name, ok := fl.locals[v]; ok {
			fl.live[name]++
			defer func() { fl.live[name]-- }()
		}
	}
	sub, stmts := fl.sub()
	valueExpr := fl.info.BlockValues[e.Body]
	for i, s := range e.Body.Stmts {
		if target != nil && valueExpr != nil && i == len(e.Body.Stmts)-1 {
			if es, ok := s.(*syntax.ExprStmt); ok && es.X == valueExpr {
				if vt := fl.info.TypeOf(valueExpr); boxes(vt, want) && fl.info.Implicit[valueExpr] == check.ConvNone {
					// A typed clause yields the call's type; the chain's value
					// is its optional when the last catch yields null.
					x := sub.box(valueExpr, sub.exprAs(valueExpr, vt))
					sub.emit(&ast.AssignStmt{Lhs: []ast.Expr{target}, Tok: token.ASSIGN, Rhs: []ast.Expr{x}}, valueExpr.Span())
					continue
				}
				sub.exprInto(valueExpr, target, want)
				continue
			}
		}
		sub.stmt(s)
	}
	return &ast.BlockStmt{List: *stmts, Lbrace: fl.l.pos(e.Body.Lbrace), Rbrace: fl.l.pos(e.Body.Rbrace)}
}

// catchBinder names the binder of a catch whose handler is body. scoped
// reports that the error variable is declared in the init of an if
// statement rather than in the enclosing block.
//
// A binder keeps its Lyth name, and binders named err share the function's
// error variable (errVar), as hand-written Go reuses err: the variable is
// assigned by the call, read in the handler, and dead afterwards. A binder
// gets a name of its own when another identifier uses the name, when it
// would hide the binder of an enclosing handler, or when it is declared in
// the enclosing block and a closure in the handler captures it, since a
// later := of the same name would change what the closure sees.
func (fl *fnLower) catchBinder(v *check.VarSym, body *syntax.Block, scoped bool) string {
	if name, ok := fl.locals[v]; ok {
		return name
	}
	name := v.Name()
	if !fl.binderFree(name) || !scoped && fl.captured(v, body) {
		name = fl.freshBinder(name)
	}
	fl.locals[v] = name
	return name
}

// binderFree reports whether a binder may use name as its Go name.
func (fl *fnLower) binderFree(name string) bool {
	return !fl.plain[name] && !fl.reserved[name] && !check.IsGoReserved(name) && fl.live[name] == 0
}

// freshBinder returns an unused Go name for a binder named name.
func (fl *fnLower) freshBinder(name string) string {
	if fl.reserved[name] || check.IsGoReserved(name) {
		return fl.temp(name)
	}
	return fl.alloc(name)
}

// captured reports whether a closure in body (a lambda, or a go or defer
// statement) refers to v.
func (fl *fnLower) captured(v *check.VarSym, body *syntax.Block) bool {
	found := false
	syntax.Walk(body, func(n syntax.Node) {
		switch n.(type) {
		case *syntax.LambdaExpr, *syntax.GoStmt, *syntax.DeferStmt:
			syntax.Walk(n, func(m syntax.Node) {
				if id, ok := m.(*syntax.Ident); ok && fl.info.Uses[id] == check.Symbol(v) {
					found = true
				}
			})
		}
	})
	return found
}

// ---------------------------------------------------------------------------
// Collections and lambdas

func (fl *fnLower) listLit(e *syntax.ListLit) ast.Expr {
	t := fl.info.TypeOf(e)
	elts := fl.exprList(e.Elems, nil)
	var elem types.Type
	switch b := collectionShape(t).(type) {
	case *types.Slice:
		elem = b.Elem
	case *types.Array:
		elem = b.Elem
	}
	for i, x := range e.Elems {
		elts[i] = elideType(fl.adaptConst(x, elts[i], elem), fl.info.TypeOf(x), elem)
	}
	return &ast.CompositeLit{Type: fl.l.typeExpr(t), Elts: elts}
}

// collectionShape returns the slice, array, or map type of a literal of
// type t, looking through a named type such as gin.H.
func collectionShape(t types.Type) types.Type {
	if n, ok := types.Base(t).(*types.Named); ok && (n.Go != nil || n.Decl != nil && n.Decl.Kind == types.DefinedDecl) {
		return check.CollectionOf(t)
	}
	return types.Base(t)
}

// isLit reports whether e is a Lyth collection literal ([a, b], [k: v]).
func isLit(e syntax.Expr) bool {
	switch syntax.Unparen(e).(type) {
	case *syntax.ListLit, *syntax.MapLit:
		return true
	}
	return false
}

// elideType drops the type of a composite literal element whose type is the
// collection's element type, as gofmt -s does: []Point{{X: 1}}, not
// []Point{Point{X: 1}}.
func elideType(x ast.Expr, t, elem types.Type) ast.Expr {
	if cl, ok := x.(*ast.CompositeLit); ok && t != nil && elem != nil && types.Identical(t, elem) {
		cl.Type = nil
	}
	return x
}

func (fl *fnLower) mapLit(e *syntax.MapLit) ast.Expr {
	t := fl.info.TypeOf(e)
	var ops []syntax.Expr
	for i := range e.Keys {
		ops = append(ops, e.Keys[i], e.Values[i])
	}
	xs := fl.exprList(ops, nil)
	cl := &ast.CompositeLit{Type: fl.l.typeExpr(t)}
	var kt, vt types.Type
	if m, ok := collectionShape(t).(*types.Map); ok {
		kt, vt = m.Key, m.Elem
	}
	for i := 0; i < len(xs); i += 2 {
		key := elideType(xs[i], fl.info.TypeOf(ops[i]), kt)
		val := elideType(xs[i+1], fl.info.TypeOf(ops[i+1]), vt)
		cl.Elts = append(cl.Elts, &ast.KeyValueExpr{Key: key, Value: val})
	}
	return cl
}

func (fl *fnLower) lambda(e *syntax.LambdaExpr) ast.Expr {
	info := fl.info.Lambdas[e]
	if info == nil || info.Sig == nil {
		return ast.NewIdent("nil")
	}
	child := fl.child(info.Sig)
	var names []string
	for _, p := range info.Params {
		names = append(names, child.declareLocal(p))
	}
	for len(names) < len(info.Sig.Params) {
		names = append(names, "_")
	}
	var stmts []ast.Stmt
	child.stmts = &stmts
	child.castScope(func() {
		for i, s := range e.Body.Stmts {
			if info.ResultExpr != nil && i == len(e.Body.Stmts)-1 {
				if es, ok := s.(*syntax.ExprStmt); ok && es.X == info.ResultExpr {
					child.returnStmt([]syntax.Expr{info.ResultExpr}, info.ResultExpr.Span())
					continue
				}
			}
			child.stmt(s)
		}
	})
	child.finish(&stmts, e.Body.Rbrace)
	ft := fl.l.funcType(info.Sig, names)
	lit := &ast.FuncLit{Type: ft, Body: &ast.BlockStmt{List: stmts, Lbrace: fl.l.pos(e.Body.Lbrace), Rbrace: fl.l.pos(e.Body.Rbrace)}}
	return lit
}

// ---------------------------------------------------------------------------
// Selectors

func (fl *fnLower) selector(e *syntax.SelectorExpr) ast.Expr {
	sel := fl.info.Selections[e]
	if sel == nil {
		return ast.NewIdent("nil")
	}
	if sel.Kind == check.SelPkgMember {
		pkgIdent := identOf(e.X)
		if ps, ok := fl.info.Uses[pkgIdent].(*check.PkgSym); ok && ps.Pkg != nil {
			name := fl.l.useImport(ps.Pkg.Path, ps.Pkg.Name)
			if ps.Import != nil && ps.Import.Alias != nil {
				name = fl.l.useImport(ps.Pkg.Path, ps.Import.Alias.Name)
			}
			goName := sel.GoName
			if goName == "" {
				goName = e.Sel.Name
			}
			return &ast.SelectorExpr{X: ast.NewIdent(name), Sel: ast.NewIdent(goName)}
		}
	}
	recv := fl.receiver(e)
	if sel.Safe {
		return fl.safeSelect(e, recv, sel)
	}
	return &ast.SelectorExpr{X: recv, Sel: ast.NewIdent(sel.GoName)}
}

// receiver lowers the operand of a selector, propagating errors first for
// x?.name on a fallible x.
func (fl *fnLower) receiver(e *syntax.SelectorExpr) ast.Expr {
	sel := fl.info.Selections[e]
	if sel != nil && sel.TryFirst {
		try := &syntax.TryExpr{X: e.X, OpSpan: e.OpSpan, S: e.X.Span().Join(e.OpSpan)}
		fl.info.Tries[try] = fl.info.Tries[e]
		return fl.tryValue(try)
	}
	// Unboxing is unnecessary for member access: Go dereferences pointers.
	if fl.info.Implicit[e.X] == check.ConvUnbox {
		x := fl.expr0(e.X)
		fl.l.mark(x, e.X.Span())
		return x
	}
	return fl.expr(e.X)
}

// safeSelect lowers x?.field: the field when x is non-null, else null.
func (fl *fnLower) safeSelect(e *syntax.SelectorExpr, recv ast.Expr, sel *check.Selection) ast.Expr {
	t := fl.info.TypeOf(e)
	x := fl.stash(e.X, recv)
	name := fl.temp(lowerFirst(e.Sel.Name))
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(t)}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, e.S)
	var val ast.Expr = &ast.SelectorExpr{X: cloneIdentExpr(x), Sel: ast.NewIdent(sel.GoName)}
	if types.IsNullable(t) && !types.IsNilable(types.Base(t)) && !types.MayBeNull(sel.Type) {
		val = &ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{val}}
	}
	ifs := &ast.IfStmt{Cond: nilCmp(x, token.NEQ), Body: &ast.BlockStmt{List: []ast.Stmt{
		&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}}}}}
	fl.emitSynth(ifs, e.S)
	return ast.NewIdent(name)
}

// blanks returns n blank identifiers.
func blanks(n int) []ast.Expr {
	out := make([]ast.Expr, n)
	for i := range out {
		out[i] = ast.NewIdent("_")
	}
	return out
}

// hasStringMethod reports whether values of t have a String() string method
// (the method fmt uses), excluding interfaces, whose dynamic values may not.
func hasStringMethod(t types.Type) bool {
	if types.IsInterface(t) {
		return false
	}
	gt := types.Lower(types.Base(t))
	obj, _, _ := gotypes.LookupFieldOrMethod(gt, false, nil, "String")
	fn, ok := obj.(*gotypes.Func)
	if !ok {
		return false
	}
	sig := fn.Type().(*gotypes.Signature)
	if sig.Params().Len() != 0 || sig.Results().Len() != 1 {
		return false
	}
	b, ok := sig.Results().At(0).Type().Underlying().(*gotypes.Basic)
	return ok && b.Kind() == gotypes.String
}

// firstOr returns the only type of ts, or nil.
func firstOr(ts []types.Type) types.Type {
	if len(ts) == 1 {
		return ts[0]
	}
	return nil
}

// boxes reports whether a catch or ? turns the call's value of type st into
// rt by boxing it: catch { null } makes an Int an Int?.
func boxes(st, rt types.Type) bool {
	return optionalValue(rt) && !types.IsNullable(st) && rt != nil && types.IdenticalIgnoringNull(st, rt)
}

// catchBoxed lowers a catch or typed catch chain whose value is an optional
// of the call's value type. The call's value is bound in the if statement and
// boxed into name, which the handlers assign directly:
//
//	var n *int
//	if v, err := strconv.Atoi(s); err == nil {
//		n = new(v)
//	}
//
// clauses are the typed clauses of a chain ending in final (or in ? when
// final is nil); for a plain catch they are nil and final is the catch.
func (fl *fnLower) catchBoxed(final *syntax.CatchExpr, clauses []*syntax.CatchExpr, tailSpan source.Span, name string, rt types.Type, span source.Span) {
	var base syntax.Expr
	var info *check.TryInfo
	if len(clauses) > 0 {
		base, info = clauses[0].X, fl.info.Tries[clauses[0]]
	} else {
		base, info = final.X, fl.info.Tries[final]
	}
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(rt)}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, span)
	operand := fl.expr(base)
	value := fl.temp("v")
	errName := ""
	if final != nil {
		if fi := fl.info.Tries[final]; fi != nil && fi.Binder != nil {
			errName = fl.catchBinder(fi.Binder, final.Body, true)
		}
	}
	if errName == "" {
		errName = fl.errVar()
	}
	lhs := []ast.Expr{ast.NewIdent(value)}
	for i := 1; i < len(info.Success); i++ {
		lhs = append(lhs, ast.NewIdent("_"))
	}
	init := &ast.AssignStmt{Lhs: append(lhs, ast.NewIdent(errName)), Tok: token.DEFINE, Rhs: []ast.Expr{operand}}
	fl.l.mark(init, base.Span())
	boxed := &ast.BlockStmt{List: []ast.Stmt{&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN,
		Rhs: []ast.Expr{&ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{ast.NewIdent(value)}}}}}}
	markTree(fl.l, boxed, span)
	ifs := &ast.IfStmt{If: fl.l.pos(span), Init: init}
	switch {
	case len(clauses) > 0:
		ifs.Cond = nilCmp(ast.NewIdent(errName), token.NEQ)
		ifs.Body = fl.chainDispatch(clauses, final, tailSpan, errName, ast.NewIdent(name), rt)
		ifs.Else = boxed
	case nullHandler(final):
		// catch { null }: the target already holds null.
		ifs.Cond = nilCmp(ast.NewIdent(errName), token.EQL)
		ifs.Body = boxed
	default:
		ifs.Cond = nilCmp(ast.NewIdent(errName), token.NEQ)
		ifs.Body = fl.catchHandler(final, info, ast.NewIdent(name), rt)
		ifs.Else = boxed
	}
	fl.l.markSynth(ifs.Cond, tailSpan)
	fl.emit(ifs, span)
}

// nullHandler reports whether a catch block is just null.
func nullHandler(e *syntax.CatchExpr) bool {
	if len(e.Body.Stmts) != 1 {
		return false
	}
	es, ok := e.Body.Stmts[0].(*syntax.ExprStmt)
	if !ok {
		return false
	}
	_, isNull := syntax.Unparen(es.X).(*syntax.NullLit)
	return isNull
}

// foldableConst reports whether a constant in a template may be written
// into the text: not a named type's (Color.Green, time.Second print through
// String), and not one naming a package, whose import would go unused.
func (fl *fnLower) foldableConst(e syntax.Expr, t types.Type) bool {
	if _, basic := types.Base(t).(*types.Basic); !basic {
		return false
	}
	foldable := true
	syntax.Walk(e, func(n syntax.Node) {
		if sel, ok := n.(*syntax.SelectorExpr); ok {
			if id := identOf(sel.X); id != nil {
				if _, isPkg := fl.info.Uses[id].(*check.PkgSym); isPkg {
					foldable = false
				}
			}
		}
		if fl.info.Entries[asExpr(n)] != nil {
			foldable = false
		}
	})
	return foldable
}

func asExpr(n syntax.Node) syntax.Expr {
	e, _ := n.(syntax.Expr)
	return e
}

// boxRead reads a map element or channel element whose type has no zero
// value (check.Zeroless) as a T? that is null when the key is missing or the
// channel is closed, where Go would hand out the zero value:
//
//	var v *Job
//	if x, ok := m[k]; ok {
//		v = &x
//	}
func (fl *fnLower) boxRead(read ast.Expr, elem types.Type, span source.Span) ast.Expr {
	name := fl.temp("v")
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(types.MakeNullable(elem))}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, span)
	fl.boxInto(name, read, span)
	return ast.NewIdent(name)
}

// boxInto assigns &x to name when read finds a value.
func (fl *fnLower) boxInto(name string, read ast.Expr, span source.Span) {
	x, ok := fl.temp("x"), fl.temp("ok")
	fl.emitSynth(&ast.IfStmt{
		Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(x), ast.NewIdent(ok)}, Tok: token.DEFINE, Rhs: []ast.Expr{read}},
		Cond: ast.NewIdent(ok),
		Body: &ast.BlockStmt{List: []ast.Stmt{boxAssign(name, x)}},
	}, span)
}

// zerolessRead reports whether e is a map index or receive whose element
// type has no zero value, which reads as a boxed T? (boxRead).
func (fl *fnLower) zerolessRead(e syntax.Expr) bool {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.IndexExpr:
		m, ok := check.CollectionOf(fl.info.TypeOf(x.X)).(*types.Map)
		return ok && fl.info.IndexInsts[x] == nil && check.Zeroless(m.Elem)
	case *syntax.CallExpr:
		if ci := fl.info.Calls[x]; ci != nil && ci.Kind == check.CallPrelude && ci.Prelude == check.PreludeReceive && !ci.Safe {
			s := syntax.Unparen(x.Fun).(*syntax.SelectorExpr)
			ch, ok := check.CollectionOf(fl.info.TypeOf(s.X)).(*types.Chan)
			return ok && check.Zeroless(ch.Elem)
		}
	}
	return false
}

// boxAssign is name = &x.
func boxAssign(name, x string) ast.Stmt {
	return &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN, Rhs: []ast.Expr{&ast.UnaryExpr{Op: token.AND, X: ast.NewIdent(x)}}}
}

// commaOkRead lowers the m[k] or ch.receive() of v, ok := ... to its Go
// read, and returns the element type when it has no zero value, so that the
// caller boxes the value; any other expression is lowered as it is.
func (fl *fnLower) commaOkRead(e syntax.Expr) (ast.Expr, types.Type) {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.IndexExpr:
		if m, ok := check.CollectionOf(fl.info.TypeOf(x.X)).(*types.Map); ok && fl.info.IndexInsts[x] == nil {
			xs := fl.exprList([]syntax.Expr{x.X, x.Index}, nil)
			read := &ast.IndexExpr{X: xs[0], Index: xs[1]}
			if check.Zeroless(m.Elem) {
				return read, m.Elem
			}
			return read, nil
		}
	case *syntax.CallExpr:
		if ci := fl.info.Calls[x]; ci != nil && ci.Kind == check.CallPrelude && ci.Prelude == check.PreludeReceive && !ci.Safe {
			s := syntax.Unparen(x.Fun).(*syntax.SelectorExpr)
			read := &ast.UnaryExpr{Op: token.ARROW, X: fl.expr(s.X)}
			if ch, ok := check.CollectionOf(fl.info.TypeOf(s.X)).(*types.Chan); ok && check.Zeroless(ch.Elem) {
				return read, ch.Elem
			}
			return read, nil
		}
	}
	return fl.expr(e), nil
}
