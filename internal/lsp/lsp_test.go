package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// client drives a server over pipes, as an editor does.
type client struct {
	t       *testing.T
	w       io.Writer
	mu      sync.Mutex
	nextID  int
	pending map[int]chan *message
	diags   map[string][]Diagnostic
	changed chan struct{}
}

func newClient(t *testing.T) *client {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	c := &client{t: t, w: cw, pending: map[int]chan *message{}, diags: map[string][]Diagnostic{}, changed: make(chan struct{}, 100)}
	done := make(chan struct{})
	go func() {
		_ = Run(sr, sw, Options{Version: "test", Debounce: 10 * time.Millisecond})
		sw.Close()
		close(done)
	}()
	go func() {
		r := bufio.NewReader(cr)
		for {
			m, err := readMessage(r)
			if err != nil {
				return
			}
			if m.Method == "textDocument/publishDiagnostics" {
				var p struct {
					URI         string       `json:"uri"`
					Diagnostics []Diagnostic `json:"diagnostics"`
				}
				_ = json.Unmarshal(m.Params, &p)
				c.mu.Lock()
				c.diags[p.URI] = p.Diagnostics
				c.mu.Unlock()
				c.changed <- struct{}{}
				continue
			}
			if m.Method != "" {
				continue
			}
			var id int
			_ = json.Unmarshal(m.ID, &id)
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	t.Cleanup(func() {
		c.notify("exit", nil)
		cw.Close()
		<-done
	})
	return c
}

func (c *client) notify(method string, params any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = writeMessage(c.w, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends a request and decodes its result into out.
func (c *client) call(method string, params, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *message, 1)
	c.pending[id] = ch
	err := writeMessage(c.w, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	c.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-time.After(60 * time.Second):
		return fmt.Errorf("%s: no response", method)
	}
}

func (c *client) mustCall(method string, params, out any) {
	c.t.Helper()
	if err := c.call(method, params, out); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

// waitDiags waits until the diagnostics published for uri satisfy ok.
func (c *client) waitDiags(uri string, ok func([]Diagnostic) bool) []Diagnostic {
	c.t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		c.mu.Lock()
		d, have := c.diags[uri]
		c.mu.Unlock()
		if have && ok(d) {
			return d
		}
		select {
		case <-c.changed:
		case <-deadline:
			c.t.Fatalf("diagnostics for %s: got %v", uri, d)
		}
	}
}

const libSrc = `// Greet returns a friendly message.
func Greet(name: string): string = "hi ${name}"

struct User {
    ID: int64
    Name: string
}

func User.Label(): string = "${ID}:${Name}"

func Zero[T](): T {
    z: T
    return z
}

enum Color { Red, Green }
`

const mainSrc = `import "fmt"
import "strings"
import "example.com/app/lib"

func double(n: int) = n * 2

func main() {
    u := lib.User(ID = 1, Name = "ada")
    total := double(21)
    for i, w in strings.Fields("a b") {
        fmt.Println(i, w)
    }
    fmt.Println(lib.Greet(u.Name), u.Label(), total, lib.Zero[int]())
}
`

// workspace writes a module with a library package and a main package.
func workspace(t *testing.T) (root, mainPath, libPath string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.26\n",
		"lib/lib.lyth": libSrc,
		"main.lyth":    mainSrc,
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, filepath.Join(root, "main.lyth"), filepath.Join(root, "lib", "lib.lyth")
}

// at returns the position of the n-th (0-based) occurrence of needle in
// text, moved delta bytes into it.
func at(t *testing.T, text, needle string, n, delta int) Position {
	t.Helper()
	off := -1
	for i := 0; i <= n; i++ {
		j := strings.Index(text[off+1:], needle)
		if j < 0 {
			t.Fatalf("%q occurs fewer than %d times", needle, n+1)
		}
		off += 1 + j
	}
	return newLineIndex([]byte(text)).position(off + delta)
}

func tdp(uri string, p Position) map[string]any {
	return map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}
}

func open(c *client, uri, text string) {
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "lyth", "version": 1, "text": text}})
}

