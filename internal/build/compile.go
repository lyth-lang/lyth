package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	gobuild "go/build"
	"go/build/constraint"
	"go/token"
	gotypes "go/types"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/lyth-lang/lyth/internal/check"
	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/emit"
	"github.com/lyth-lang/lyth/internal/golang"
	"github.com/lyth-lang/lyth/internal/lower"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
)

// MinGoMinor is the minimum supported Go 1.x toolchain (generated code uses
// Go 1.26 features such as new(expr)).
const MinGoMinor = 26

// supportImports are standard packages the generated code may use.
var supportImports = []string{"errors", "fmt", "os", "slices", "strconv", "strings"}

// Config configures a compilation session.
type Config struct {
	WorkDir          string // directory lyth was invoked from
	Verbose          bool
	NoLineDirectives bool
	InTree           bool // generate for in-tree output (bare file names in //line)
	// OnlyFile builds a file target alone, without the other files of its
	// package, as go run file.go does (lyth run file.lyth).
	OnlyFile bool
	// SparseLineDirectives emits //line only before lines that can fail at
	// run time (lyth gen: output meant for reading). Builds put one wherever
	// the Lyth line changes; in-tree output is always sparse.
	SparseLineDirectives bool
	Timings              bool // record phase durations (lyth -timings)
	Tests                bool // also compile the _test.lyth files of the target packages
	Tags                 []string
	Env                  []string
	Stdout, Stderr       io.Writer
	// Overlay replaces the contents of source files, by absolute path: an
	// editor's unsaved buffers.
	Overlay map[string][]byte
	// NoWrite stops after validation: nothing is written to .lyth/gen, for
	// analysis only (the language server).
	NoWrite bool
}

// Package is one Lyth package in a session.
type Package struct {
	Dir       *pkgDir
	Files     []*source.File
	Syntax    []*syntax.File
	Checked   *check.Package
	Generated []*emit.Result
	// Lowered are the files Generated was printed from, in the same order,
	// kept to print them again for coverage runs (cover.go).
	Lowered []*lower.File
	GoTypes *gotypes.Package // types of the generated Go (validation)
	// depSink holds the diagnostics of an external package's sources, and
	// goView why the program sees it through its published Go (deps.go).
	depSink *diag.Sink
	goView  string

	generated, validated bool
}

// Session holds the state of one compilation.
type Session struct {
	cfg      *Config
	Mod      *Module
	Sink     *diag.Sink
	Packages []*Package // in dependency order
	byPath   map[string]*Package
	loader   *golang.Loader
	goEnv    *golang.Env
	goPkgs   map[string]*golang.Package
	GenDir   string
	Overlay  string
	// ModFile is the go.mod that go commands use (-modfile) when the
	// program imports the standard library; StdRoot is the library's
	// directory in the user cache.
	ModFile string
	// WorkFile is the go.work that go commands use (GOWORK) when the module
	// is in a workspace, which rejects -modfile: the workspace's modules and
	// replacements with the standard library added.
	WorkFile string
	StdRoot  string
	// Maps from generated file (virtual path in the package directory, or
	// physical path in single-file mode) to its source map.
	Maps map[string]*emit.SourceMap
	// coverGen holds, for a coverage run on Go 1.26, the generated file
	// printed again without //line directives, by the file it replaces.
	coverGen map[*emit.Result]*emit.Result
	// Timings holds phase durations in order.
	Timings []Timing
	// Targets are the packages selected on the command line (all packages
	// when none were named). Other packages are compiled as dependencies.
	Targets []*Package
	// available holds every Lyth package discovered in the module, by import
	// path; packages are added to the session when targeted or imported.
	available map[string]*pkgDir
	// bridges are the Go packages of the module that import Lyth packages
	// (bridge.go), by import path; nil marks a plain Go package.
	bridges map[string]*bridge
	// goTypes indexes the loaded Go packages and their dependencies by
	// path, for validation; goTypesN is len(goPkgs) when it was built.
	goTypes  map[string]*gotypes.Package
	goTypesN int
	// reqs are the main module's requirements, and externals the Lyth
	// packages of the module cache by import path (nil: none), both lazily.
	reqs      []requirement
	externals map[string]*pkgDir
	// withModules are the roots of the other modules compiled with the
	// program: workspace modules and local replacements.
	withModules []string
}

// Timing is the duration of one compilation phase.
type Timing struct {
	Phase    string
	Duration time.Duration
}

// phase records the time since start under name and returns a new start.
func (s *Session) phase(name string, start time.Time) time.Time {
	now := time.Now()
	s.Timings = append(s.Timings, Timing{Phase: name, Duration: now.Sub(start)})
	return now
}

// Target selects what to compile.
type Target struct {
	File     string   // a single .lyth file (outside or inside a module)
	Patterns []string // package directories relative to WorkDir; empty means all
}

