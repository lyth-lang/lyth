package tests

import (
	"archive/zip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/build"
	"github.com/lyth-lang/lyth/internal/diag"
)

// libSource is a Lyth library using what Go cannot express: nullable
// results and fields, defaults, enums, sealed interfaces, extensions.
const libSource = `package users

enum Role { Admin, Member }

struct User {
    Name: string
    Email: string?
    Role: Role
}

sealed interface Event {
    Who(): string
}

struct Login : Event {
    Name: string
}

func Login.Who(): string = self.Name

func Find(id: int): User? = if id == 1 { User(Name = "Ada", Email = null, Role = Role.Admin) } else { null }

func Greet(name: string, greeting: string = "hello"): string = "${greeting} ${name}"

func Parse(s: string): int throws = s.toInt()

func First[T](xs: []T): T = xs[0]

func string.Shout(): string = self + "!"
`

// libApp uses the library through its Lyth declarations.
const libApp = `import "example.com/lib/users"

func describe(e: users.Event): string = when e {
    is users.Login -> "login " + e.Name
}

func main() throws {
    u := users.Find(1) ?: return
    email := u.Email ?: "none"
    kind := when u.Role {
        Admin -> "admin"
        Member -> "member"
    }
    println(u.Name, email, kind, users.Greet("Ada"), users.Parse("7")?, users.First([3, 4]), "hi".Shout())
    println(describe(users.Login(Name = "Bob")))
}
`

const libAppOutput = "Ada none admin hello Ada 7 3 hi!\nlogin Bob"