func TestServer(t *testing.T) {
	_, mainPath, libPath := workspace(t)
	mainURI, libURI := pathToURI(mainPath), pathToURI(libPath)
	c := newClient(t)

	var init struct {
		Capabilities map[string]any `json:"capabilities"`
		ServerInfo   struct{ Name string }
	}
	c.mustCall("initialize", map[string]any{"processId": nil, "rootUri": nil, "capabilities": map[string]any{}, "initializationOptions": map[string]any{"vet": false}}, &init)
	if init.ServerInfo.Name != "lyth" || init.Capabilities["hoverProvider"] != true {
		t.Fatalf("initialize: %+v", init)
	}
	c.notify("initialized", map[string]any{})
	open(c, mainURI, mainSrc)
	c.waitDiags(mainURI, func(d []Diagnostic) bool { return len(d) == 0 })

	hover := func(uri, text, needle string, n, delta int) string {
		t.Helper()
		var h struct {
			Contents struct{ Value string }
		}
		c.mustCall("textDocument/hover", tdp(uri, at(t, text, needle, n, delta)), &h)
		return h.Contents.Value
	}
	t.Run("hover", func(t *testing.T) {
		got := hover(mainURI, mainSrc, "double(21)", 0, 1)
		for _, want := range []string{"func double(n: int): int", "Compiles to", "func double(n int) int"} {
			if !strings.Contains(got, want) {
				t.Errorf("hover on double: missing %q in\n%s", want, got)
			}
		}
		if got := hover(mainURI, mainSrc, "u.Name", 0, 0); !strings.Contains(got, "u: User") || !strings.Contains(got, "local variable") {
			t.Errorf("hover on u:\n%s", got)
		}
		if got := hover(mainURI, mainSrc, "Greet", 0, 1); !strings.Contains(got, "func Greet(name: string): string") || !strings.Contains(got, "Greet returns a friendly message.") {
			t.Errorf("hover on lib.Greet:\n%s", got)
		}
		if got := hover(mainURI, mainSrc, "Println", 0, 1); !strings.Contains(got, "func fmt.Println(a ...any) (n int, err error)") || !strings.Contains(got, "Println formats") {
			t.Errorf("hover on fmt.Println (Go declaration and its doc):\n%s", got)
		}
		if got := hover(mainURI, mainSrc, "Name =", 0, 1); !strings.Contains(got, "Name: string") || !strings.Contains(got, `json:"name"`) {
			t.Errorf("hover on the named argument Name:\n%s", got)
		}
	})

	definition := func(uri, text, needle string, n, delta int, method string) []Location {
		t.Helper()
		var locs []Location
		c.mustCall(method, tdp(uri, at(t, text, needle, n, delta)), &locs)
		return locs
	}
	t.Run("definition", func(t *testing.T) {
		locs := definition(mainURI, mainSrc, "Greet", 0, 0, "textDocument/definition")
		if len(locs) != 1 || locs[0].URI != libURI || locs[0].Range.Start != at(t, libSrc, "Greet(", 0, 0) {
			t.Errorf("definition of lib.Greet: %+v", locs)
		}
		locs = definition(mainURI, mainSrc, "double(21)", 0, 0, "textDocument/definition")
		if len(locs) != 1 || locs[0].Range.Start != at(t, mainSrc, "double", 0, 0) {
			t.Errorf("definition of double: %+v", locs)
		}
		locs = definition(mainURI, mainSrc, "Println", 0, 0, "textDocument/definition")
		if len(locs) != 1 || !strings.HasSuffix(locs[0].URI, "/fmt/print.go") {
			t.Errorf("definition of fmt.Println: %+v", locs)
		}
		locs = definition(mainURI, mainSrc, "u.Label", 0, 0, "textDocument/typeDefinition")
		if len(locs) != 1 || locs[0].URI != libURI || locs[0].Range.Start != at(t, libSrc, "User {", 0, 0) {
			t.Errorf("type definition of u: %+v", locs)
		}
	})

	t.Run("references", func(t *testing.T) {
		var locs []Location
		c.mustCall("textDocument/references", map[string]any{"textDocument": map[string]any{"uri": mainURI},
			"position": at(t, mainSrc, "u.Name", 0, 2), "context": map[string]any{"includeDeclaration": true}}, &locs)
		var got []string
		for _, l := range locs {
			got = append(got, fmt.Sprintf("%s:%d", filepath.Base(uriToPath(l.URI)), l.Range.Start.Line))
		}
		// The field, the template in User.Label, the named argument, and u.Name.
		want := "lib.lyth:5 lib.lyth:8 main.lyth:7 main.lyth:12"
		if strings.Join(got, " ") != want {
			t.Errorf("references of Name: %v, want %s", got, want)
		}
	})

	t.Run("rename", func(t *testing.T) {
		var edit struct {
			Changes map[string][]TextEdit `json:"changes"`
		}
		c.mustCall("textDocument/rename", map[string]any{"textDocument": map[string]any{"uri": mainURI},
			"position": at(t, mainSrc, "double(21)", 0, 0), "newName": "twice"}, &edit)
		if len(edit.Changes) != 1 || len(edit.Changes[mainURI]) != 2 {
			t.Errorf("rename double: %+v", edit.Changes)
		}
		if err := c.call("textDocument/prepareRename", tdp(mainURI, at(t, mainSrc, "Println", 0, 0)), nil); err == nil || !strings.Contains(err.Error(), "declared in Go") {
			t.Errorf("prepareRename of fmt.Println: %v", err)
		}
		if err := c.call("textDocument/rename", map[string]any{"textDocument": map[string]any{"uri": mainURI},
			"position": at(t, mainSrc, "total", 0, 0), "newName": "when"}, nil); err == nil {
			t.Error("renaming to a keyword succeeded")
		}
	})

	t.Run("completion", func(t *testing.T) {
		labels := func(text string, p Position) map[string]bool {
			t.Helper()
			c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": mainURI, "version": 2}, "contentChanges": []map[string]any{{"text": text}}})
			var r struct {
				Items []completionItem `json:"items"`
			}
			c.mustCall("textDocument/completion", tdp(mainURI, p), &r)
			out := map[string]bool{}
			for _, it := range r.Items {
				out[it.Label] = true
			}
			return out
		}
		edited := strings.Replace(mainSrc, "    total := double(21)\n", "    total := double(21)\n    u.\n", 1)
		got := labels(edited, at(t, edited, "u.\n", 0, 2))
		for _, want := range []string{"ID", "Name", "Label"} {
			if !got[want] {
				t.Errorf("completion after u.: missing %s in %v", want, got)
			}
		}
		edited = strings.Replace(mainSrc, "    total := double(21)\n", "    total := double(21)\n    lib.Gr\n", 1)
		got = labels(edited, at(t, edited, "lib.Gr\n", 0, 6))
		for _, want := range []string{"Greet", "User", "Zero", "Color"} {
			if !got[want] {
				t.Errorf("completion after lib.: missing %s", want)
			}
		}
		edited = strings.Replace(mainSrc, "    total := double(21)\n", "    total := double(21)\n    strings.\n", 1)
		if got := labels(edited, at(t, edited, "strings.\n", 0, 8)); !got["Fields"] || !got["ToUpper"] {
			t.Errorf("completion after strings.: %d items", len(got))
		}
		edited = strings.Replace(mainSrc, "    total := double(21)\n", "    total := double(21)\n    to\n", 1)
		got = labels(edited, at(t, edited, "    to\n", 0, 6))
		for _, want := range []string{"total", "u", "double", "fmt", "lib", "len", "string"} {
			if !got[want] {
				t.Errorf("completion of a name: missing %s", want)
			}
		}
		if got["i"] {
			t.Error("completion offers the loop variable i outside its loop")
		}
		c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": mainURI, "version": 3}, "contentChanges": []map[string]any{{"text": mainSrc}}})
	})

	t.Run("signature help", func(t *testing.T) {
		edited := strings.Replace(mainSrc, "    total := double(21)\n", "    total := double(21)\n    lib.Greet(\n", 1)
		c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": mainURI, "version": 4}, "contentChanges": []map[string]any{{"text": edited}}})
		var h struct {
			Signatures []struct {
				Label         string
				Documentation struct{ Value string }
			}
			ActiveParameter int
		}
		c.mustCall("textDocument/signatureHelp", tdp(mainURI, at(t, edited, "lib.Greet(\n", 0, 10)), &h)
		if len(h.Signatures) != 1 || h.Signatures[0].Label != "Greet(name: string): string" || h.ActiveParameter != 0 {
			t.Errorf("signature help: %+v", h)
		}
		c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": mainURI, "version": 5}, "contentChanges": []map[string]any{{"text": mainSrc}}})
	})

	t.Run("inlay hints", func(t *testing.T) {
		var hints []struct {
			Position Position
			Label    string
		}
		c.mustCall("textDocument/inlayHint", map[string]any{"textDocument": map[string]any{"uri": mainURI},
			"range": Range{End: Position{Line: 100}}}, &hints)
		var got []string
		for _, h := range hints {
			got = append(got, fmt.Sprintf("%d:%d%s", h.Position.Line, h.Position.Character, h.Label))
		}
		// double's result, total, and the loop variables; u is a construction.
		want := "4:19: int 8:9: int 9:9: int 9:12: string"
		if strings.Join(got, " ") != want {
			t.Errorf("inlay hints: %v, want %s", got, want)
		}
	})

	t.Run("symbols", func(t *testing.T) {
		var syms []documentSymbol
		c.mustCall("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": libURI}}, &syms)
		var got []string
		for _, s := range syms {
			var kids []string
			for _, k := range s.Children {
				kids = append(kids, k.Name)
			}
			got = append(got, s.Name+"("+strings.Join(kids, ",")+")")
		}
		if want := "Greet() User(ID,Name,Label) Zero() Color(Red,Green)"; strings.Join(got, " ") != want {
			t.Errorf("document symbols: %v, want %s", got, want)
		}
		var ws []struct{ Name string }
		c.mustCall("workspace/symbol", map[string]any{"query": "gret"}, &ws)
		if len(ws) != 1 || ws[0].Name != "Greet" {
			t.Errorf("workspace symbols for gret: %+v", ws)
		}
	})

	t.Run("generated Go", func(t *testing.T) {
		var goCode string
		c.mustCall("workspace/executeCommand", map[string]any{"command": "lyth.showGo", "arguments": []string{mainURI}}, &goCode)
		if !strings.Contains(goCode, "package main") || !strings.Contains(goCode, "func double(n int) int") {
			t.Errorf("generated Go:\n%s", goCode)
		}
	})

	t.Run("diagnostics and quick fixes", func(t *testing.T) {
		bad := strings.Replace(mainSrc, `lib.User(ID = 1, Name = "ada")`, `lib.User{ID: 1}`, 1)
		c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": mainURI, "version": 6}, "contentChanges": []map[string]any{{"text": bad}}})
		d := c.waitDiags(mainURI, func(d []Diagnostic) bool { return len(d) > 0 })
		if d[0].Code != "E0008" || d[0].Data == nil {
			t.Fatalf("diagnostics: %+v", d)
		}
		var actions []struct {
			Title string
			Edit  struct {
				Changes map[string][]TextEdit
			}
		}
		c.mustCall("textDocument/codeAction", map[string]any{"textDocument": map[string]any{"uri": mainURI}, "range": d[0].Range,
			"context": map[string]any{"diagnostics": []Diagnostic{d[0]}}}, &actions)
		if len(actions) != 1 || actions[0].Edit.Changes[mainURI][0].NewText != "lib.User(ID = 1)" {
			t.Errorf("code actions: %+v", actions)
		}
		c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": mainURI, "version": 7}, "contentChanges": []map[string]any{{"text": mainSrc}}})
		c.waitDiags(mainURI, func(d []Diagnostic) bool { return len(d) == 0 })
	})
}