// Compile runs the front end, lowering, emission, and validation, and writes
// generated files. Diagnostics are in Session.Sink; the error is non-nil only
// for problems that are not about user code (I/O, toolchain).
func Compile(cfg *Config, target Target) (*Session, error) {
	s := &Session{cfg: cfg, Sink: &diag.Sink{}, byPath: map[string]*Package{}, Maps: map[string]*emit.SourceMap{}}
	if wd, err := canonical(cfg.WorkDir); err == nil {
		cfg.WorkDir = wd
	}
	t := time.Now()
	if err := s.setup(target); err != nil {
		return s, err
	}
	if err := s.checkToolchain(); err != nil {
		return s, err
	}
	defer s.explainNewerGo()
	if !s.checkGoDirective() || !s.checkLythDirective() {
		return s, nil
	}
	t = s.phase("setup", t)
	s.parse()
	if s.StdRoot != "" {
		if err := s.writeModFile(); err != nil {
			return s, err
		}
	}
	t = s.phase("parse", t)
	if s.Mod.Synthetic {
		s.embedOutsideModule()
	}
	if !s.order() {
		return s, nil
	}
	if err := s.loadGo(); err != nil {
		return s, s.newerGoError(err)
	}
	if s.loader.CacheHit {
		t = s.phase("load Go packages (cached)", t)
	} else {
		t = s.phase("load Go packages", t)
	}
	s.check()
	t = s.phase("check", t)
	if s.Sink.HasErrors() {
		return s, nil
	}
	if err := s.generate(); err != nil {
		return s, err
	}
	t = s.phase("lower+emit", t)
	s.validate()
	t = s.phase("validate", t)
	if s.Sink.HasErrors() || s.cfg.NoWrite {
		return s, nil
	}
	err := s.write()
	s.phase("write", t)
	return s, err
}

func (s *Session) setup(t Target) error {
	wd := s.cfg.WorkDir
	if t.File != "" {
		file := t.File
		if !filepath.IsAbs(file) {
			file = filepath.Join(wd, file)
		}
		abs, err := canonical(file)
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); err != nil {
			if _, open := s.cfg.Overlay[abs]; !open {
				return fmt.Errorf("cannot read %s: %w", t.File, err)
			}
		}
		mod, err := FindModule(filepath.Dir(abs))
		if err == nil {
			s.Mod = mod
			p, err := scanDir(mod, filepath.Dir(abs))
			if err != nil {
				return err
			}
			p = s.editorFiles(mod, filepath.Dir(abs), p)
			if s.cfg.OnlyFile {
				p.Others = len(p.Files) - 1
				p.Files, p.TestFiles = []string{abs}, nil
			}
			// Other Lyth packages of the module are available as imports.
			all, err := discover(mod)
			if err != nil {
				return err
			}
			for _, d := range s.editorDirs(mod) {
				if i := slices.IndexFunc(all, func(q *pkgDir) bool { return q.Dir == d }); i >= 0 {
					all[i] = s.editorFiles(mod, d, all[i])
				} else {
					all = append(all, s.editorFiles(mod, d, nil))
				}
			}
			s.makeAvailable(append(all, s.otherModules(mod)...))
			s.Targets = append(s.Targets, s.addPkg(p))
			return nil
		}
		// Outside any module: synthesize one in the user cache.
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		sum := sha256.Sum256([]byte(abs))
		root := filepath.Join(cache, "lyth", "run", hex.EncodeToString(sum[:8]))
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module lythrun\n\ngo 1.26\n"), 0o644); err != nil {
			return err
		}
		s.Mod = &Module{Root: root, Path: "lythrun", Synthetic: true}
		s.Targets = append(s.Targets, s.addPkg(&pkgDir{Dir: filepath.Dir(abs), Rel: ".", ImportPath: "lythrun", Files: []string{abs}}))
		return nil
	}
	mod, err := FindModule(wd)
	if err != nil {
		if err == errNoModule {
			return fmt.Errorf("no go.mod found in %s or any parent directory\n\tcreate one with: go mod init <module-path>\n\tor run a single file: lyth run file.lyth", wd)
		}
		return err
	}
	s.Mod = mod
	all, err := discover(mod)
	if err != nil {
		return err
	}
	for _, d := range s.editorDirs(mod) {
		if i := slices.IndexFunc(all, func(q *pkgDir) bool { return q.Dir == d }); i >= 0 {
			all[i] = s.editorFiles(mod, d, all[i])
		} else {
			all = append(all, s.editorFiles(mod, d, nil))
		}
	}
	if len(all) == 0 {
		return fmt.Errorf("no .lyth files found in module %s", mod.Path)
	}
	// Every Lyth package is compiled (dependencies resolve from the full set);
	// the selected ones become the targets.
	selected := map[string]bool{}
	for _, pat := range t.Patterns {
		recursive := pat == "./..." || pat == "..." || strings.HasSuffix(pat, "/...")
		dir := patternDir(strings.TrimSuffix(strings.TrimSuffix(pat, "..."), "/"), wd, mod)
		found := false
		for _, p := range all {
			if p.Dir == dir || recursive && strings.HasPrefix(p.Dir, dir+string(filepath.Separator)) {
				selected[p.Dir] = true
				found = true
			}
		}
		if !found {
			return fmt.Errorf("no Lyth package in %s", pat)
		}
	}
	s.makeAvailable(append(all, s.otherModules(mod)...))
	var testOnly []string
	for _, p := range all {
		if len(t.Patterns) == 0 || selected[p.Dir] {
			if p.TestOnly() && !s.cfg.Tests {
				testOnly = append(testOnly, p.Rel)
				continue // only lyth test compiles a directory of tests
			}
			s.Targets = append(s.Targets, s.addPkg(p))
		}
	}
	if len(s.Targets) == 0 && len(testOnly) > 0 {
		return fmt.Errorf("%s has only test files; run them with lyth test ./%s", testOnly[0], testOnly[0])
	}
	return nil
}

