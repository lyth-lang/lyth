package lower

import (
	"go/ast"
	"go/constant"
	"go/token"
	"strings"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

func (fl *fnLower) call(e *syntax.CallExpr) ast.Expr {
	ci := fl.info.Calls[e]
	if ci == nil {
		return ast.NewIdent("nil")
	}
	switch ci.Kind {
	case check.CallConstruct:
		return fl.construct(e, ci)
	case check.CallConvert:
		if isLit(ci.Args[0]) && types.Identical(fl.info.TypeOf(ci.Args[0]), ci.Target) {
			// gin.H(["k": v]) is the literal gin.H{"k": v}.
			return fl.expr(ci.Args[0])
		}
		x := fl.exprAs(ci.Args[0], ci.Target)
		fun := fl.l.typeExpr(ci.Target)
		switch fun.(type) {
		case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.ArrayType, *ast.MapType:
		default:
			fun = &ast.ParenExpr{X: fun}
		}
		return &ast.CallExpr{Fun: fun, Args: []ast.Expr{x}}
	case check.CallBuiltin:
		return fl.builtin(e, ci)
	case check.CallPrelude:
		return fl.prelude(e, ci)
	}
	if s, ok := syntax.Unparen(e.Fun).(*syntax.SelectorExpr); ok && ci.Safe {
		return fl.safeCall(e, s, ci)
	}
	if ci.Ext != nil {
		return fl.extCall(e, ci)
	}
	// Function value and arguments, left to right.
	ops := append([]syntax.Expr{e.Fun}, ci.Args...)
	var fun ast.Expr
	var args []ast.Expr
	if _, pre := fl.pre[e.Fun]; pre {
		// A deferred call whose function value was read at the defer.
		xs := fl.exprList(ops, nil)
		fun, args = xs[0], xs[1:]
	} else if s, ok := syntax.Unparen(e.Fun).(*syntax.SelectorExpr); ok && fl.info.Selections[s] != nil && fl.info.Selections[s].Kind != check.SelPkgMember {
		// Method call: receiver first. A pointer method called on a variable,
		// field, or element takes its address, so a receiver read before an
		// argument that needs statements is its address: v := &b.c, then
		// v.add(x). A copy would lose what the method changes.
		recv := fl.receiver(s)
		rest := fl.exprListAfterRecv(recv, s.X, ci.Args, fl.needsAddr(s))
		recv, args = rest[0], rest[1:]
		fun = &ast.SelectorExpr{X: recv, Sel: ast.NewIdent(fl.info.Selections[s].GoName)}
	} else {
		xs := fl.exprList(ops, nil)
		fun, args = xs[0], xs[1:]
		if ci.ExplicitTypeArgs && len(ci.TypeArgs) > 0 {
			fun = fl.l.instantiate(fun, ci.TypeArgs)
		}
	}
	if ci.ArgParams != nil {
		args = fl.bindArgs(e, ci, args)
	} else {
		for i, a := range ci.Args {
			if i < len(ci.Sig.Params) {
				args[i] = fl.adaptConst(a, args[i], ci.Sig.Params[i])
			}
		}
	}
	call := &ast.CallExpr{Fun: fun, Args: args}
	if hasSpread(e) {
		call.Ellipsis = 1
	}
	return call
}

// extCall lowers a call of an extension function, x.Filter(f), to the Go
// call of the function with the receiver first: coll.Filter(x, f).
func (fl *fnLower) extCall(e *syntax.CallExpr, ci *check.CallInfo) ast.Expr {
	s := syntax.Unparen(e.Fun).(*syntax.SelectorExpr)
	var recv ast.Expr
	var args []ast.Expr
	if ci.Ext.Addr {
		// A pointer receiver is the operand's address, also when it is read
		// before an argument that needs statements.
		first := fl.expr(s.X)
		if p, ok := first.(*ast.ParenExpr); ok {
			first = p.X
		}
		xs := fl.exprListAfterRecv(&ast.UnaryExpr{Op: token.AND, X: first}, s.X, ci.Args, false)
		recv, args = xs[0], xs[1:]
	} else {
		xs := fl.exprList(append([]syntax.Expr{s.X}, ci.Args...), nil)
		recv, args = xs[0], xs[1:]
		if p, ok := recv.(*ast.ParenExpr); ok {
			recv = p.X // an argument needs no parentheses: Halved(3 * time.Second)
		}
		if ci.Ext.Deref {
			recv = &ast.StarExpr{X: recv}
		}
	}
	fun := fl.l.extFunc(ci.Ext)
	if ci.ExplicitTypeArgs && len(ci.TypeArgs) > 0 {
		fun = fl.l.instantiate(fun, ci.TypeArgs)
	}
	if ci.ArgParams != nil {
		args = fl.bindArgs(e, ci, args)
	} else {
		for i, a := range ci.Args {
			if i < len(ci.Sig.Params) {
				args[i] = fl.adaptConst(a, args[i], ci.Sig.Params[i])
			}
		}
	}
	call := &ast.CallExpr{Fun: fun, Args: append([]ast.Expr{recv}, args...)}
	if hasSpread(e) {
		call.Ellipsis = 1
	}
	return call
}

// bindArgs puts the arguments of a call with named arguments or default
// values into parameter order. Arguments were lowered in the order written;
// impure ones are spilled into temporaries first when the reordering (or a
// default evaluated before them) would change when they run. Defaults are
// evaluated last, at the call site.
func (fl *fnLower) bindArgs(e *syntax.CallExpr, ci *check.CallInfo, args []ast.Expr) []ast.Expr {
	np := len(ci.Sig.Params)
	slots := make([][]ast.Expr, np)
	type entry struct {
		order  int
		impure bool
	}
	first := make([]entry, np) // the first value of each parameter
	for i, a := range ci.Args {
		p := ci.ArgParams[i]
		if p < 0 || p >= np {
			continue
		}
		pt := ci.Sig.Params[p]
		if ci.Sig.Variadic && p == np-1 && !hasSpread(e) {
			if sl, ok := pt.(*types.Slice); ok {
				pt = sl.Elem
			}
		}
		args[i] = fl.adaptConst(a, args[i], pt)
		if len(slots[p]) == 0 {
			first[p] = entry{order: i, impure: !fl.pure(a, args[i]) && !fl.callFree(a)}
		}
		slots[p] = append(slots[p], args[i])
	}
	for _, d := range ci.Defaults {
		first[d.Param] = entry{order: len(ci.Args) + d.Param, impure: true}
	}
	// Would Go's left-to-right evaluation in parameter order run an impure
	// value before one written (or defaulted) earlier?
	spill, last := false, -1
	for p := 0; p < np; p++ {
		if (len(slots[p]) > 0 || first[p].order >= len(ci.Args)) && first[p].impure {
			if first[p].order < last {
				spill = true
			}
			last = max(last, first[p].order)
		}
	}
	if spill {
		for i, a := range ci.Args {
			p := ci.ArgParams[i]
			if p < 0 || p >= np || fl.pure(a, args[i]) || fl.callFree(a) {
				continue
			}
			tmp := fl.temp(paramBase(ci.Sig, p))
			fl.emitSynth(&ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{args[i]}}, a.Span())
			for j, x := range slots[p] {
				if x == args[i] {
					slots[p][j] = ast.NewIdent(tmp)
				}
			}
		}
	}
	for _, d := range ci.Defaults {
		slots[d.Param] = []ast.Expr{fl.defaultArg(d, ci.Sig.Params[d.Param])}
	}
	var out []ast.Expr
	for _, s := range slots {
		out = append(out, s...)
	}
	return out
}

