package build

import (
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// TestProtect checks that a panic in a compiler stage becomes an E0901
// diagnostic naming where it was raised, instead of crashing lyth.
func TestProtect(t *testing.T) {
	sink := &diag.Sink{}
	f := source.NewFile("/m/a.lyth", "a.lyth", []byte("func main() {}\n"))
	var m map[string]int
	err := protect(sink, source.Span{File: f}, "while parsing a.lyth", func() { m["x"] = 1 })
	if err == nil {
		t.Fatal("protect returned no error for a panic")
	}
	all := sink.All()
	if len(all) != 1 || all[0].Code != diag.EInternal {
		t.Fatalf("diagnostics = %v, want one E0901", all)
	}
	d := all[0]
	if !strings.Contains(d.Message, "internal compiler error while parsing a.lyth: assignment to entry in nil map") {
		t.Errorf("message = %q", d.Message)
	}
	notes := strings.Join(d.Notes, "\n")
	if !strings.Contains(notes, "raised at build.TestProtect") || !strings.Contains(notes, "report_test.go:") {
		t.Errorf("notes do not name the panicking function:\n%s", notes)
	}
	if !strings.Contains(notes, "please report this") {
		t.Errorf("notes do not ask for a report:\n%s", notes)
	}
	if err := protect(sink, source.Span{File: f}, "", func() {}); err != nil || len(sink.All()) != 1 {
		t.Errorf("protect without a panic: err %v, %d diagnostics", err, len(sink.All()))
	}
}

// TestToolchainOutputCodes checks how go build output without a position is
// reported: linker failures as E0904, anything else as E0903.
func TestToolchainOutputCodes(t *testing.T) {
	for _, tc := range []struct {
		out  string
		code diag.Code
	}{
		{"/usr/bin/ld: main.o: undefined reference to `C.missing'\ncollect2: error: ld returned 1 exit status", diag.EGoLink},
		{"link: duplicated definition of symbol main.f", diag.EGoLink},
		{"go: downloading example.com/m v1.0.0\nverifying module: checksum mismatch", diag.EGoCompile},
	} {
		s := &Session{Sink: &diag.Sink{}}
		s.mapToolchainOutput(tc.out)
		all := s.Sink.All()
		if len(all) != 1 || all[0].Code != tc.code || !strings.Contains(strings.Join(all[0].Notes, "\n"), strings.SplitN(tc.out, "\n", 2)[0]) {
			t.Errorf("%q: got %v, want one %s carrying the output", tc.out, all, tc.code)
		}
	}
}