// editorDirs returns the directories of the module that hold .lyth files
// only an editor has (Config.Overlay: a new file not saved yet).
func (s *Session) editorDirs(mod *Module) []string {
	var dirs []string
	for path := range s.cfg.Overlay {
		if !strings.HasSuffix(path, ".lyth") || fileExists(path) {
			continue
		}
		dir := filepath.Dir(path)
		if rel, err := filepath.Rel(mod.Root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// editorFiles adds to p, the package in dir (nil when it has no files on
// disk), the .lyth files there that only an editor has.
func (s *Session) editorFiles(mod *Module, dir string, p *pkgDir) *pkgDir {
	for path := range s.cfg.Overlay {
		if filepath.Dir(path) != dir || !strings.HasSuffix(path, ".lyth") || fileExists(path) {
			continue
		}
		if p == nil {
			rel, _ := filepath.Rel(mod.Root, dir)
			p = &pkgDir{Dir: dir, Rel: filepath.ToSlash(rel), ImportPath: mod.Path}
			if p.Rel != "." {
				p.ImportPath += "/" + p.Rel
			}
		}
		if strings.HasSuffix(path, "_test.lyth") {
			if !slices.Contains(p.TestFiles, path) {
				p.TestFiles = append(p.TestFiles, path)
				sort.Strings(p.TestFiles)
			}
		} else if !slices.Contains(p.Files, path) {
			p.Files = append(p.Files, path)
			sort.Strings(p.Files)
		}
	}
	return p
}

// patternDir returns the directory a package pattern (without /...) names:
// a directory relative to wd or absolute, or, as for the go command, an
// import path of the module (example.com/app/cmd/api).
func patternDir(pat, wd string, mod *Module) string {
	switch {
	case pat == "" || pat == ".":
		return wd
	case filepath.IsAbs(pat):
		return filepath.Clean(pat)
	case pat == mod.Path:
		return mod.Root
	case strings.HasPrefix(pat, mod.Path+"/") && !strings.HasPrefix(pat, "."):
		return filepath.Join(mod.Root, filepath.FromSlash(strings.TrimPrefix(pat, mod.Path+"/")))
	}
	return filepath.Join(wd, pat)
}

// otherModules returns the Lyth packages of other modules compiled with the
// program: those of the workspace, and of modules replaced by a directory.
func (s *Session) otherModules(mod *Module) []*pkgDir {
	ws := s.workspacePackages(mod)
	return append(ws, s.replacedPackages(mod, ws)...)
}

// workspacePackages returns the Lyth packages of the other modules of the
// workspace mod is in (go.work), which its code may import. They are
// generated under .lyth/gen/_ws/<module path>/, and the overlay points the
// go command at them in their own directories.
func (s *Session) workspacePackages(mod *Module) []*pkgDir {
	work := s.workspace()
	if work == "" {
		return nil
	}
	data, err := os.ReadFile(work)
	if err != nil {
		return nil
	}
	wf, err := modfile.ParseWork(work, data, nil)
	if err != nil {
		return nil
	}
	var out []*pkgDir
	for _, u := range wf.Use {
		dir := u.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(filepath.Dir(work), dir)
		}
		other, err := FindModule(dir)
		if err != nil || other.Root == mod.Root {
			continue
		}
		pkgs, err := discover(other)
		if err != nil {
			continue
		}
		s.withModules = append(s.withModules, other.Root)
		for _, p := range pkgs {
			p.Rel = path.Join("_ws", strings.ReplaceAll(other.Path, "/", "_"), p.Rel)
			out = append(out, p)
		}
	}
	return out
}

func (s *Session) makeAvailable(all []*pkgDir) {
	s.available = map[string]*pkgDir{}
	for _, p := range all {
		if !p.TestOnly() { // a package of tests cannot be imported
			s.available[p.ImportPath] = p
		}
	}
}

func (s *Session) addPkg(p *pkgDir) *Package {
	pkg := &Package{Dir: p}
	s.Packages = append(s.Packages, pkg)
	s.byPath[p.ImportPath] = pkg
	return pkg
}

// checkGoDirective reports a go.mod whose go directive is older than the
// Go Lyth's semantics and generated code need: Go 1.22 gives each loop
// iteration its own variable, as Lyth's loops promise, and the generated
// code uses Go 1.26's new(expr).
func (s *Session) checkGoDirective() bool {
	if s.Mod.Synthetic {
		return true
	}
	path := filepath.Join(s.Mod.Root, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	v, line := GoDirective(string(data))
	if goMinor(v) >= MinGoMinor {
		return true
	}
	f := source.NewFile(path, displayName(path, s.cfg.WorkDir), data)
	span := source.Span{File: f}
	if line > 0 {
		start := f.LineStart(line)
		span = source.Span{File: f, Start: start, End: start + len(strings.TrimRight(f.LineText(line), "\r"))}
	}
	what := "go.mod has no go directive, which means go 1.16"
	if v != "" {
		what = "go.mod declares go " + v
	}
	s.Sink.Errorf(diag.EToolchain, span, "%s; Lyth needs go 1.%d or newer", what, MinGoMinor).
		WithNote("Lyth's loops give each iteration its own variable, as Go does since go 1.22, and the generated code uses Go 1.%d's new(expr)", MinGoMinor).
		WithHint("update the module: go mod edit -go=1.%d", MinGoMinor)
	return false
}

func (s *Session) checkToolchain() error {
	env, err := golang.GoEnv(s.Mod.Root, s.cfg.Env)
	if err != nil {
		return fmt.Errorf("the go command is required: %v", err)
	}
	s.goEnv = env
	v, minor := env.Version()
	if minor != 0 && minor < MinGoMinor {
		return fmt.Errorf("lyth requires Go 1.%d or newer; found %s\n\tinstall a newer toolchain from https://go.dev/dl/ or set GOTOOLCHAIN=go1.%d.0", MinGoMinor, v, MinGoMinor)
	}
	return nil
}

// parse parses the session's packages, adding Lyth packages of the module
// that they import (transitively).
func (s *Session) parse() {
	if s.available == nil {
		s.available = map[string]*pkgDir{} // single-file builds
	}
	for i := 0; i < len(s.Packages); i++ {
		p := s.Packages[i]
		paths := p.Dir.Files
		if s.cfg.Tests && slices.Contains(s.Targets, p) {
			// Like go test, only the packages under test get their tests.
			paths = append(slices.Clip(paths), p.Dir.TestFiles...)
		}
		sink := s.Sink
		if p.Dir.External {
			// a dependency's diagnostics are not the program's (settleExternal)
			p.depSink = &diag.Sink{}
			s.Sink = p.depSink
		}
		for _, path := range paths {
			data, ok := s.cfg.Overlay[path]
			var err error
			if !ok {
				data, err = os.ReadFile(path)
			}
			if err != nil {
				s.Sink.Errorf(diag.EImport, source.Span{}, "cannot read %s: %v", path, err)
				continue
			}
			f := source.NewFile(path, displayName(path, s.cfg.WorkDir), data)
			var sf *syntax.File
			if protect(s.Sink, source.Span{File: f}, "while parsing "+f.Name, func() { sf = syntax.Parse(f, s.Sink) }) != nil {
				continue
			}
			if !s.buildsFile(path, sf) {
				continue // another platform's file, or excluded by //go:build
			}
			into := p
			if sf.Package != nil && strings.HasSuffix(sf.Package.Name, "_test") && strings.HasSuffix(path, "_test.lyth") {
				// An external test package, as Go's: the tests import the
				// package under test like any other importer would.
				into = s.externalTests(p)
			}
			into.Files = append(into.Files, f)
			into.Syntax = append(into.Syntax, sf)
			for _, imp := range sf.Imports {
				if _, ok := s.available[imp.Path]; !ok {
					if dep := s.stdPackage(imp.Path); dep != nil {
						s.available[imp.Path] = dep
					} else if dep := s.externalPackage(imp.Path); dep != nil {
						s.available[imp.Path] = dep
					}
				}
				if dep, ok := s.available[imp.Path]; ok && s.byPath[imp.Path] == nil {
					s.addPkg(dep)
				} else if !ok {
					s.bridgeFor(imp.Path) // a Go package of the module that may reach Lyth code
				}
			}
		}
		s.Sink = sink
		if len(p.Dir.GoFiles) > 0 && len(p.Files) > 0 {
			s.Sink.Errorf(diag.EUnsupported, source.Span{File: p.Files[0], Start: 0, End: 0},
				"package %s mixes Go and Lyth files, which is not supported yet", p.Dir.ImportPath).
				WithNote("Go files: %s", strings.Join(baseNames(p.Dir.GoFiles), ", ")).
				WithHint("move the Go code into its own package and import it from Lyth")
		}
	}
}

// externalTests returns the external test package (package x_test) of the
// directory of p, adding it to the session after p.
func (s *Session) externalTests(p *Package) *Package {
	path := p.Dir.ImportPath + "_test"
	if xp := s.byPath[path]; xp != nil {
		return xp
	}
	dir := *p.Dir
	dir.ImportPath, dir.Files, dir.TestFiles, dir.GoFiles, dir.GoTestFiles = path, nil, nil, nil, nil
	xp := &Package{Dir: &dir}
	s.byPath[path] = xp
	s.Packages = append(s.Packages, xp)
	return xp
}

// embedOutsideModule reports //go:embed in a file built outside a module:
// the program is built in a directory of the user cache, where the
// patterns would not find the files beside the source.
func (s *Session) embedOutsideModule() {
	for _, p := range s.Packages {
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				pd, ok := d.(*syntax.PropDecl)
				if !ok {
					continue
				}
				for _, dir := range pd.Directives {
					if dir.Name() == "embed" {
						s.Sink.Errorf(diag.EUnsupported, dir.S, "//go:embed needs a module: this file is not in one").
							WithHint("create a module in its directory with lyth init, then lyth run .")
					}
				}
			}
		}
	}
}

// buildsFile reports whether the build includes a parsed file, as go build
// decides for the Go file it becomes: its name's _GOOS and _GOARCH
// suffixes, and its //go:build line, for the session's GOOS, GOARCH, and
// tags. A malformed //go:build line is reported.
func (s *Session) buildsFile(path string, sf *syntax.File) bool {
	content := "package p\n"
	if c := sf.BuildConstraint; c != "" {
		if _, err := constraint.Parse(c); err != nil {
			span := source.Span{File: sf.Src}
			for _, cm := range sf.Comments {
				if cm.Text == c {
					span = cm.Span
				}
			}
			s.Sink.Errorf(diag.EUnsupportedSyntax, span, "invalid //go:build line: %v", strings.TrimPrefix(err.Error(), "parsing //go:build line: ")).
				WithHint("write a boolean expression of build tags: //go:build linux && !arm")
			return true
		}
		content = c + "\n\npackage p\n"
	}
	ctx := gobuild.Default
	if s.goEnv != nil {
		if v := s.goEnv.Vars["GOOS"]; v != "" {
			ctx.GOOS = v
		}
		if v := s.goEnv.Vars["GOARCH"]; v != "" {
			ctx.GOARCH = v
		}
		ctx.CgoEnabled = s.goEnv.Vars["CGO_ENABLED"] == "1"
	}
	ctx.BuildTags = s.cfg.Tags
	ctx.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(content)), nil }
	name := strings.TrimSuffix(filepath.Base(path), ".lyth") + ".go"
	ok, err := ctx.MatchFile(filepath.Dir(path), name)
	return err != nil || ok
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// order sorts packages so that dependencies come first.
func (s *Session) order() bool {
	state := map[*Package]int{}
	var sorted, stack []*Package
	ok := true
	var visit func(p *Package, via *syntax.ImportDecl)
	visit = func(p *Package, via *syntax.ImportDecl) {
		switch state[p] {
		case 1:
			if via != nil {
				// a imports b, b imports a: the packages on the cycle, from p.
				i := slices.Index(stack, p)
				var steps []string
				for j := i; j >= 0 && j < len(stack); j++ {
					next := p
					if j+1 < len(stack) {
						next = stack[j+1]
					}
					steps = append(steps, stack[j].Dir.ImportPath+" imports "+next.Dir.ImportPath)
				}
				s.Sink.Errorf(diag.EImport, via.PathSpan, "import cycle: %s", strings.Join(steps, ", ")).
					WithHint("move what the packages share into a package of its own, or merge them")
			}
			ok = false
			return
		case 2:
			return
		}
		state[p] = 1
		stack = append(stack, p)
		defer func() { stack = stack[:len(stack)-1] }()
		for _, f := range p.Syntax {
			for _, imp := range f.Imports {
				if dep := s.byPath[imp.Path]; dep != nil {
					visit(dep, imp)
				}
				if b := s.bridges[imp.Path]; b != nil {
					// The Go package needs the Go of the Lyth packages it imports.
					for _, lp := range b.lyth {
						if dep := s.byPath[lp.ImportPath]; dep != nil {
							visit(dep, imp)
						}
					}
				}
			}
		}
		state[p] = 2
		sorted = append(sorted, p)
	}
	for _, p := range s.Packages {
		visit(p, nil)
	}
	s.Packages = sorted
	return ok
}