func paramBase(sig *types.Signature, p int) string {
	if p < len(sig.ParamNames) && sig.ParamNames[p] != "_" {
		return sig.ParamNames[p]
	}
	return "v"
}

// defaultArg lowers a parameter's default value at a call site. The
// expression belongs to the declaring package, so it is lowered with that
// package's information; references to its declarations get qualified as
// usual when the call is in another package.
func (fl *fnLower) defaultArg(d check.DefaultArg, pt types.Type) ast.Expr {
	sub := *fl
	sub.info = d.Info
	return sub.exprAs(d.Expr, pt)
}

// needsAddr reports whether a method call through the selector s takes the
// address of its receiver operand: a pointer method on a value.
func (fl *fnLower) needsAddr(s *syntax.SelectorExpr) bool {
	sel := fl.info.Selections[s]
	if sel == nil || sel.Kind != check.SelMethod || !sel.PtrRecv || fl.info.Implicit[s.X] == check.ConvUnbox {
		return false
	}
	switch types.Base(fl.info.TypeOf(s.X)).(type) {
	case *types.Pointer, nil:
		return false
	}
	return !types.IsInterface(fl.info.TypeOf(s.X))
}

// exprListAfter lowers operands after an already-lowered first operand,
// spilling the first operand if later ones need statements.
func (fl *fnLower) exprListAfter(first ast.Expr, firstSyn syntax.Expr, rest []syntax.Expr) []ast.Expr {
	return fl.exprListAfterRecv(first, firstSyn, rest, false)
}

