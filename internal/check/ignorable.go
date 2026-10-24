package check

import (
	gotypes "go/types"

	"github.com/lyth-lang/lyth/internal/syntax"
	"github.com/lyth-lang/lyth/internal/types"
)

// ignorableErrors lists Go functions whose error results are conventionally
// ignored because they cannot meaningfully fail or failure is reported
// elsewhere. It is errcheck's default exclusion list, the de facto Go
// convention (spec/errors.md §4.4).
var ignorableErrors = map[string]bool{
	"fmt.Print":                       true,
	"fmt.Printf":                      true,
	"fmt.Println":                     true,
	"(*bytes.Buffer).Write":           true,
	"(*bytes.Buffer).WriteByte":       true,
	"(*bytes.Buffer).WriteRune":       true,
	"(*bytes.Buffer).WriteString":     true,
	"(*strings.Builder).Write":        true,
	"(*strings.Builder).WriteByte":    true,
	"(*strings.Builder).WriteRune":    true,
	"(*strings.Builder).WriteString":  true,
	"(hash.Hash).Write":               true,
	"(*io.PipeReader).CloseWithError": true,
	"(*io.PipeWriter).CloseWithError": true,
	"math/rand.Read":                  true,
	"(*math/rand.Rand).Read":          true,
}

// fprintFuncs are ignorable when writing to standard streams or in-memory
// buffers.
var fprintFuncs = map[string]bool{"fmt.Fprint": true, "fmt.Fprintf": true, "fmt.Fprintln": true}

// errorIgnorable reports whether the error of a call used as a statement
// may be dropped silently by convention.
func (c *Checker) errorIgnorable(call *syntax.CallExpr) bool {
	fn := c.calledGoFunc(call)
	if fn == nil {
		return false
	}
	name := fn.FullName()
	if ignorableErrors[name] {
		return true
	}
	if fprintFuncs[name] && len(call.Args) > 0 {
		w := syntax.Unparen(call.Args[0].Value)
		if sel, ok := w.(*syntax.SelectorExpr); ok {
			if g, ok := c.info.Uses[sel.Sel].(*GoObj); ok && g.Obj.Pkg() != nil && g.Obj.Pkg().Path() == "os" &&
				(g.Obj.Name() == "Stdout" || g.Obj.Name() == "Stderr") {
				return true
			}
		}
		if t := c.info.TypeOf(w); t != nil {
			if p, ok := types.Base(t).(*types.Pointer); ok {
				if n, ok := types.Base(p.Elem).(*types.Named); ok && n.Go != nil && n.Go.Obj().Pkg() != nil {
					full := n.Go.Obj().Pkg().Path() + "." + n.Go.Obj().Name()
					return full == "bytes.Buffer" || full == "strings.Builder"
				}
			}
		}
	}
	return false
}

// calledGoFunc returns the Go function or method a call resolves to.
func (c *Checker) calledGoFunc(call *syntax.CallExpr) *gotypes.Func {
	switch f := syntax.Unparen(call.Fun).(type) {
	case *syntax.SelectorExpr:
		if sel := c.info.Selections[f]; sel != nil {
			if fn, ok := sel.Obj.(*gotypes.Func); ok {
				return fn
			}
		}
	}
	return nil
}