func (s *Session) loadGo() error {
	need := map[string]bool{}
	for _, imp := range supportImports {
		need[imp] = true
	}
	for _, p := range s.Packages {
		for _, f := range p.Syntax {
			for _, imp := range f.Imports {
				if dep := s.byPath[imp.Path]; (dep == nil || dep.Dir.External) && s.bridges[imp.Path] == nil && imp.Path != "" {
					need[imp.Path] = true // an external Lyth package builds from its published Go
				}
			}
		}
	}
	for _, imp := range s.bridgeImports() {
		if s.byPath[imp] == nil {
			need[imp] = true
		}
	}
	var paths []string
	for p := range need {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	s.loader = golang.NewLoader(s.Mod.Root, s.cfg.Env, s.cfg.Tags)
	s.loader.GoEnv = s.goEnv
	if os.Getenv("LYTH_NO_CACHE") == "" {
		s.loader.CacheFile = filepath.Join(s.Mod.Root, ".lyth", "cache", "gotypes.json")
	}
	res, err := s.loader.Load(paths)
	if err != nil {
		return err
	}
	s.goPkgs = res
	return nil
}

func (s *Session) check() {
	for _, p := range s.Packages {
		if len(p.Syntax) == 0 {
			continue
		}
		for _, f := range p.Syntax {
			for _, imp := range f.Imports {
				if b := s.bridges[imp.Path]; b != nil {
					s.ensureBridge(b)
				}
			}
		}
		imp := func(path string) (*check.Package, string) {
			if b := s.bridges[path]; b != nil && b.types == nil {
				return nil, strings.Join(b.errors, "; ")
			}
			if dep := s.byPath[path]; dep != nil && dep.Dir.External && dep.Checked == nil {
				if gp := s.goPkgs[path]; gp == nil || gp.Types == nil {
					return nil, path + " " + dep.goView
				}
			}
			if dep := s.byPath[path]; dep != nil && dep.goView == "" {
				if dep.Checked == nil {
					return nil, "package " + path + " failed to compile"
				}
				return dep.Checked, ""
			}
			gp := s.goPkgs[path]
			if gp == nil {
				return nil, ""
			}
			if gp.Types == nil {
				return nil, strings.Join(gp.Errors, "; ")
			}
			return &check.Package{Path: path, Name: gp.Types.Name(), Go: gp.Types}, ""
		}
		sink := s.Sink
		if p.Dir.External {
			sink = p.depSink
		}
		conf := &check.Config{Sink: sink, Import: imp, DirName: filepath.Base(p.Dir.Dir), NoModule: s.Mod.Synthetic}
		if s.Mod.Synthetic {
			conf.DirName = "main"
		}
		protect(sink, p.span(), "in package "+p.Dir.ImportPath, func() { p.Checked = check.Check(conf, p.Dir.ImportPath, p.Syntax) })
		if p.Dir.External {
			s.settleExternal(p)
		}
	}
}

func (s *Session) emitOptions() emit.Options {
	return emit.Options{
		LineDirectives: !s.cfg.NoLineDirectives,
		AbsPaths:       !s.cfg.InTree,
		DenseLines:     !s.cfg.InTree && !s.cfg.SparseLineDirectives,
	}
}

func (s *Session) generate() error {
	opts := s.emitOptions()
	for _, p := range s.Packages {
		s.generatePkg(p, opts)
	}
	return nil
}

// generatePkg lowers and emits a checked package, once.
func (s *Session) generatePkg(p *Package, opts emit.Options) {
	if p.Checked == nil || p.generated || p.Dir.External {
		return
	}
	p.generated = true
	var lowered []*lower.File
	if protect(s.Sink, p.span(), "in package "+p.Dir.ImportPath, func() { lowered = lower.Package(p.Checked) }) != nil {
		return
	}
	for _, lf := range lowered {
		var res *emit.Result
		var err error
		if protect(s.Sink, source.Span{File: lf.Src}, "while generating Go code for "+lf.Src.Name, func() { res, err = emit.Emit(lf, opts) }) != nil {
			continue
		}
		if err != nil {
			s.Sink.Errorf(diag.EInternal, source.Span{File: lf.Src}, "internal compiler error while generating Go code: %v", err).
				WithNote("please report this with the source file")
			continue
		}
		p.Generated = append(p.Generated, res)
		p.Lowered = append(p.Lowered, lf)
	}
}

// protect converts a panic in a compiler stage into an internal-error
// diagnostic at span, so no input crashes the compiler.
func protect(sink *diag.Sink, span source.Span, where string, fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			d := sink.Errorf(diag.EInternal, span, "internal compiler error %s: %v", where, r)
			if at := PanicSite(); at != "" {
				d.WithNote("raised at %s", at)
			}
			d.WithNote("please report this at https://github.com/lyth-lang/lyth/issues with the source file")
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	fn()
	return nil
}

// PanicSite names the function and line that raised the panic being
// recovered, and the compiler function that called into it when the panic
// came from a library such as go/types. Called from a deferred function.
func PanicSite() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	site := ""
	for {
		fr, more := frames.Next()
		if fr.Function != "" && !strings.HasPrefix(fr.Function, "runtime.") {
			at := fmt.Sprintf("%s (%s:%d)", strings.TrimPrefix(fr.Function, "github.com/lyth-lang/lyth/internal/"), filepath.Base(fr.File), fr.Line)
			switch {
			case site == "":
				site = at
				if strings.HasPrefix(fr.Function, "github.com/lyth-lang/lyth/") {
					return site
				}
			case strings.HasPrefix(fr.Function, "github.com/lyth-lang/lyth/"):
				return site + ", called from " + at
			}
		}
		if !more {
			return site
		}
	}
}