// exprListAfterRecv is exprListAfter for a method's receiver; addr spills
// the receiver's address, for a pointer method that takes it.
func (fl *fnLower) exprListAfterRecv(first ast.Expr, firstSyn syntax.Expr, rest []syntax.Expr, addr bool) []ast.Expr {
	out := []ast.Expr{first}
	for _, e := range rest {
		mark := len(*fl.stmts)
		x := fl.expr(e)
		if len(*fl.stmts) > mark {
			var spills []ast.Stmt
			for j := range out {
				syn := firstSyn
				if j > 0 {
					syn = rest[j-1]
				}
				if fl.pure(syn, out[j]) || fl.callFree(syn) {
					continue
				}
				tmp := fl.temp("v")
				val := out[j]
				if j == 0 && addr {
					val = &ast.UnaryExpr{Op: token.AND, X: val}
				}
				spill := &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(tmp)}, Tok: token.DEFINE, Rhs: []ast.Expr{val}}
				markTree(fl.l, spill, syn.Span())
				spills = append(spills, spill)
				out[j] = ast.NewIdent(tmp)
			}
			if len(spills) > 0 {
				tail := append([]ast.Stmt(nil), (*fl.stmts)[mark:]...)
				*fl.stmts = append(append((*fl.stmts)[:mark], spills...), tail...)
			}
		}
		out = append(out, x)
	}
	return out
}

func hasSpread(e *syntax.CallExpr) bool {
	for _, a := range e.Args {
		if a.Spread {
			return true
		}
	}
	return false
}

// safeCall lowers x?.m(args): the call happens only when x is non-null.
// safeCallParts lowers x?.m(args) up to the call: the receiver, evaluated
// once, the statements its arguments need, and the call, all of which run
// only when the receiver is not null.
func (fl *fnLower) safeCallParts(e *syntax.CallExpr, s *syntax.SelectorExpr, ci *check.CallInfo) (ast.Expr, []ast.Stmt, *ast.CallExpr) {
	recv := fl.stash(s.X, fl.receiver(s))
	sub, stmts := fl.sub()
	args := sub.exprList(ci.Args, nil)
	if ci.ArgParams != nil {
		args = sub.bindArgs(e, ci, args)
	} else {
		for i, a := range ci.Args {
			if i < len(ci.Sig.Params) {
				args[i] = sub.adaptConst(a, args[i], ci.Sig.Params[i])
			}
		}
	}
	call := &ast.CallExpr{Fun: &ast.SelectorExpr{X: cloneIdentExpr(recv), Sel: ast.NewIdent(fl.info.Selections[s].GoName)}, Args: args}
	if hasSpread(e) {
		call.Ellipsis = 1
	}
	return recv, *stmts, call
}

