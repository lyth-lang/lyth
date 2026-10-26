package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/build"
)

// docOutputs records what each documented program prints. The website shows
// it in the examples' Output tabs (website/scripts/sync-docs.mjs).
const docOutputs = "../website/src/data/doc-outputs.json"

type docOutput struct {
	File   string `json:"file"`
	Source string `json:"source"`
	Output string `json:"output"`
}

// docSources lists the Markdown files the website is generated from.
func docSources(t *testing.T) []string {
	var files []string
	for _, dir := range []string{"guide", "spec", "design", "adr"} {
		matches, _ := filepath.Glob(filepath.Join("..", "docs", dir, "*.md"))
		files = append(files, matches...)
	}
	return append(files, "../README.md", "../examples/todoapi/README.md")
}

var lythFence = regexp.MustCompile("(?ms)^```(?:kotlin|lyth)[ \t]*\n(.*?)^```")

// windowsErrors respells what Windows says about a missing file as Unix
// does, which is how the recorded outputs print it.
var windowsErrors = strings.NewReplacer(
	"The system cannot find the file specified.", "no such file or directory",
	"The system cannot find the path specified.", "no such file or directory",
)

// TestDocExamples compiles and runs every documented program (an example
// with func main) and compares its output, stdout and stderr together, with
// the recorded one. After checking a difference, rerecord with
//
//	LYTH_BLESS=1 go test ./tests -run TestDocExamples
//
// An example whose output changes from run to run is marked
// "// output varies"; it is still compiled and run.
func TestDocExamples(t *testing.T) {
	var recorded []docOutput
	if data, err := os.ReadFile(docOutputs); err == nil {
		if err := json.Unmarshal(data, &recorded); err != nil {
			t.Fatalf("%s: %v", docOutputs, err)
		}
	}
	want := map[string]string{}
	for _, o := range recorded {
		want[o.Source] = o.Output
	}
	type result struct {
		file, source, output string
	}
	var results []*result
	t.Run("run", func(t *testing.T) {
		for _, file := range docSources(t) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			rel := strings.TrimPrefix(filepath.ToSlash(file), "../")
			for i, m := range lythFence.FindAllStringSubmatch(string(data), -1) {
				src := m[1]
				if !strings.Contains(src, "func main(") {
					continue
				}
				r := &result{file: rel, source: src}
				results = append(results, r)
				t.Run(fmt.Sprintf("%s#%d", rel, i+1), func(t *testing.T) {
					t.Parallel()
					out, err := runDocExample(src)
					if err != nil {
						t.Fatalf("example %d in %s:\n%s\n%v", i+1, rel, src, err)
					}
					if runtime.GOOS == "windows" {
						out = windowsErrors.Replace(out)
					}
					r.output = out
					if strings.Contains(src, "// output varies") || os.Getenv("LYTH_BLESS") != "" {
						return
					}
					if w, ok := want[src]; !ok {
						t.Errorf("example %d in %s has no recorded output; record it with LYTH_BLESS=1 go test ./tests -run TestDocExamples", i+1, rel)
					} else if w != out {
						t.Errorf("example %d in %s prints something else now:\n%s", i+1, rel, diffLines(w, out))
					}
				})
			}
		}
	})
	if os.Getenv("LYTH_BLESS") == "" {
		return
	}
	var out []docOutput
	for _, r := range results {
		if r.output != "" && !strings.Contains(r.source, "// output varies") {
			out = append(out, docOutput{File: r.file, Source: r.source, Output: r.output})
		}
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(docOutputs, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runDocExample compiles src as a single-file program and returns what it
// prints, with the exit status appended when it is not zero. Each example
// gets a stable directory, so its synthetic module is reused between runs.
func runDocExample(src string) (string, error) {
	sum := sha256.Sum256([]byte(src))
	dir := filepath.Join(os.TempDir(), "lyth-doc-examples", hex.EncodeToString(sum[:8]), "example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	file := filepath.Join(dir, "example.lyth")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		return "", err
	}
	var log bytes.Buffer
	cfg := &build.Config{WorkDir: dir, Stdout: &log, Stderr: &log}
	s, err := build.Compile(cfg, build.Target{File: file})
	if s != nil && s.Sink.HasErrors() {
		return "", fmt.Errorf("does not compile:\n%s", render(s.Sink.All()))
	}
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "example.bin")
	if ok, err := s.GoBuild(bin, []string{"."}, nil); err != nil || !ok {
		return "", fmt.Errorf("go build failed: %v\n%s%s", err, log.String(), render(s.Sink.All()))
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		fmt.Fprintf(&out, "(exit status %d)\n", exit.ExitCode())
	} else if err != nil {
		return "", err
	}
	return out.String(), nil
}