// span is the package's first file, where package-wide diagnostics go.
func (p *Package) span() source.Span {
	if len(p.Files) > 0 {
		return source.Span{File: p.Files[0]}
	}
	return source.Span{}
}

// write stores generated files under .lyth/gen and writes the overlay.
func (s *Session) write() error {
	if s.Mod.Synthetic {
		for _, p := range s.Packages {
			for _, g := range p.Generated {
				path := filepath.Join(s.Mod.Root, g.Name)
				if p.Dir.Std {
					path = filepath.Join(p.Dir.Dir, g.Name)
				}
				if err := writeIfChanged(path, g.Code); err != nil {
					return err
				}
				s.Maps[path] = g.Map
			}
		}
		return cleanStale(s.Mod.Root, s.Maps, nil)
	}
	s.GenDir = filepath.Join(s.Mod.Root, ".lyth", "gen")
	if err := os.MkdirAll(s.GenDir, 0o755); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(s.Mod.Root, ".lyth", ".gitignore"), []byte("# Generated by lyth.\n*\n"), 0o644)
	replace := map[string]string{}
	written := map[string]*emit.SourceMap{}
	for _, p := range s.Packages {
		if p.Dir.External {
			continue // the go command builds its published Go
		}
		dir := filepath.Join(s.GenDir, filepath.FromSlash(p.Dir.Rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, g := range p.Generated {
			real := filepath.Join(dir, g.Name)
			if err := writeIfChanged(real, g.Code); err != nil {
				return err
			}
			mapData, _ := json.Marshal(g.Map)
			_ = writeIfChanged(real+".map", mapData)
			virtual := filepath.Join(p.Dir.Dir, g.Name)
			replace[virtual] = real
			s.Maps[virtual] = g.Map
			written[real] = g.Map
		}
		if !p.Dir.Std {
			hideStaleInTree(p.Dir.Dir, replace)
		}
	}
	shared := filepath.Join(s.Mod.Root, ".lyth", "overlay.json")
	s.Overlay = shared
	// Keep entries of packages this session did not compile (a targeted
	// build must not invalidate the rest of the module), as long as their
	// sources still exist.
	inSession := map[string]bool{}
	for _, p := range s.Packages {
		if !p.Dir.External {
			inSession[p.Dir.Dir] = true
		}
	}
	if data, err := os.ReadFile(s.Overlay); err == nil {
		var old struct{ Replace map[string]string }
		if json.Unmarshal(data, &old) == nil {
			for virtual, real := range old.Replace {
				dir := filepath.Dir(virtual)
				if inSession[dir] || !hasLythFiles(dir) {
					continue
				}
				if _, err := os.Stat(real); err == nil {
					replace[virtual] = real
					written[real] = nil
				}
			}
		}
	}
	// A file whose Lyth source exists stays even when this session did not
	// produce it: another lyth process (a test run while the editor builds)
	// may be using it.
	if err := cleanStale(s.GenDir, written, func(gen string) bool { return fileExists(s.genSource(gen)) }); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"Replace": replace}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeIfChanged(shared, data); err != nil {
		return err
	}
	// The go command this session runs reads an overlay of its own, named
	// by its content: a concurrent lyth process rewriting overlay.json must
	// not change what this build sees.
	sum := sha256.Sum256(data)
	s.Overlay = filepath.Join(s.Mod.Root, ".lyth", "overlay-"+hex.EncodeToString(sum[:6])+".json")
	removeOldOverlays(filepath.Join(s.Mod.Root, ".lyth"), s.Overlay)
	return writeIfChanged(s.Overlay, data)
}