// safeCallStmt lowers x?.m(args) whose result is unused, or that a defer
// or go statement runs (wrap): if x != nil { defer x.m(args) }. The
// receiver and arguments are evaluated at the statement, as Go does for a
// deferred call.
func (fl *fnLower) safeCallStmt(e *syntax.CallExpr, s *syntax.SelectorExpr, ci *check.CallInfo, wrap func(*ast.CallExpr) ast.Stmt) {
	recv, body, call := fl.safeCallParts(e, s, ci)
	body = append(body, wrap(call))
	fl.emitSynth(&ast.IfStmt{Cond: nilCmp(recv, token.NEQ), Body: &ast.BlockStmt{List: body}}, e.S)
}

// safeCallOf returns the call and selector of a safe method call x?.m(...).
func (fl *fnLower) safeCallOf(e syntax.Expr) (*syntax.CallExpr, *syntax.SelectorExpr, *check.CallInfo) {
	c, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return nil, nil, nil
	}
	s, ok := syntax.Unparen(c.Fun).(*syntax.SelectorExpr)
	ci := fl.info.Calls[c]
	if !ok || ci == nil || !ci.Safe || ci.Ext != nil || ci.Kind == check.CallPrelude {
		return nil, nil, nil
	}
	return c, s, ci
}

func (fl *fnLower) safeCall(e *syntax.CallExpr, s *syntax.SelectorExpr, ci *check.CallInfo) ast.Expr {
	t := fl.info.TypeOf(e)
	if t == nil || t == types.Unit || len(ci.Sig.Results) == 0 {
		fl.safeCallStmt(e, s, ci, func(c *ast.CallExpr) ast.Stmt { return &ast.ExprStmt{X: c} })
		return ast.NewIdent("nil")
	}
	recv, stmts, call := fl.safeCallParts(e, s, ci)
	name := fl.temp(lowerFirst(s.Sel.Name))
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(t)}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, e.S)
	var val ast.Expr = call
	if types.IsNullable(t) && !types.IsNilable(types.Base(t)) && !types.MayBeNull(ci.Sig.Results[0]) {
		val = &ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{call}}
	}
	body := append(stmts, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}})
	fl.emitSynth(&ast.IfStmt{Cond: nilCmp(recv, token.NEQ), Body: &ast.BlockStmt{List: body}}, e.S)
	return ast.NewIdent(name)
}

// construct lowers struct construction to a keyed composite literal.
func (fl *fnLower) construct(e *syntax.CallExpr, ci *check.CallInfo) ast.Expr {
	vals := fl.exprList(ci.Args, nil)
	cl := &ast.CompositeLit{Type: fl.l.typeExpr(ci.Target), Lbrace: fl.l.pos(e.Lparen), Rbrace: fl.l.pos(e.Rparen)}
	fields := fieldTypes(ci.Target)
	for i, f := range ci.Fields {
		v := vals[i]
		if ft, ok := fields[f]; ok {
			v = fl.adaptConst(ci.Args[i], v, ft)
		}
		// The key takes the argument's position, so a construction written
		// over several lines stays a multi-line literal.
		kv := &ast.KeyValueExpr{Key: &ast.Ident{Name: f, NamePos: fl.l.pos(ci.Args[i].Span())}, Value: v}
		fl.l.mark(kv, ci.Args[i].Span())
		cl.Elts = append(cl.Elts, kv)
	}
	return cl
}

func fieldTypes(t types.Type) map[string]types.Type {
	out := map[string]types.Type{}
	if n, ok := types.Base(t).(*types.Named); ok && n.Decl != nil {
		for _, f := range n.Decl.Fields {
			out[f.GoName] = f.Type
		}
	}
	return out
}

