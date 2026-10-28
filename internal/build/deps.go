package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	gotypes "go/types"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/types"
)

// Lyth packages of other modules (ADR 0053). A module the program requires
// may publish Lyth packages: .lyth sources beside the Go that lyth gen
// -in-tree generated from them. Lyth code that imports one sees its Lyth
// declarations (nullability, defaults, enums, sealed interfaces) from the
// sources, which are checked with the program; the go command builds the
// published Go, since files in the module cache cannot be overlaid. A module
// replaced by a local directory is compiled with the program instead, as a
// workspace module is.

// requirement is a module the main module's go.mod requires, with the
// module that provides its code after replace directives.
type requirement struct {
	path    string
	version string // of the module that provides the code
	dir     string // a local replacement; "" for the module cache
	modPath string // the path the code is downloaded as (a replacement's)
}

// requirements parses the main module's go.mod once. Since Go 1.17 it lists
// every module that provides a package the module's build imports.
func (s *Session) requirements() []requirement {
	if s.reqs != nil || s.Mod == nil || s.Mod.Synthetic {
		return s.reqs
	}
	s.reqs = []requirement{}
	file := filepath.Join(s.Mod.Root, "go.mod")
	data, err := os.ReadFile(file)
	if err != nil {
		return s.reqs
	}
	mf, err := modfile.Parse(file, data, nil)
	if err != nil {
		return s.reqs
	}
	for _, r := range mf.Require {
		req := requirement{path: r.Mod.Path, version: r.Mod.Version, modPath: r.Mod.Path}
		for _, rep := range mf.Replace {
			if rep.Old.Path != r.Mod.Path || rep.Old.Version != "" && rep.Old.Version != r.Mod.Version {
				continue
			}
			if rep.New.Version == "" {
				req.dir = rep.New.Path
				if !filepath.IsAbs(req.dir) {
					req.dir = filepath.Join(s.Mod.Root, req.dir)
				}
			} else {
				req.modPath, req.version = rep.New.Path, rep.New.Version
			}
		}
		s.reqs = append(s.reqs, req)
	}
	// longest module path first, so a nested module wins
	sort.Slice(s.reqs, func(i, j int) bool { return len(s.reqs[i].path) > len(s.reqs[j].path) })
	return s.reqs
}

// replacedPackages returns the Lyth packages of the modules replaced by a
// local directory, compiled with the program like workspace modules: they
// are generated under .lyth/gen/_dep/<module path>/.
func (s *Session) replacedPackages(mod *Module, skip []*pkgDir) []*pkgDir {
	var out []*pkgDir
	for _, r := range s.requirements() {
		if r.dir == "" {
			continue
		}
		other, err := FindModule(r.dir)
		if err != nil || other.Root == mod.Root {
			continue
		}
		pkgs, err := discover(&Module{Root: other.Root, Path: r.path})
		if err != nil {
			continue
		}
		for _, p := range pkgs {
			if !containsDir(skip, p.Dir) {
				p.Rel = path.Join("_dep", strings.ReplaceAll(r.path, "/", "_"), p.Rel)
				out = append(out, p)
			}
		}
	}
	return out
}

func containsDir(ps []*pkgDir, dir string) bool {
	for _, p := range ps {
		if p.Dir == dir {
			return true
		}
	}
	return false
}

// externalPackage returns the Lyth package with import path importPath in a
// module of the module cache, or nil when no required module provides one
// (a Go package, the standard library, a package of this module).
func (s *Session) externalPackage(importPath string) *pkgDir {
	if p, ok := s.externals[importPath]; ok {
		return p
	}
	if s.externals == nil {
		s.externals = map[string]*pkgDir{}
	}
	var p *pkgDir
	if first, _, _ := strings.Cut(importPath, "/"); strings.Contains(first, ".") {
		p = s.findExternal(importPath)
	}
	s.externals[importPath] = p
	return p
}