// genSource returns the Lyth source of a file generated in .lyth/gen.
func (s *Session) genSource(gen string) string {
	rel, err := filepath.Rel(s.GenDir, gen)
	if err != nil {
		return ""
	}
	base := filepath.Base(rel)
	switch {
	case strings.HasSuffix(base, ".lyth_test.go"):
		base = strings.TrimSuffix(base, ".lyth_test.go") + "_test.lyth"
	case strings.HasSuffix(base, ".lyth.go"):
		base = strings.TrimSuffix(base, ".go")
	default:
		return ""
	}
	return filepath.Join(s.Mod.Root, filepath.Dir(rel), base)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// removeOldOverlays deletes the per-run overlay files of runs that ended a
// day ago or more (a fuzzing run may take hours).
func removeOldOverlays(dir, keep string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "overlay-") || !strings.HasSuffix(name, ".json") || filepath.Join(dir, name) == keep {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

// hideStaleInTree hides generated files that lyth gen -in-tree or an
// interrupted coverage run left beside sources that are gone (b.lyth.go
// after b.lyth was renamed): go build would compile them with the package.
// The overlay deletes them for the go command; the files themselves stay.
func hideStaleInTree(dir string, replace map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		var src string
		switch {
		case strings.HasSuffix(name, ".lyth_test.go"):
			src = strings.TrimSuffix(name, ".lyth_test.go") + "_test.lyth"
		case strings.HasSuffix(name, ".lyth.go"):
			src = strings.TrimSuffix(name, ".go")
		default:
			continue
		}
		path := filepath.Join(dir, name)
		if _, ok := replace[path]; ok || fileExists(filepath.Join(dir, src)) {
			continue
		}
		if data, err := os.ReadFile(path); err == nil && bytes.HasPrefix(data, []byte("// Code generated by lyth")) {
			replace[path] = ""
		}
	}
}

// hasLythFiles reports whether dir contains .lyth source files.
func hasLythFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".lyth") {
			return true
		}
	}
	return false
}