func (fl *fnLower) builtin(e *syntax.CallExpr, ci *check.CallInfo) ast.Expr {
	name := map[check.BuiltinKind]string{
		check.BuiltinLen: "len", check.BuiltinCap: "cap", check.BuiltinAppend: "append", check.BuiltinCopy: "copy",
		check.BuiltinDelete: "delete", check.BuiltinMake: "make", check.BuiltinNew: "new", check.BuiltinPanic: "panic",
		check.BuiltinRecover: "recover", check.BuiltinMin: "min", check.BuiltinMax: "max", check.BuiltinClear: "clear",
		check.BuiltinClose: "close", check.BuiltinComplex: "complex", check.BuiltinReal: "real", check.BuiltinImag: "imag",
	}[ci.Builtin]
	var fun ast.Expr = ast.NewIdent(name)
	switch ci.Builtin {
	case check.BuiltinPrint:
		fun = sel(fl.l.useImport("fmt", "fmt"), "Print")
	case check.BuiltinPrintln:
		fun = sel(fl.l.useImport("fmt", "fmt"), "Println")
	}
	args := fl.exprList(ci.Args, nil)
	if ci.Builtin == check.BuiltinPrint || ci.Builtin == check.BuiltinPrintln {
		// Nullable values print as Lyth values ("null" or the value), not as
		// Go pointers.
		for i, a := range ci.Args {
			if t := fl.info.TypeOf(a); t != nil && types.IsNullable(t) {
				args[i] = fl.stringify(a, args[i], t)
			}
		}
	}
	call := &ast.CallExpr{Fun: fun, Args: args}
	if hasSpread(e) {
		call.Ellipsis = 1
	}
	return call
}

// prelude lowers prelude methods on built-in types to standard library calls.
func (fl *fnLower) prelude(e *syntax.CallExpr, ci *check.CallInfo) ast.Expr {
	s := syntax.Unparen(e.Fun).(*syntax.SelectorExpr)
	if ci.Safe {
		switch ci.Prelude {
		case check.PreludeSend, check.PreludeClose, check.PreludeReceive:
			return fl.safeChanOp(e, s, ci)
		}
	}
	recv := fl.expr(s.X)
	strconvPkg := func() string { return fl.l.useImport("strconv", "strconv") }
	switch ci.Prelude {
	case check.PreludeToInt:
		return &ast.CallExpr{Fun: sel(strconvPkg(), "Atoi"), Args: []ast.Expr{recv}}
	case check.PreludeToInt64:
		return &ast.CallExpr{Fun: sel(strconvPkg(), "ParseInt"), Args: []ast.Expr{recv, intLit(10), intLit(64)}}
	case check.PreludeToFloat64:
		return &ast.CallExpr{Fun: sel(strconvPkg(), "ParseFloat"), Args: []ast.Expr{recv, intLit(64)}}
	case check.PreludeToBool:
		return &ast.CallExpr{Fun: sel(strconvPkg(), "ParseBool"), Args: []ast.Expr{recv}}
	case check.PreludeReceive:
		read := &ast.UnaryExpr{Op: token.ARROW, X: recv}
		if ch, ok := check.CollectionOf(fl.info.TypeOf(s.X)).(*types.Chan); ok && check.Zeroless(ch.Elem) {
			return fl.boxRead(read, ch.Elem, e.S)
		}
		return read
	case check.PreludeClose:
		return &ast.CallExpr{Fun: ast.NewIdent("close"), Args: []ast.Expr{recv}}
	case check.PreludeSend:
		// A send is a Go statement: emit it here. It has no value, so it
		// only appears where its (absent) result is discarded.
		fl.emit(fl.sendStmt(e, recv, ci), e.S)
		return ast.NewIdent("_")
	case check.PreludeWrap:
		return fl.wrapErr(s.X, ci.Args[0], recv)
	case check.PreludeWrapFailure:
		// The call itself; ? and catch wrap its error when it fails
		// (wrapOnFailure).
		return recv
	}
	return ast.NewIdent("nil")
}