func (s *Session) findExternal(importPath string) *pkgDir {
	for _, r := range s.requirements() {
		rel, ok := strings.CutPrefix(importPath, r.path)
		if !ok || rel != "" && rel[0] != '/' {
			continue
		}
		if r.dir != "" {
			return nil // compiled with the program (replacedPackages)
		}
		root := s.moduleDir(r)
		if root == "" {
			return nil
		}
		p, err := scanDir(&Module{Root: root, Path: r.path}, filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/"))))
		if err != nil || p == nil || len(p.Files) == 0 {
			return nil
		}
		p.TestFiles, p.GoTestFiles = nil, nil // a dependency's tests are its own
		p.External = true
		p.Module = r.modPath + "@" + r.version
		return p
	}
	return nil
}

// moduleDir returns the directory of a required module in the module cache,
// downloading it as the go command would, or "" when it cannot be had.
func (s *Session) moduleDir(r requirement) string {
	if cache := s.goEnvVar("GOMODCACHE"); cache != "" {
		if ep, err := module.EscapePath(r.modPath); err == nil {
			if ev, err := module.EscapeVersion(r.version); err == nil {
				dir := filepath.Join(cache, filepath.FromSlash(ep)+"@"+ev)
				if info, err := os.Stat(dir); err == nil && info.IsDir() {
					return dir
				}
			}
		}
	}
	cmd := exec.Command("go", "mod", "download", "-json", r.modPath+"@"+r.version)
	cmd.Dir = s.Mod.Root
	if s.cfg.Env != nil {
		cmd.Env = s.cfg.Env
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return ""
	}
	var info struct{ Dir string }
	if json.Unmarshal(out.Bytes(), &info) != nil {
		return ""
	}
	return info.Dir
}

func (s *Session) goEnvVar(name string) string {
	if s.goEnv == nil {
		return ""
	}
	return s.goEnv.Vars[name]
}

// settleExternal decides how the program sees a checked package of another
// module: through its Lyth declarations when they compile with this lyth and
// match the published Go, else through the published Go, with a warning at
// each import. Its own diagnostics stay in its sink: a dependency's
// warnings are not the program's.
func (s *Session) settleExternal(p *Package) {
	gp := s.goPkgs[p.Dir.ImportPath]
	if gp == nil || gp.Types == nil {
		p.Checked = nil
		p.goView = "has .lyth sources but no Go: a Lyth library publishes the Go that lyth gen -in-tree writes beside them"
		return
	}
	reason, hint := "", fmt.Sprintf("update %s, or the lyth that builds this module (lyth version)", modulePathOf(p.Dir.Module))
	if p.depSink.HasErrors() || p.Checked == nil || p.Checked.Go == nil {
		for _, d := range p.depSink.All() {
			if d.Severity == diag.Error {
				reason = "its Lyth sources do not compile with this lyth: " + d.Message
				if d.Span.File != nil {
					pos := d.Span.StartPos()
					reason = fmt.Sprintf("its Lyth sources do not compile with this lyth (%s:%d: %s)", d.Span.File.Base(), pos.Line, d.Message)
				}
				break
			}
		}
		if reason == "" {
			reason = "its Lyth sources do not compile with this lyth"
		}
	} else if diff := apiDiff(p.Checked, gp.Types); diff != "" {
		reason = "its published Go does not match its Lyth sources (" + diff + ")"
		hint = fmt.Sprintf("%s publishes Go that lyth gen -in-tree did not write from these sources; its author regenerates it", p.Dir.Module)
	}
	if reason == "" {
		return
	}
	p.Checked = nil
	p.goView = reason
	for _, q := range s.Packages {
		if q.Dir.External {
			continue
		}
		for _, f := range q.Syntax {
			for _, imp := range f.Imports {
				if imp.Path == p.Dir.ImportPath {
					s.Sink.Warnf(diag.WGoView, imp.PathSpan, "%s is used through its Go API: %s", imp.Path, reason).
						WithNote("its nullable types, default arguments, enums, and sealed interfaces are seen as Go declares them").
						WithHint("%s", hint)
				}
			}
		}
	}
}

func modulePathOf(modVersion string) string {
	p, _, _ := strings.Cut(modVersion, "@")
	return p
}

// apiDiff compares the exported API a Lyth package lowers to (its shadow
// types and its functions) with the published Go's, and describes the first
// difference, or returns "".
func apiDiff(lp *check.Package, published *gotypes.Package) string {
	scope := lp.Go.Scope()
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*gotypes.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		pub, ok := published.Scope().Lookup(name).(*gotypes.TypeName)
		if !ok {
			return "type " + name + " is missing from the Go"
		}
		if a, b := typeKey(obj.Type().Underlying()), typeKey(pub.Type().Underlying()); a != b {
			return fmt.Sprintf("type %s is %s in Lyth and %s in Go", name, a, b)
		}
		ms, mp := gotypes.NewMethodSet(gotypes.NewPointer(obj.Type())), gotypes.NewMethodSet(gotypes.NewPointer(pub.Type()))
		for i := range ms.Len() {
			m := ms.At(i).Obj()
			if !m.Exported() {
				continue
			}
			pm := mp.Lookup(published, m.Name())
			if pm == nil {
				return fmt.Sprintf("method %s.%s is missing from the Go", name, m.Name())
			}
			if a, b := typeKey(m.Type()), typeKey(pm.Obj().Type()); a != b {
				return fmt.Sprintf("%s.%s is %s in Lyth and %s in Go", name, m.Name(), a, b)
			}
		}
	}
	for _, fn := range lp.Funcs {
		if !fn.Pub || fn.Recv != nil || fn.Sig == nil {
			continue
		}
		sig := types.LowerSignature(fn.Sig, nil)
		if fn.ExtRecv != nil {
			// an extension function receives its receiver first
			params := []*gotypes.Var{gotypes.NewParam(0, nil, "", types.Lower(fn.ExtRecv))}
			for i := range sig.Params().Len() {
				params = append(params, sig.Params().At(i))
			}
			sig = gotypes.NewSignatureType(nil, nil, nil, gotypes.NewTuple(params...), sig.Results(), sig.Variadic())
		}
		pub, ok := published.Scope().Lookup(fn.GoName).(*gotypes.Func)
		if !ok {
			return fn.GoName + " is missing from the Go"
		}
		if a, b := typeKey(sig), typeKey(pub.Type()); a != b {
			return fmt.Sprintf("%s is %s in Lyth and %s in Go", fn.GoName, a, b)
		}
	}
	return ""
}