// writeIfChanged avoids touching files whose content is unchanged.
func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	return writeAtomic(path, data)
}

// writeAtomic writes data to a temporary file beside path and renames it
// into place, so a go command or lyth process reading path concurrently sees
// the old file or the new one, never a partial one (on Windows, only when no
// other process holds the file open). The temporary name ends in .tmp, which
// neither the go command nor cleanStale takes for a source.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	if busyOnWindows(err) {
		// Still held open by another process: write in place, which
		// Windows allows, rather than fail the build.
		return os.WriteFile(path, data, 0o644)
	}
	return err
}

// rename replaces to with from. On Windows a file another process has open
// cannot be replaced: as the go command does, it tries again for a while.
func rename(from, to string) error {
	err := os.Rename(from, to)
	for wait := time.Millisecond; err != nil && busyOnWindows(err) && wait <= time.Second; wait *= 2 {
		time.Sleep(wait)
		err = os.Rename(from, to)
	}
	return err
}

// busyOnWindows reports whether err is Windows refusing access to a file
// that another process holds: ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION.
func busyOnWindows(err error) bool {
	var errno syscall.Errno
	return runtime.GOOS == "windows" && errors.As(err, &errno) && (errno == 5 || errno == 32)
}

// cleanStale removes generated files that the current build did not
// produce, unless keepAlso says to keep one.
func cleanStale(dir string, keep map[string]*emit.SourceMap, keepAlso func(string) bool) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !(strings.HasSuffix(name, ".lyth.go") || strings.HasSuffix(name, ".lyth_test.go") ||
			strings.HasSuffix(name, ".lyth.go.map") || strings.HasSuffix(name, ".lyth_test.go.map")) {
			return nil
		}
		base := strings.TrimSuffix(path, ".map")
		if _, ok := keep[base]; !ok && (keepAlso == nil || !keepAlso(base)) {
			os.Remove(path)
		}
		return nil
	})
}

// TypesCached reports whether Go type information came from the persistent
// cache instead of `go list`.
func (s *Session) TypesCached() bool { return s.loader != nil && s.loader.CacheHit }

// GoFset returns the positions of the loaded Go packages' declarations, or
// nil before they are loaded.
func (s *Session) GoFset() *token.FileSet {
	if s.loader == nil {
		return nil
	}
	return s.loader.Fset
}

// WorkDir returns the directory lyth was invoked from, where go test runs.
func (s *Session) WorkDir() string { return s.cfg.WorkDir }

// MainPackages returns the target packages named main.
func (s *Session) MainPackages() []*Package {
	var out []*Package
	for _, p := range s.Targets {
		if p.Checked != nil && p.Checked.Name == "main" {
			out = append(out, p)
		}
	}
	return out
}