// wrapErr lowers err.wrap(msg) to fmt.Errorf("msg: %w", err). recvSyn is the
// receiver's Lyth expression, evaluated before msg; nil when recv is a
// variable that msg cannot change.
func (fl *fnLower) wrapErr(recvSyn, msg syntax.Expr, recv ast.Expr) ast.Expr {
	fmtPkg := fl.l.useImport("fmt", "fmt")
	if tv := fl.info.Types[msg]; tv.Value != nil && tv.Value.Kind() == constant.String {
		format := escapePercent(constant.StringVal(tv.Value)) + ": %w"
		return &ast.CallExpr{Fun: sel(fmtPkg, "Errorf"), Args: []ast.Expr{strLit(format), recv}}
	}
	if tmpl, ok := syntax.Unparen(msg).(*syntax.TemplateLit); ok {
		return fl.wrapTemplate(fmtPkg, recvSyn, recv, tmpl)
	}
	if recvSyn == nil {
		return &ast.CallExpr{Fun: sel(fmtPkg, "Errorf"), Args: []ast.Expr{strLit("%s: %w"), fl.expr(msg), recv}}
	}
	msgs := fl.exprListAfter(recv, recvSyn, []syntax.Expr{msg})
	return &ast.CallExpr{Fun: sel(fmtPkg, "Errorf"), Args: []ast.Expr{strLit("%s: %w"), msgs[1], msgs[0]}}
}

// wrapOnFailure lowers the context of f().wrap(msg) around err, the error of
// the call in the branch where it failed, so the message is evaluated only
// then: the statements the message needs, and the wrapped error. For any
// other x it returns err itself.
func (fl *fnLower) wrapOnFailure(x syntax.Expr, err ast.Expr) ([]ast.Stmt, ast.Expr) {
	msg := fl.wrapMessage(x)
	if msg == nil {
		return nil, err
	}
	sub, stmts := fl.sub()
	wrapped := sub.wrapErr(nil, msg, err)
	return *stmts, wrapped
}

// wrapMessage returns msg when x is f().wrap(msg) on a call that can fail,
// and nil otherwise.
func (fl *fnLower) wrapMessage(x syntax.Expr) syntax.Expr {
	call, ok := syntax.Unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil
	}
	if ci := fl.info.Calls[call]; ci != nil && ci.Prelude == check.PreludeWrapFailure && len(ci.Args) == 1 {
		return ci.Args[0]
	}
	return nil
}

// safeChanOp lowers ch?.send(v), ch?.close(), and ch?.receive() on a
// nullable channel: the operation runs only when the channel is not null,
// and a receive from a null channel is null.
func (fl *fnLower) safeChanOp(e *syntax.CallExpr, s *syntax.SelectorExpr, ci *check.CallInfo) ast.Expr {
	ch := fl.stash(s.X, fl.expr(s.X))
	sub, stmts := fl.sub()
	switch ci.Prelude {
	case check.PreludeSend:
		sub.emit(sub.sendStmt(e, cloneIdentExpr(ch), ci), e.S)
		fl.emitSynth(&ast.IfStmt{Cond: nilCmp(ch, token.NEQ), Body: &ast.BlockStmt{List: *stmts}}, e.S)
		return ast.NewIdent("_")
	case check.PreludeClose:
		body := append(*stmts, &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("close"), Args: []ast.Expr{cloneIdentExpr(ch)}}})
		fl.emitSynth(&ast.IfStmt{Cond: nilCmp(ch, token.NEQ), Body: &ast.BlockStmt{List: body}}, e.S)
		return ast.NewIdent("_")
	}
	t := fl.info.TypeOf(e)
	name := fl.temp("v")
	spec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(name)}, Type: fl.l.typeExpr(t)}
	fl.emitSynth(&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}}, e.S)
	var val ast.Expr = &ast.UnaryExpr{Op: token.ARROW, X: cloneIdentExpr(ch)}
	var body []ast.Stmt
	switch {
	case check.Zeroless(types.Base(t)):
		// A closed channel of a type without zero value gives null.
		x, ok := fl.temp("x"), fl.temp("ok")
		body = append(*stmts, &ast.IfStmt{
			Init: &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(x), ast.NewIdent(ok)}, Tok: token.DEFINE, Rhs: []ast.Expr{val}},
			Cond: ast.NewIdent(ok),
			Body: &ast.BlockStmt{List: []ast.Stmt{boxAssign(name, x)}},
		})
	case types.IsNullable(t) && !types.IsNilable(types.Base(t)):
		val = &ast.CallExpr{Fun: ast.NewIdent("new"), Args: []ast.Expr{val}} // int? holds the received int
		fallthrough
	default:
		body = append(*stmts, &ast.AssignStmt{Lhs: []ast.Expr{ast.NewIdent(name)}, Tok: token.ASSIGN, Rhs: []ast.Expr{val}})
	}
	fl.emitSynth(&ast.IfStmt{Cond: nilCmp(ch, token.NEQ), Body: &ast.BlockStmt{List: body}}, e.S)
	return ast.NewIdent(name)
}

