package syntax

// Walk visits n and all nodes under it in depth-first order.
func Walk(n Node, f func(Node)) {
	if n == nil || isNilNode(n) {
		return
	}
	f(n)
	Children(n, func(c Node) { Walk(c, f) })
}

// Children calls f for each direct child node of n.
func Children(n Node, f func(Node)) {
	visit := func(c Node) {
		if c != nil && !isNilNode(c) {
			f(c)
		}
	}
	switch x := n.(type) {
	case *FunDecl:
		visit(x.Name)
		for _, p := range x.Params {
			visit(p.Name)
		}
		if x.Body != nil {
			visit(x.Body)
		}
		if x.ExprBody != nil {
			visit(x.ExprBody)
		}
	case *Block:
		for _, s := range x.Stmts {
			visit(s)
		}
	case *ConstDecl:
		visit(x.Name)
		if x.Value != nil {
			visit(x.Value)
		}
	case *StructDecl:
		visit(x.Name)
	case *LocalDecl:
		for _, id := range x.Names {
			visit(id)
		}
		if x.Value != nil {
			visit(x.Value)
		}
	case *AssignStmt:
		visit(x.Lhs)
		visit(x.Rhs)
	case *IncDecStmt:
		visit(x.X)
	case *ExprStmt:
		visit(x.X)
	case *ForStmt:
		for _, id := range x.Names {
			visit(id)
		}
		visit(x.Iter)
		visit(x.Body)
	case *WhileStmt:
		visit(x.Cond)
		visit(x.Body)
	case *SelectStmt:
		for _, sc := range x.Cases {
			visit(sc)
		}
	case *SelectCase:
		for _, id := range x.Names {
			visit(id)
		}
		if x.Op != nil {
			visit(x.Op)
		}
		visit(x.Body)
	case *DeferStmt:
		if x.Call != nil {
			visit(x.Call)
		}
		if x.Body != nil {
			visit(x.Body)
		}
	case *GoStmt:
		if x.Call != nil {
			visit(x.Call)
		}
		if x.Body != nil {
			visit(x.Body)
		}
	case *TemplateLit:
		for _, p := range x.Parts {
			if p.X != nil {
				visit(p.X)
			}
		}
	case *ParenExpr:
		visit(x.X)
	case *ListLit:
		for _, e := range x.Elems {
			visit(e)
		}
	case *MapLit:
		for i := range x.Keys {
			visit(x.Keys[i])
			visit(x.Values[i])
		}
	case *UnaryExpr:
		visit(x.X)
	case *BinaryExpr:
		visit(x.X)
		visit(x.Y)
	case *ElvisExpr:
		visit(x.X)
		visit(x.Y)
	case *RangeExpr:
		visit(x.Lo)
		visit(x.Hi)
	case *CallExpr:
		visit(x.Fun)
		for _, a := range x.Args {
			visit(a.Value)
		}
		if x.Trailing != nil {
			visit(x.Trailing)
		}
	case *InstantiateExpr:
		visit(x.X)
	case *SelectorExpr:
		visit(x.X)
	case *IndexExpr:
		visit(x.X)
		visit(x.Index)
	case *SliceExpr:
		visit(x.X)
		if x.Lo != nil {
			visit(x.Lo)
		}
		if x.Hi != nil {
			visit(x.Hi)
		}
		if x.Max != nil {
			visit(x.Max)
		}
	case *TryExpr:
		visit(x.X)
	case *NotNullExpr:
		visit(x.X)
	case *CatchExpr:
		visit(x.X)
		if x.Binder != nil {
			visit(x.Binder)
		}
		visit(x.Body)
	case *IfExpr:
		visit(x.Cond)
		visit(x.Then)
		if x.Else != nil {
			visit(x.Else)
		}
	case *WhenExpr:
		if x.Subject != nil {
			visit(x.Subject)
		}
		if x.Bind != nil {
			visit(x.Bind)
		}
		for _, br := range x.Branches {
			for _, c := range br.Conds {
				if c.X != nil {
					visit(c.X)
				}
			}
			visit(br.Body)
		}
	case *LambdaExpr:
		for _, p := range x.Params {
			visit(p.Name)
		}
		visit(x.Body)
	case *JumpExpr:
		for _, v := range x.Values {
			visit(v)
		}
	case *TupleExpr:
		for _, e := range x.Elems {
			visit(e)
		}
	case *IsExpr:
		visit(x.X)
	case *AsExpr:
		visit(x.X)
	case *InExpr:
		visit(x.X)
		visit(x.Y)
	}
}

func isNilNode(n Node) bool {
	switch x := n.(type) {
	case *Block:
		return x == nil
	case *Ident:
		return x == nil
	case *LambdaExpr:
		return x == nil
	}
	return false
}
