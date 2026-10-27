package check

import (
	gotypes "go/types"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// signatureOf returns the function signature underlying t, or nil.
func signatureOf(t types.Type) *types.Signature {
	switch b := types.Base(t).(type) {
	case *types.Signature:
		return b
	case *types.Named:
		if u := definedUnder(b); u != nil {
			sig, _ := types.Base(u).(*types.Signature)
			return sig
		}
		if b.Go != nil {
			if sig, ok := b.Go.Underlying().(*gotypes.Signature); ok {
				return types.LiftSignature(sig)
			}
		}
	}
	return nil
}

// lambda checks a lambda against an optional expected function type.
func (c *Checker) lambda(e *syntax.LambdaExpr, want types.Type) *operand {
	var exp *types.Signature
	unknown := want == types.Invalid // the argument of a call that failed to check
	if unknown {
		want = nil
	}
	if want != nil {
		exp = signatureOf(want)
		if exp == nil && !types.IsInterface(want) {
			c.errorf(diag.ETypeMismatch, e.S, "a lambda cannot be used where %s is expected", want)
		}
	}
	scope := newScope(c.scope, FuncScope)
	fn := &funcCtx{isLambda: true, name: "lambda", declSpan: e.S, noExpected: want == nil && !unknown}
	if c.fn != nil {
		fn.recv, fn.recvDecl = c.fn.recv, c.fn.recvDecl
	}
	scope.fn = fn
	info := &LambdaInfo{}
	sig := &types.Signature{}

	// A result that is still an unbound type parameter of a generic call
	// (R in map(xs) { ... }) comes from the body; the call unifies it.
	inferResult := false
	badResult := false // a return of several values, reported
	if exp != nil {
		for _, r := range exp.Results {
			inferResult = inferResult || c.foreignTypeParams(r)
		}
	}
	if exp != nil {
		sig.Variadic = exp.Variadic
		sig.Results = exp.Results
		sig.Throws = exp.Throws
		sig.Params = exp.Params
		switch {
		case len(e.Params) == 0 && !e.HasArrow && len(exp.Params) == 1:
			it := &VarSym{name: "it", Type: exp.Params[0], Kind: ParamVar, Span: e.S.At(), Implicit: true, Used: true}
			scope.Insert(it)
			info.Params = []*VarSym{it}
		case len(e.Params) != len(exp.Params):
			d := c.errorf(diag.EArgCount, e.S.At().Join(e.Body.Lbrace), "lambda declares %s, but %s expects %s",
				plural(len(e.Params), "parameter"), want, plural(len(exp.Params), "parameter"))
			if len(e.Params) == 0 && len(exp.Params) > 1 {
				d.WithHint("declare them: { %s -> ... }", placeholderNames(len(exp.Params)))
			}
		default:
			for i, p := range e.Params {
				t := exp.Params[i]
				if p.Type != nil {
					at := c.resolveType(p.Type)
					ok, conv, _ := c.assignable(t, at)
					switch {
					case !types.IdenticalIgnoringNull(at, t):
						c.errorf(diag.ETypeMismatch, p.Type.Span(), "parameter %s has type %s, but %s is expected here", p.Name.Name, at, t)
					case !ok || conv != ConvNone:
						// The caller passes a t, which may be null.
						c.errorf(diag.ETypeMismatch, p.Type.Span(), "parameter %s cannot have type %s: the lambda is called with %s values, which may be null", p.Name.Name, at, t).
							WithEdit(p.Type.Span(), t.String(), "declare it %s, or leave the type out", t)
						t = at // one error, not another at each use
					default:
						t = at
					}
				}
				v := &VarSym{name: p.Name.Name, Type: t, Kind: ParamVar, Span: p.Name.S, Used: true, owner: fn}
				if p.Name.Name != "_" {
					c.declare(scope, v, p.Name.S)
				}
				c.info.Defs[p.Name] = v
				info.Params = append(info.Params, v)
			}
		}
		fn.fallible, fn.rawTuple = fallibleResults(exp)
		if n := len(exp.Results); !exp.Throws && n > 0 && types.IsErrorType(exp.Results[n-1]) && types.IsPlatform(exp.Results[n-1]) {
			// A Go callback returning error (func() error, func(T) (U, error)):
			// the lambda is fallible and its success values precede the error.
			// A function type written in Lyth says throws when it can fail; an
			// Error result there is a value, as in a fun with that type.
			fn.fallible, fn.rawTuple = true, false
			sig = &types.Signature{Params: exp.Params, Variadic: exp.Variadic, Results: exp.Results[:n-1], Throws: true}
		}
		if inferResult {
			// A fallible lambda's inferred result is its success value
			// (T in () -> T throws); the error stays part of the signature.
			sig.Results = nil
			fn.infer = true
		}
	} else {
		if unknown && len(e.Params) == 0 && !e.HasArrow {
			it := &VarSym{name: "it", Type: types.Invalid, Kind: ParamVar, Span: e.S.At(), Implicit: true, Used: true}
			scope.Insert(it)
			info.Params = []*VarSym{it}
		}
		for _, p := range e.Params {
			var t types.Type = types.Invalid
			if p.Type == nil && !unknown {
				c.errorf(diag.ECannotInfer, p.S, "cannot infer the type of lambda parameter %s", p.Name.Name).
					WithHint("annotate it: { %s: Type -> ... }", p.Name.Name)
			} else {
				t = c.resolveType(p.Type)
			}
			v := &VarSym{name: p.Name.Name, Type: t, Kind: ParamVar, Span: p.Name.S, Used: true, owner: fn}
			if p.Name.Name != "_" {
				c.declare(scope, v, p.Name.S)
			}
			c.info.Defs[p.Name] = v
			info.Params = append(info.Params, v)
			sig.Params = append(sig.Params, t)
		}
		fn.infer = true
	}
	fn.sig = sig
	info.Fallible = fn.fallible
	if c.fn != nil {
		// The enclosing body's analysis covers the lambda.
		fn.reassigned, fn.flowOff = c.fn.reassigned, c.fn.flowOff
	} else {
		fn.flowOff = flowOff(e.Body)
	}

	saveScope, saveFn := c.scope, c.fn
	c.scope, c.fn = scope, fn
	stmts := e.Body.Stmts
	n := len(stmts)
	var lastExpr syntax.Expr
	if n > 0 {
		if es, ok := stmts[n-1].(*syntax.ExprStmt); ok {
			if _, isJump := es.X.(*syntax.JumpExpr); !isJump {
				lastExpr = es.X
			}
		}
	}
	if !fn.infer {
		needValue := len(sig.Results) == 1
		if needValue && lastExpr != nil {
			terminated := c.stmtList(stmts[:n-1])
			x := c.expr(lastExpr, sig.Results[0])
			if !terminated && x.mode != ModeInvalid && c.passThrough(x, sig.Results) {
				// { strconv.Atoi(s) } for a func() (int, error): the call's
				// results are the lambda's, as return strconv.Atoi(s) would be.
				info.ResultExpr = lastExpr
			} else if !terminated && x.mode != ModeInvalid {
				x = c.singleValue(x)
				if x.mode != ModeInvalid && !isNothing(x.typ) {
					c.assign(x, sig.Results[0], "lambda result")
					info.ResultExpr = lastExpr
				}
			}
		} else if _, isCall := syntax.Unparen(lastExpr).(*syntax.CallExpr); isCall && len(sig.Results) > 1 {
			// { pair(it) } for a (int) -> (int, int): the call's results are
			// the lambda's, as return pair(it) would be.
			terminated := c.stmtList(stmts[:n-1])
			x := c.expr(lastExpr, nil)
			switch {
			case terminated || x.mode == ModeInvalid:
			case c.passThrough(x, sig.Results) || c.tupleFits(x, sig.Results):
				info.ResultExpr = lastExpr
			default:
				c.errorf(diag.EMissingReturn, e.Body.Rbrace, "lambda must return %s, but %s gives %s", (&types.Tuple{Elems: sig.Results}).String(), lastExpr.Span().Short(), describeType(x))
			}
		} else {
			terminated := c.stmtList(stmts)
			if needValue && !terminated {
				c.errorf(diag.EMissingReturn, e.Body.Rbrace, "lambda must end with a value of type %s", sig.Results[0])
			} else if len(sig.Results) > 1 && !terminated {
				c.errorf(diag.EMissingReturn, e.Body.Rbrace, "lambda must return %s", (&types.Tuple{Elems: sig.Results}).String())
			}
		}
	} else {
		var resultT types.Type
		if lastExpr != nil && !statementLike(lastExpr) {
			c.stmtList(stmts[:n-1])
			x := c.expr(lastExpr, nil)
			switch {
			case x.mode == ModeValue && isStatementCall(lastExpr, x):
				// { fmt.Println(x) }: a call whose results are not one value
				// ends a unit lambda, as a statement would.
				if r, ok := x.typ.(*types.Result); ok {
					if call := callOf(lastExpr); call != nil && !c.errorIgnorable(call) {
						c.unhandledError(call, r)
					}
				}
				resultT = types.Unit
			case x.mode == ModeValue:
				x = c.singleValue(x)
				if x.mode != ModeInvalid {
					resultT = c.defaultValueType(x, "lambda result")
					info.ResultExpr = lastExpr
				}
			case x.mode == ModeNoValue:
				resultT = types.Unit
			}
		} else {
			c.stmtList(stmts)
		}
		for _, rt := range fn.inferred {
			if rt == types.Invalid {
				badResult = true // return a, b, reported
				continue
			}
			if resultT == nil || isNothing(resultT) {
				resultT = rt
			} else if !types.Identical(resultT, rt) && rt != types.Unit && !isNothing(rt) {
				c.errorf(diag.ECannotInfer, e.S, "lambda returns both %s and %s", resultT, rt).
					WithHint("annotate the expected function type")
			}
		}
		if resultT != nil && resultT != types.Unit && !isNothing(resultT) && resultT != types.Invalid {
			sig.Results = []types.Type{resultT}
		}
	}
	info.Unused = c.finishLocals(fn)
	c.scope, c.fn = saveScope, saveFn
	if fn.fallible && !fn.rawTuple && len(sig.Results) == 0 {
		c.bareCatchReturns(e.Body)
	}
	info.Sig = sig
	c.info.Lambdas[e] = info
	if badResult {
		return invalid(e)
	}
	if exp != nil && !inferResult {
		// The lambda has exactly the expected function type (including a Go
		// callback type returning error).
		return &operand{mode: ModeValue, typ: types.Base(want)}
	}
	return &operand{mode: ModeValue, typ: sig}
}

// bareCatchReturns warns about a bare return in a catch handler of a fallible
// lambda without results: it ends the lambda successfully and drops the
// error, so a helper running the lambda (a transaction) goes on as if it
// had worked, where Go makes the author write return nil or return err
// (ADR 0051). A handler that names the error _ discards it on purpose.
// Nested lambdas and go and defer blocks have returns of their own.
func (c *Checker) bareCatchReturns(body *syntax.Block) {
	var visit func(n syntax.Node, handler bool)
	visit = func(n syntax.Node, handler bool) {
		switch x := n.(type) {
		case *syntax.LambdaExpr, *syntax.GoStmt, *syntax.DeferStmt:
			return
		case *syntax.CatchExpr:
			visit(x.X, handler)
			visit(x.Body, x.Binder == nil || x.Binder.Name != "_")
			return
		case *syntax.JumpExpr:
			if handler && x.Kind == syntax.JumpReturn && len(x.Values) == 0 {
				c.warnf(diag.WCatchReturn, x.S, "return in this catch handler ends the lambda successfully, and the error is dropped").
					WithHint("to fail the lambda with the error, throw it: catch (err) { ...; throw err }").
					WithHint("to succeed on purpose, discard the error by name: catch (_) { ... }")
			}
		}
		syntax.Children(n, func(ch syntax.Node) { visit(ch, handler) })
	}
	visit(body, false)
}

// statementLike reports whether e, the last statement of a lambda whose
// result type is inferred, can only be a statement: an if without else, or
// a catch whose handler yields no value.
func statementLike(e syntax.Expr) bool {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.IfExpr:
		return x.Else == nil
	case *syntax.CatchExpr:
		stmts := x.Body.Stmts
		if len(stmts) == 0 {
			return true
		}
		_, isExpr := stmts[len(stmts)-1].(*syntax.ExprStmt)
		return !isExpr
	}
	return false
}

// isStatementCall reports whether x, a call ending a lambda, has results that
// cannot be the lambda's value: several values, or a value and an error.
func isStatementCall(e syntax.Expr, x *operand) bool {
	if callOf(e) == nil {
		return false
	}
	switch t := x.typ.(type) {
	case *types.Result:
		return len(t.Raw) > 1 // errors.New(...) is one value: an error
	case *types.Tuple:
		return len(t.Elems) != 1
	}
	return false
}

// tupleFits reports whether x, a call with several results, gives exactly
// results (Go types identical), so return x passes them on.
func (c *Checker) tupleFits(x *operand, results []types.Type) bool {
	t, ok := x.typ.(*types.Tuple)
	if !ok || len(t.Elems) != len(results) {
		return false
	}
	for i, el := range t.Elems {
		ok, conv, _ := c.assignable(el, results[i])
		if !ok || conv != ConvNone || !gotypes.Identical(types.Lower(el), types.Lower(results[i])) {
			return false
		}
	}
	return true
}
