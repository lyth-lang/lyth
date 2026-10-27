package build

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestWriteAtomic checks that readers of a generated file see a whole
// version while concurrent lyth processes rewrite it, and that no temporary
// file is left behind.
func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lyth.go")
	versions := [][]byte{bytes.Repeat([]byte("a"), 1<<16), bytes.Repeat([]byte("b"), 1<<17)}
	if err := writeAtomic(path, versions[0]); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := range 50 {
				if err := writeIfChanged(path, versions[(w+i)%2]); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, versions[0]) && !bytes.Equal(data, versions[1]) {
			t.Fatalf("read a partial file of %d bytes", len(data))
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files left in the directory: %d, want 1", len(entries))
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, %v; want 0644", info.Mode().Perm(), err)
	}
}