// TestLythLibraryFromOtherModule checks that Lyth code sees the Lyth
// declarations of a Lyth library in another module (ADR 0053): replaced by a
// local directory, it is compiled with the program; from the module cache, it
// is checked from its sources and built from the Go published with them,
// falling back to that Go, with W0014, when the sources do not compile or do
// not match it.
func TestLythLibraryFromOtherModule(t *testing.T) {
	ws := t.TempDir()
	if real, err := filepath.EvalSymlinks(ws); err == nil {
		ws = real
	}
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/go.mod", "module example.com/lib\n\ngo 1.26\n")
	write("lib/users/users.lyth", libSource)
	lib := compileModule(t, filepath.Join(ws, "lib"), true)
	if _, err := lib.WriteInTree(); err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(ws, "lib", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	published := read("users/users.lyth.go")

	t.Run("replace", func(t *testing.T) {
		write("local/go.mod", "module example.com/app\n\ngo 1.26\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n")
		write("local/main.lyth", libApp)
		before, _ := os.ReadDir(filepath.Join(ws, "lib", "users"))
		s := compileModule(t, filepath.Join(ws, "local"), false)
		if out := runApp(t, s); out != libAppOutput {
			t.Errorf("output = %q, want %q", out, libAppOutput)
		}
		if after, _ := os.ReadDir(filepath.Join(ws, "lib", "users")); len(after) != len(before) {
			t.Errorf("lyth wrote into the replaced module: %v", after)
		}
	})

	// A module proxy in a directory, and a module cache of the test's own.
	proxy, cache := filepath.Join(ws, "proxy"), filepath.Join(ws, "modcache")
	t.Cleanup(func() {
		cmd := exec.Command("go", "clean", "-modcache")
		cmd.Env = append(os.Environ(), "GOMODCACHE="+cache)
		_ = cmd.Run() // the module cache is read-only
	})
	versions := map[string]map[string][]byte{
		"v1.0.0": {"users/users.lyth": []byte(libSource), "users/users.lyth.go": published},
		"v1.1.0": {"users/users.lyth": []byte(libSource)},
		"v1.2.0": {"users/users.lyth": []byte(strings.Replace(libSource, `greeting: string = "hello"`, `greeting: string = "hello", end: string = ""`, 1)), "users/users.lyth.go": published},
		"v1.3.0": {"users/users.lyth": []byte(strings.Replace(libSource, "func Find(id: int)", "fun Find(id: Int)", 1)), "users/users.lyth.go": published},
		"v1.4.0": {"users/users.lyth": []byte(libSource), "users/users.lyth.go": published, "go.mod": []byte("module example.com/lib\n\ngo 1.26\n\n//lyth:version 99.0\n")},
	}
	for v, files := range versions {
		if files["go.mod"] == nil {
			files["go.mod"] = read("go.mod")
		}
		writeProxyModule(t, proxy, "example.com/lib", v, files)
	}
	proxyURL := filepath.ToSlash(proxy)
	if !strings.HasPrefix(proxyURL, "/") {
		proxyURL = "/" + proxyURL // file:///C:/... on Windows
	}
	env := append(os.Environ(), "GOMODCACHE="+cache, "GOPROXY=file://"+proxyURL, "GOSUMDB=off", "GOFLAGS=-mod=mod", "GOWORK=off")
	compileWith := func(t *testing.T, version string) *build.Session {
		t.Helper()
		dir := filepath.Join(ws, "app-"+version)
		write("app-"+version+"/go.mod", "module example.com/app\n\ngo 1.26\n\nrequire example.com/lib "+version+"\n")
		write("app-"+version+"/main.lyth", libApp)
		s, err := build.Compile(&build.Config{WorkDir: dir, Env: env, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	find := func(s *build.Session, code diag.Code) *diag.Diagnostic {
		for _, d := range s.Sink.All() {
			if d.Code == code {
				return d
			}
		}
		return nil
	}

	t.Run("module cache", func(t *testing.T) {
		s := compileWith(t, "v1.0.0")
		if len(s.Sink.All()) > 0 {
			t.Fatalf("diagnostics:\n%s", render(s.Sink.All()))
		}
		if out := runApp(t, s); out != libAppOutput {
			t.Errorf("output = %q, want %q", out, libAppOutput)
		}
	})
	t.Run("no published Go", func(t *testing.T) {
		d := find(compileWith(t, "v1.1.0"), diag.EImport)
		if d == nil || !strings.Contains(strings.Join(d.Notes, "\n"), "has .lyth sources but no Go") {
			t.Errorf("want E0103 naming the missing Go, got %v", d)
		}
	})
	t.Run("stale published Go", func(t *testing.T) {
		d := find(compileWith(t, "v1.2.0"), diag.WGoView)
		if d == nil || !strings.Contains(d.Message, "does not match its Lyth sources (Greet is") {
			t.Errorf("want W0014 naming Greet, got %v", d)
		}
	})
	t.Run("sources this lyth rejects", func(t *testing.T) {
		s := compileWith(t, "v1.3.0")
		d := find(s, diag.WGoView)
		if d == nil || !strings.Contains(d.Message, "do not compile with this lyth (users.lyth:") {
			t.Errorf("want W0014 naming the first error, got %v", d)
		}
		for _, d := range s.Sink.All() {
			if d.Span.File != nil && strings.Contains(d.Span.File.Path, "modcache") {
				t.Errorf("a diagnostic of the dependency reached the program: %s", d.Message)
			}
		}
	})
	t.Run("written for a newer Lyth", func(t *testing.T) {
		s := compileWith(t, "v1.4.0")
		d := find(s, diag.WGoView)
		if d == nil || !strings.Contains(d.Message, "it is written for Lyth 99.0, newer than this lyth ("+build.LangVersion+")") {
			t.Errorf("want W0014 naming the version, got %v", d)
		}
	})
}

// TestLythVersionDirective checks the //lyth:version line of go.mod (ADR
// 0054) for the modules compiled with the program: a version newer than this
// lyth, or a malformed line, is E0602 at the line; the go command keeps it.
func TestLythVersionDirective(t *testing.T) {
	main := "func main() {\n    println(\"hi\")\n}\n"
	compile := func(t *testing.T, root string) *build.Session {
		t.Helper()
		s, err := build.Compile(&build.Config{WorkDir: root, NoWrite: true, Stdout: os.Stdout, Stderr: os.Stderr}, build.Target{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct{ line, want string }{
		{"//lyth:version " + build.LangVersion, ""},
		{"//lyth:version 0.1", ""},
		{"//lyth:version 99.0", "go.mod:5:1: error[E0602]: the module is written for Lyth 99.0, newer than this lyth (" + build.LangVersion + ")"},
		{"//lyth:version 1", "go.mod:5:1: error[E0602]: go.mod has a malformed //lyth:version line"},
		{"//lyth:version 0.1\n//lyth:version 0.2", "go.mod:6:1: error[E0602]: go.mod has a second //lyth:version line (the first is line 5)"},
	} {
		root := writeModule(t, "example.com/app", map[string]string{"main.lyth": main})
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n\n"+tc.line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := ""
		if ds := compile(t, root).Sink.All(); len(ds) > 0 {
			got = render(ds)
		}
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%q: got\n%s\nwant %q", tc.line, got, tc.want)
		}
	}

	// A module replaced by a directory is compiled with the program, so its
	// version is checked as the program's is.
	ws := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/go.mod", "module example.com/lib\n\ngo 1.26\n\n//lyth:version 99.0\n")
	write("lib/users/users.lyth", libSource)
	write("app/go.mod", "module example.com/app\n\ngo 1.26\n\n//lyth:version "+build.LangVersion+"\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n")
	write("app/main.lyth", main)
	if got := render(compile(t, filepath.Join(ws, "app")).Sink.All()); !strings.Contains(got, filepath.Join("lib", "go.mod")+":5:1: error[E0602]: the module is written for Lyth 99.0") {
		t.Errorf("replaced module: got\n%s", got)
	}

	// The go command keeps the line when it edits go.mod.
	cmd := exec.Command("go", "mod", "edit", "-go=1.27", "-require=example.com/other@v1.0.0")
	cmd.Dir = filepath.Join(ws, "app")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit: %v: %s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(ws, "app", "go.mod"))
	if v, _, err := build.LythDirective(string(data)); err != nil || v != build.LangVersion {
		t.Errorf("after go mod edit, go.mod declares %q (%v):\n%s", v, err, data)
	}
}

// runApp builds the session's module and returns what the program prints.
func runApp(t *testing.T, s *build.Session) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "app.bin")
	if ok, err := s.GoBuild(bin, []string{"example.com/app"}, nil); err != nil || !ok {
		t.Fatalf("build failed: %v\n%s", err, render(s.Sink.All()))
	}
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeProxyModule writes one version of a module to a GOPROXY directory.
func writeProxyModule(t *testing.T, proxy, mod, version string, files map[string][]byte) {
	t.Helper()
	dir := filepath.Join(proxy, filepath.FromSlash(mod), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, _ := json.Marshal(map[string]string{"Version": version, "Time": "2026-10-01T00:00:00Z"})
	if err := os.WriteFile(filepath.Join(dir, version+".info"), info, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, version+".mod"), files["go.mod"], 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	for name, data := range files {
		w, err := z.Create(mod + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
}
