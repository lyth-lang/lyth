package lsp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
)

var goFiles sync.Map // path -> *parsedGo

type parsedGo struct {
	fset *token.FileSet
	file *ast.File
}

// goDoc returns the doc comment of the Go declaration at line of file, read
// from the source (export data has no comments).
func goDoc(file string, line int) string {
	v, ok := goFiles.Load(file)
	if !ok {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return ""
		}
		v, _ = goFiles.LoadOrStore(file, &parsedGo{fset, f})
	}
	p := v.(*parsedGo)
	at := func(n ast.Node) bool { return n != nil && p.fset.Position(n.Pos()).Line == line }
	doc := ""
	ast.Inspect(p.file, func(n ast.Node) bool {
		if doc != "" {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncDecl:
			if at(n.Name) {
				doc = n.Doc.Text()
			}
		case *ast.GenDecl:
			for _, spec := range n.Specs {
				var names []*ast.Ident
				var specDoc *ast.CommentGroup
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names, specDoc = []*ast.Ident{s.Name}, s.Doc
				case *ast.ValueSpec:
					names, specDoc = s.Names, s.Doc
				}
				for _, id := range names {
					if at(id) {
						doc = specDoc.Text()
						if doc == "" && len(n.Specs) == 1 {
							doc = n.Doc.Text()
						}
						if doc == "" {
							if vs, ok := spec.(*ast.ValueSpec); ok {
								doc = vs.Comment.Text()
							}
						}
						return false
					}
				}
			}
		case *ast.Field:
			for _, id := range n.Names {
				if at(id) {
					doc = n.Doc.Text()
					if doc == "" {
						doc = n.Comment.Text()
					}
					return false
				}
			}
		}
		return true
	})
	return doc
}