// TestVetOnSave checks that go vet's findings are published on the .lyth
// lines when a file is saved.
func TestVetOnSave(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := "import \"fmt\"\n\nfunc main() {\n    fmt.Printf(\"%d\\n\", \"text\")\n}\n"
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/vet\n\ngo 1.26\n"), 0o644)
	path := filepath.Join(root, "main.lyth")
	_ = os.WriteFile(path, []byte(src), 0o644)
	c := newClient(t)
	c.mustCall("initialize", map[string]any{"capabilities": map[string]any{}, "initializationOptions": map[string]any{"vet": true}}, nil)
	open(c, pathToURI(path), src)
	d := c.waitDiags(pathToURI(path), func(d []Diagnostic) bool { return len(d) > 0 })
	if d[0].Source != "vet" || d[0].Range.Start.Line != 3 || !strings.Contains(d[0].Message, "%d") {
		t.Errorf("vet diagnostics: %+v", d)
	}
}

// TestEditorPaths checks that locations sent back use the editor's spelling
// of a path reached through a symlink, as macOS's /var is /private/var.
func TestEditorPaths(t *testing.T) {
	s := &Server{aliases: [][2]string{{"/private", "/"}, {"/real/dir", "/link"}}}
	for path, want := range map[string]string{
		"/private/var/m/main.lyth": "file:///var/m/main.lyth",
		"/real/dir/a.lyth":         "file:///link/a.lyth",
		"/other/b.lyth":            "file:///other/b.lyth",
	} {
		if got := s.uri(path); got != want {
			t.Errorf("uri(%s) = %s, want %s", path, got, want)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real", "pkg")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Skip(err)
	}
	_ = os.WriteFile(filepath.Join(real, "main.lyth"), []byte("func main() {}\n"), 0o644)
	s = &Server{}
	editor := filepath.Join(link, "pkg", "main.lyth")
	if got := s.open(pathToURI(editor)); got != filepath.Join(real, "main.lyth") {
		t.Errorf("open: canonical path %s", got)
	}
	if got := s.uri(filepath.Join(real, "main.lyth")); got != pathToURI(editor) {
		t.Errorf("uri of the opened file: %s, want %s", got, pathToURI(editor))
	}
}
