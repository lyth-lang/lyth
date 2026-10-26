package build

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
	"github.com/lyth-lang/lyth/internal/syntax"
)

// GenerateOptions are the flags of lyth generate, those of go generate.
type GenerateOptions struct {
	Run, Skip *regexp.Regexp // which directives run, by their text
	DryRun    bool           // -n: print the commands, run nothing
	Verbose   bool           // -v: print the packages and files processed
	Trace     bool           // -x: print the commands as they run
}

// Generate runs the //go:generate directives of the .lyth files (test
// files included) of the packages that patterns select, as go generate does
// for .go files: each in its package directory, with $GOFILE, $GOLINE,
// $GOPACKAGE, $GOOS, $GOARCH, $GOROOT, and $DOLLAR set and expanded, and
// //go:generate -command aliases for the rest of the file. go generate
// cannot run them: it reads only .go files on disk. A failing command stops
// its package; the others still run.
func Generate(cfg *Config, patterns []string, opts GenerateOptions) error {
	wd, err := canonical(cfg.WorkDir)
	if err != nil {
		return err
	}
	mod, err := FindModule(wd)
	if err != nil {
		if err == errNoModule {
			return fmt.Errorf("no go.mod found in %s or any parent directory", wd)
		}
		return err
	}
	all, err := discover(mod)
	if err != nil {
		return err
	}
	pkgs, err := selectPackages(all, wd, patterns)
	if err != nil {
		return err
	}
	env, err := goEnvFor(mod.Root, cfg.Env)
	if err != nil {
		return err
	}
	failed := 0
	for _, p := range pkgs {
		if opts.Verbose {
			fmt.Fprintln(cfg.Stderr, displayName(p.Dir, wd))
		}
		if err := generatePackage(cfg, p, env, opts, wd); err != nil {
			fmt.Fprintf(cfg.Stderr, "%v\n", err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d %s failed", failed, plural(failed, "package"))
	}
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// selectPackages picks the packages that patterns name (all when none).
func selectPackages(all []*pkgDir, wd string, patterns []string) ([]*pkgDir, error) {
	if len(patterns) == 0 {
		patterns = []string{"."}
	}
	var out []*pkgDir
	seen := map[*pkgDir]bool{}
	for _, pat := range patterns {
		recursive := pat == "./..." || pat == "..." || strings.HasSuffix(pat, "/...")
		dir := strings.TrimSuffix(strings.TrimSuffix(pat, "..."), "/")
		if dir == "" || dir == "." {
			dir = wd
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(wd, dir)
		}
		dir = filepath.Clean(dir)
		found := false
		for _, p := range all {
			if p.Dir == dir || recursive && strings.HasPrefix(p.Dir, dir+string(filepath.Separator)) {
				found = true
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("no Lyth package in %s", pat)
		}
	}
	return out, nil
}

// goEnvFor returns the go env variables generators see.
func goEnvFor(dir string, env []string) (map[string]string, error) {
	cmd := exec.Command("go", "env", "GOROOT", "GOOS", "GOARCH")
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("the go command is required: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return nil, fmt.Errorf("unexpected go env output %q", out)
	}
	return map[string]string{"GOROOT": lines[0], "GOOS": lines[1], "GOARCH": lines[2]}, nil
}

func generatePackage(cfg *Config, p *pkgDir, env map[string]string, opts GenerateOptions, wd string) error {
	files := append(append([]string(nil), p.Files...), p.TestFiles...)
	name := lythPackageName(p)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(data, []byte("//go:generate")) {
			continue
		}
		if opts.Verbose {
			fmt.Fprintln(cfg.Stderr, displayName(path, wd))
		}
		g := &generator{cfg: cfg, dir: p.Dir, file: filepath.Base(path), pkg: name, goEnv: env, commands: map[string][]string{}, opts: opts}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for line := 1; sc.Scan(); line++ {
			text := strings.TrimSuffix(sc.Text(), "\r")
			if !strings.HasPrefix(text, "//go:generate ") && !strings.HasPrefix(text, "//go:generate\t") {
				continue
			}
			if opts.Run != nil && !opts.Run.MatchString(text) || opts.Skip != nil && opts.Skip.MatchString(text) {
				continue
			}
			g.line = line
			if err := g.run(text); err != nil {
				return fmt.Errorf("%s:%d: %v", displayName(path, wd), line, err)
			}
		}
	}
	return nil
}

// lythPackageName is the package name the compiler gives p: its package
// clause, main when a file declares func main, or else the directory name.
func lythPackageName(p *pkgDir) string {
	hasMain := false
	for _, path := range p.Files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f := syntax.Parse(source.NewFile(path, path, data), &diag.Sink{})
		if f.Package != nil {
			return f.Package.Name
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*syntax.FunDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
				hasMain = true
			}
		}
	}
	if hasMain {
		return "main"
	}
	return strings.ReplaceAll(filepath.Base(p.Dir), "-", "_")
}

type generator struct {
	cfg      *Config
	dir      string
	file     string
	pkg      string
	line     int
	goEnv    map[string]string
	commands map[string][]string // -command aliases of the file
	opts     GenerateOptions
}

func (g *generator) env() []string {
	vars := []string{
		"GOROOT=" + g.goEnv["GOROOT"],
		"GOARCH=" + g.goEnv["GOARCH"],
		"GOOS=" + g.goEnv["GOOS"],
		"GOFILE=" + g.file,
		"GOLINE=" + strconv.Itoa(g.line),
		"GOPACKAGE=" + g.pkg,
		"DOLLAR=$",
		"PWD=" + g.dir,
	}
	path := filepath.Join(g.goEnv["GOROOT"], "bin")
	if old := os.Getenv("PATH"); old != "" {
		path += string(os.PathListSeparator) + old
	}
	return append(vars, "PATH="+path)
}

func (g *generator) expand(name string) string {
	for _, e := range g.env() {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return os.Getenv(name)
}

// run executes one directive line.
func (g *generator) run(line string) error {
	words, err := splitGenerate(strings.TrimLeft(strings.TrimPrefix(line, "//go:generate"), " \t"))
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return fmt.Errorf("no arguments to directive")
	}
	if words[0] == "-command" {
		if len(words) < 3 {
			return fmt.Errorf("no command specified for -command")
		}
		if g.commands[words[1]] != nil {
			return fmt.Errorf("command %q multiply defined", words[1])
		}
		g.commands[words[1]] = words[2:]
		return nil
	}
	if alias := g.commands[words[0]]; alias != nil {
		words = append(append([]string(nil), alias...), words[1:]...)
	}
	for i, w := range words {
		words[i] = os.Expand(w, g.expand)
	}
	if g.opts.DryRun || g.opts.Trace {
		fmt.Fprintln(g.cfg.Stderr, strings.Join(words, " "))
	}
	if g.opts.DryRun {
		return nil
	}
	path := words[0]
	if !strings.ContainsRune(path, os.PathSeparator) {
		// //go:generate go run ...: the go of the toolchain, as go generate does.
		if p, err := exec.LookPath(filepath.Join(g.goEnv["GOROOT"], "bin", path)); err == nil {
			path = p
		}
	}
	cmd := exec.Command(path, words[1:]...)
	cmd.Args[0] = words[0]
	cmd.Dir = g.dir
	cmd.Stdout, cmd.Stderr = g.cfg.Stdout, g.cfg.Stderr
	cmd.Env = append(os.Environ(), g.env()...)
	if g.cfg.Env != nil {
		cmd.Env = append(append([]string(nil), g.cfg.Env...), g.env()...)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q: %v", words[0], err)
	}
	return nil
}

// splitGenerate splits a directive into words as go generate does:
// separated by spaces and tabs, with Go-quoted strings as single words.
func splitGenerate(line string) ([]string, error) {
	var words []string
	for {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			return words, nil
		}
		if line[0] == '"' {
			end := -1
			for i := 1; i < len(line); i++ {
				if line[i] == '\\' {
					i++
					continue
				}
				if line[i] == '"' {
					end = i
					break
				}
			}
			if end < 0 {
				return nil, fmt.Errorf("mismatched quoted string")
			}
			word, err := strconv.Unquote(line[:end+1])
			if err != nil {
				return nil, fmt.Errorf("bad quoted string")
			}
			words = append(words, word)
			line = line[end+1:]
			if line != "" && line[0] != ' ' && line[0] != '\t' {
				return nil, fmt.Errorf("expect space after quoted argument")
			}
			continue
		}
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			i = len(line)
		}
		words = append(words, line[:i])
		line = line[i:]
	}
}