// typeKey spells a Go type for comparing two packages' APIs: by structure,
// without parameter names or struct tags, and with named types by path.
func typeKey(t gotypes.Type) string {
	switch t := t.(type) {
	case *gotypes.Signature:
		key := func(tup *gotypes.Tuple) string {
			parts := make([]string, tup.Len())
			for i := range parts {
				parts[i] = typeKey(tup.At(i).Type())
			}
			return strings.Join(parts, ", ")
		}
		variadic := ""
		if t.Variadic() {
			variadic = "..."
		}
		return "func(" + key(t.Params()) + variadic + ") (" + key(t.Results()) + ")"
	case *gotypes.Struct:
		parts := make([]string, t.NumFields())
		for i := range parts {
			f := t.Field(i)
			parts[i] = f.Name() + " " + typeKey(f.Type())
		}
		return "struct{" + strings.Join(parts, "; ") + "}"
	case *gotypes.Pointer:
		return "*" + typeKey(t.Elem())
	case *gotypes.Slice:
		return "[]" + typeKey(t.Elem())
	case *gotypes.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), typeKey(t.Elem()))
	case *gotypes.Map:
		return "map[" + typeKey(t.Key()) + "]" + typeKey(t.Elem())
	case *gotypes.Chan:
		return fmt.Sprintf("chan(%d) %s", t.Dir(), typeKey(t.Elem()))
	case *gotypes.Interface:
		var parts []string
		for i := range t.NumMethods() {
			if m := t.Method(i); m.Exported() {
				parts = append(parts, m.Name()+typeKey(m.Type()))
			}
		}
		sort.Strings(parts)
		return "interface{" + strings.Join(parts, "; ") + "}"
	}
	return gotypes.TypeString(t, func(p *gotypes.Package) string { return p.Path() })
}