// sendStmt builds Go's ch <- v for ch.send(v), given the lowered channel.
func (fl *fnLower) sendStmt(e *syntax.CallExpr, ch ast.Expr, ci *check.CallInfo) *ast.SendStmt {
	s := syntax.Unparen(e.Fun).(*syntax.SelectorExpr)
	xs := fl.exprListAfter(ch, s.X, ci.Args)
	return &ast.SendStmt{Chan: xs[0], Arrow: fl.l.pos(e.S), Value: fl.convertTo(ci.Args[0], xs[1])}
}

func escapePercent(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// wrapTemplate lowers err.wrap("text ${x} ...") to a single
// fmt.Errorf("text %d ...: %w", x, err), the form a Go programmer writes,
// instead of building the message string first. Values whose Lyth rendering
// differs from fmt's (nullable values print "null") are rendered first and
// passed as %s.
func (fl *fnLower) wrapTemplate(fmtPkg string, recvSyn syntax.Expr, recv ast.Expr, tmpl *syntax.TemplateLit) ast.Expr {
	// The receiver is evaluated first (method call order), so it is stashed
	// when the template's operands could observe it changing.
	if recvSyn != nil && !fl.pure(recvSyn, recv) {
		recv = fl.stash(recvSyn, recv)
	}
	var exprs []syntax.Expr
	for _, p := range tmpl.Parts {
		if p.X != nil {
			exprs = append(exprs, p.X)
		}
	}
	lowered := fl.exprList(exprs, nil)
	var format strings.Builder
	var args []ast.Expr
	k := 0
	for _, p := range tmpl.Parts {
		if p.X == nil {
			format.WriteString(escapePercent(p.Text))
			continue
		}
		x := lowered[k]
		k++
		tv := fl.info.Types[p.X]
		if tv.Value != nil && (tv.Value.Kind() == constant.String || tv.Value.Kind() == constant.Int && !types.Identical(types.Base(tv.Type), types.Rune) || tv.Value.Kind() == constant.Bool) {
			if tv.Value.Kind() == constant.String {
				format.WriteString(escapePercent(constant.StringVal(tv.Value)))
			} else {
				format.WriteString(tv.Value.ExactString())
			}
			continue
		}
		verb, arg := fl.formatVerb(p.X, x, tv.Type)
		format.WriteString(verb)
		args = append(args, arg)
	}
	format.WriteString(": %w")
	args = append([]ast.Expr{strLit(format.String())}, append(args, recv)...)
	return &ast.CallExpr{Fun: sel(fmtPkg, "Errorf"), Args: args}
}

// formatVerb picks the fmt verb that renders a value exactly as a Lyth string
// template does.
func (fl *fnLower) formatVerb(e syntax.Expr, x ast.Expr, t types.Type) (string, ast.Expr) {
	if types.IsNullable(t) {
		return "%s", fl.stringify(e, x, t)
	}
	if b := types.BasicOf(t); b != nil && isBasicType(types.Base(t)) {
		switch {
		case types.IsString(b):
			return "%s", x
		case b == types.Rune:
			return "%c", x
		case types.IsInteger(b):
			return "%d", x
		case types.IsFloat(b):
			return "%v", x
		case types.IsBoolean(b):
			return "%t", x
		}
	}
	// Strings, errors, Stringers, and everything else: fmt.Sprint semantics,
	// which is what templates use for these values.
	return "%v", x
}
