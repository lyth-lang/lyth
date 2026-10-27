package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunFileInModule checks that lyth run file.lyth inside a module builds
// that file alone, as go run file.go does, while lyth run . builds the whole
// package and finds both mains.
func TestRunFileInModule(t *testing.T) {
	if testing.Short() {
		t.Skip("builds programs")
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":       "module tools\n\ngo 1.26.0\n",
		"hello.lyth":   "import \"fmt\"\n\nfunc main() {\n    fmt.Println(\"hello\", greeting)\n}\n\nconst greeting = \"from hello\"\n",
		"goodbye.lyth": "import \"fmt\"\n\nfunc main() {\n    fmt.Println(\"goodbye\")\n}\n",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	for _, tc := range []struct{ file, want string }{
		{"hello.lyth", "hello from hello\n"},
		{"goodbye.lyth", "goodbye\n"},
	} {
		var out, errOut bytes.Buffer
		if code := run([]string{"run", tc.file}, &out, &errOut); code != 0 {
			t.Fatalf("lyth run %s: exit %d\n%s", tc.file, code, errOut.String())
		}
		if out.String() != tc.want {
			t.Errorf("lyth run %s printed %q, want %q", tc.file, out.String(), tc.want)
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"run", "."}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "main") {
		t.Errorf("lyth run . with two mains: exit %d, %q", code, errOut.String())
	}
}
