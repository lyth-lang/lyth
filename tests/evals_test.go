package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/build"
)

// TestEvalSolutions builds and runs the reference solution of every task in
// evals/tasks (see evals/README.md): it must print the task's expected.out,
// and pass as many tests as the task asks for. A task's starter, when it has
// one, must not compile: the task is to fix or upgrade it.
func TestEvalSolutions(t *testing.T) {
	tasks, _ := filepath.Glob("../evals/tasks/*/solution")
	if len(tasks) == 0 {
		t.Fatal("no eval tasks")
	}
	for _, solution := range tasks {
		dir := filepath.Dir(solution)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			want, err := os.ReadFile(filepath.Join(dir, "expected.out"))
			if err != nil {
				t.Fatal(err)
			}
			spec, _ := os.ReadFile(filepath.Join(dir, "task.toml"))
			root := copyTaskModule(t, solution)
			var out bytes.Buffer
			s, err := build.Compile(&build.Config{WorkDir: root, Tests: true, Stdout: &out, Stderr: &out}, build.Target{})
			if err != nil || s.Sink.HasErrors() {
				t.Fatalf("compilation failed: %v\n%s", err, render(s.Sink.All()))
			}
			if code, err := s.GoRun("app", nil, nil); err != nil || code != 0 {
				t.Fatalf("exit %d, %v:\n%s", code, err, out.String())
			}
			if strings.TrimSpace(out.String()) != strings.TrimSpace(string(want)) {
				t.Errorf("output differs from expected.out\n%s", diffLines(string(want), out.String()))
			}
			if m := regexp.MustCompile(`(?m)^tests\s*=\s*(\d+)`).FindSubmatch(spec); m != nil {
				n, _ := strconv.Atoi(string(m[1]))
				code, output := testModule(t, root, "-v")
				if passed := strings.Count(output, "--- PASS:"); code != 0 || passed < n {
					t.Errorf("lyth test: exit %d, %d tests passed, want %d:\n%s", code, passed, n, output)
				}
			}
			if starter := filepath.Join(dir, "starter"); exists(starter) {
				root := copyTaskModule(t, starter)
				s, _ := build.Compile(&build.Config{WorkDir: root, Tests: true, Stdout: &out, Stderr: &out}, build.Target{})
				if s == nil || !s.Sink.HasErrors() {
					t.Error("the starter compiles; the task would be done before it starts")
				}
			}
		})
	}
}

// copyTaskModule copies the .lyth files of a task directory into a new
// module whose path is app, as the eval harness's lyth init app makes.
func copyTaskModule(t *testing.T, from string) string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".lyth") {
			return err
		}
		data, err := os.ReadFile(path)
		rel, _ := filepath.Rel(from, path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeModule(t, "app", files)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
