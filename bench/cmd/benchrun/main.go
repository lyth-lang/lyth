// Command benchrun runs the Lyth-versus-Go benchmark suite and writes a
// report to results/.
//
// For every workload package it compiles two test binaries, one per
// implementation (build tag impl_lyth), runs them in alternating rounds so
// that machine drift affects both equally, and compares the results with
// benchstat. It also measures binary size, build time (cold and warm), and
// peak memory of two equivalent programs (programs/logagg_go and
// programs/logagg_lyth).
//
// Run from the bench directory:
//
//	go run ./cmd/benchrun
//	LYTH_BENCH_DATABASE_URL=postgres:///lyth_bench go run ./cmd/benchrun -count 10
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

const benchstatVersion = "golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e"

var (
	count     = flag.Int("count", 10, "benchmark rounds per implementation")
	benchtime = flag.String("benchtime", "500ms", "go test -benchtime per benchmark run")
	benchRE   = flag.String("bench", ".", "benchmark name regexp")
	outDir    = flag.String("out", "results", "output directory")
	skipBuild = flag.Bool("skip-build-metrics", false, "skip binary size, build time, and memory measurements")
	buildRuns = flag.Int("build-runs", 3, "cold and warm build repetitions")
)

func main() {
	flag.Parse()
	root, err := os.Getwd()
	check(err)
	if data, err := os.ReadFile("go.mod"); err != nil || !strings.HasPrefix(string(data), "module lythbench") {
		fatalf("run benchrun from the bench directory")
	}
	check(os.MkdirAll(filepath.Join(*outDir, "bin"), 0o755))
	lyth := buildLyth(root)

	step("compiling Lyth packages")
	run(root, nil, lyth, "check")
	// The go flags lyth uses: the generated files, and the go.mod that adds
	// Lyth's standard library (lyth/coll, which collections imports).
	goFlags := []string{"-overlay=" + filepath.Join(root, ".lyth", "overlay.json")}
	if _, err := os.Stat(filepath.Join(root, ".lyth", "go.mod")); err == nil {
		goFlags = append(goFlags, "-modfile="+filepath.Join(root, ".lyth", "go.mod"))
	}

	step("checking that both implementations agree")
	run(root, nil, "go", append(append([]string{"test"}, goFlags...), "-count=1", "-run", "TestEquivalence", "./...")...)

	pkgs := benchPackages(root)
	impls := []string{"go", "lyth"}
	bins := map[string]string{}
	for _, p := range pkgs {
		for _, impl := range impls {
			bin := filepath.Join(root, *outDir, "bin", strings.ReplaceAll(p, "/", "_")+"."+impl+".test")
			args := append(append([]string{"test", "-c"}, goFlags...), "-o", bin)
			if impl == "lyth" {
				args = append(args, "-tags", "impl_lyth")
			}
			run(root, nil, "go", append(args, "./"+p)...)
			bins[p+"/"+impl] = bin
		}
	}

	results := map[string]*bytes.Buffer{"go": {}, "lyth": {}}
	for round := 0; round < *count; round++ {
		order := impls
		if round%2 == 1 {
			order = []string{"lyth", "go"} // alternate who runs first
		}
		step(fmt.Sprintf("benchmark round %d/%d", round+1, *count))
		for _, p := range pkgs {
			for _, impl := range order {
				out := output(filepath.Join(root, p), nil, bins[p+"/"+impl],
					"-test.run", "^$", "-test.bench", *benchRE, "-test.benchmem",
					"-test.benchtime", *benchtime, "-test.count", "1")
				results[impl].WriteString(out)
			}
		}
	}
	for impl, buf := range results {
		check(os.WriteFile(filepath.Join(*outDir, impl+".txt"), buf.Bytes(), 0o644))
	}

	step("comparing with benchstat")
	stat := output(root, nil, "go", "run", benchstatVersion, "-row", ".fullname", "-col", ".file",
		filepath.Join(*outDir, "go.txt"), filepath.Join(*outDir, "lyth.txt"))
	check(os.WriteFile(filepath.Join(*outDir, "benchstat.txt"), []byte(stat), 0o644))
	fmt.Println(stat)

	var build string
	if !*skipBuild {
		build = buildMetrics(root, lyth)
		fmt.Println(build)
	}
	writeReport(root, stat, build)
}

// benchPackages lists directories containing benchmark functions.
func benchPackages(root string) []string {
	var pkgs []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (strings.HasPrefix(d.Name(), ".") || d.Name() == *outDir) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, "_test.go") {
			data, _ := os.ReadFile(path)
			if bytes.Contains(data, []byte("func Benchmark")) {
				rel, _ := filepath.Rel(root, filepath.Dir(path))
				if !slices.Contains(pkgs, rel) {
					pkgs = append(pkgs, rel)
				}
			}
		}
		return nil
	})
	slices.Sort(pkgs)
	return pkgs
}

// buildMetrics compares binary size, cold/warm build time, and peak memory
// of the equivalent logagg programs.
func buildMetrics(root, lyth string) string {
	var b strings.Builder
	step("measuring binary size and build time")
	progs := map[string]string{"go": "programs/logagg_go", "lyth": "programs/logagg_lyth"}
	srcs := map[string]string{"go": "programs/logagg_go/main.go", "lyth": "programs/logagg_lyth/main.lyth"}
	binPath := func(impl string) string { return filepath.Join(root, *outDir, "bin", "logagg_"+impl) }
	build := func(impl string, env []string) time.Duration {
		start := time.Now()
		if impl == "go" {
			run(root, env, "go", "build", "-o", binPath(impl), "./"+progs[impl])
		} else {
			run(root, env, lyth, "build", "-o", binPath(impl), "./"+progs[impl])
		}
		return time.Since(start)
	}
	cold := map[string][]time.Duration{}
	warm := map[string][]time.Duration{}
	// The warm edit adds a function, so the generated Go changes too (Lyth
	// drops comments, so a comment-only edit would leave nothing to rebuild).
	edits := map[string]string{"go": "\nfunc rebuild%d() int { return %d }\n", "lyth": "\nfunc rebuild%d(): int = %d\n"}
	// Series alternate within each run so that drifting machine state
	// (thermal throttling, background load) affects all of them alike.
	// "lyth-nocache" disables Lyth's persistent Go type cache, so every build
	// runs `go list -export` (see docs/design/benchmarks.md).
	for i := 0; i < *buildRuns; i++ {
		for _, series := range []string{"go", "lyth", "lyth-nocache"} {
			impl, extra := series, []string(nil)
			if series == "lyth-nocache" {
				impl, extra = "lyth", []string{"LYTH_NO_CACHE=1"}
			}
			cache, err := os.MkdirTemp("", "lythbench-gocache-")
			check(err)
			env := append([]string{"GOCACHE=" + cache}, extra...)
			cold[series] = append(cold[series], build(impl, env))
			// Warm: edit the main source file only, rebuild with the same cache.
			orig, err := os.ReadFile(srcs[impl])
			check(err)
			for j := 0; j < *buildRuns; j++ {
				check(os.WriteFile(srcs[impl], append(orig, []byte(fmt.Sprintf(edits[impl], j, j))...), 0o644))
				warm[series] = append(warm[series], build(impl, env))
			}
			check(os.WriteFile(srcs[impl], orig, 0o644))
			os.RemoveAll(cache)
		}
	}
	sizes := map[string]int64{}
	for _, impl := range []string{"go", "lyth"} {
		build(impl, nil)
		st, err := os.Stat(binPath(impl))
		check(err)
		sizes[impl] = st.Size()
	}
	// Lyth phase breakdown on a warm build.
	timings := output(root, nil, lyth, "build", "-timings", "-o", binPath("lyth"), "./"+progs["lyth"])

	step("measuring run time and peak memory")
	type runStat struct{ wall, rss []float64 }
	stats := map[string]*runStat{"go": {}, "lyth": {}}
	for i := 0; i < 5; i++ {
		for _, impl := range []string{"go", "lyth"} {
			cmd := exec.Command(binPath(impl), "1000000")
			var out bytes.Buffer
			cmd.Stdout = &out
			start := time.Now()
			check(cmd.Run())
			stats[impl].wall = append(stats[impl].wall, float64(time.Since(start).Milliseconds()))
			stats[impl].rss = append(stats[impl].rss, float64(maxRSS(cmd.ProcessState))/(1<<20))
		}
	}

	fmt.Fprintf(&b, "| Metric | Go | Lyth | Δ |\n|---|---:|---:|---:|\n")
	row := func(name string, g, n float64, unit string) {
		fmt.Fprintf(&b, "| %s | %.1f %s | %.1f %s | %+.1f%% |\n", name, g, unit, n, unit, (n-g)/g*100)
	}
	row("binary size", float64(sizes["go"])/1024, float64(sizes["lyth"])/1024, "KiB")
	row("cold build (median)", median(ms(cold["go"])), median(ms(cold["lyth"])), "ms")
	row("warm rebuild after editing main (median)", median(ms(warm["go"])), median(ms(warm["lyth"])), "ms")
	row("warm rebuild, Lyth type cache off (median)", median(ms(warm["go"])), median(ms(warm["lyth-nocache"])), "ms")
	row("run time, 1M lines (median)", median(stats["go"].wall), median(stats["lyth"].wall), "ms")
	row("peak RSS, 1M lines (median)", median(stats["go"].rss), median(stats["lyth"].rss), "MiB")
	fmt.Fprintf(&b, "\nLyth build phases (warm, `lyth build -timings`):\n\n```\n%s```\n", timings)
	return b.String()
}

func maxRSS(ps *os.ProcessState) int64 {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes
	}
	return int64(ru.Maxrss) * 1024 // kilobytes elsewhere
}

func ms(ds []time.Duration) []float64 {
	out := make([]float64, len(ds))
	for i, d := range ds {
		out[i] = float64(d.Milliseconds())
	}
	return out
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)/2]
}

func writeReport(root, stat, build string) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Lyth vs Go benchmark results\n\n")
	fmt.Fprintf(&b, "Generated by `go run ./cmd/benchrun` on %s.\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "| Environment | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Go | %s |\n", strings.TrimSpace(output(root, nil, "go", "env", "GOVERSION")))
	fmt.Fprintf(&b, "| OS/arch | %s/%s |\n", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "darwin" {
		fmt.Fprintf(&b, "| CPU | %s (%d logical cores) |\n", strings.TrimSpace(output(root, nil, "sysctl", "-n", "machdep.cpu.brand_string")), runtime.NumCPU())
	} else {
		fmt.Fprintf(&b, "| CPU | %d logical cores |\n", runtime.NumCPU())
	}
	fmt.Fprintf(&b, "| Rounds | %d per implementation, alternating; -benchtime %s |\n", *count, *benchtime)
	fmt.Fprintf(&b, "| PostgreSQL workload | %s |\n\n", map[bool]string{true: "enabled", false: "skipped (LYTH_BENCH_DATABASE_URL not set)"}[os.Getenv("LYTH_BENCH_DATABASE_URL") != ""])
	fmt.Fprintf(&b, "## Micro and workload benchmarks\n\nColumns: hand-written Go (`go.txt`) vs Lyth (`lyth.txt`). A `~` means no statistically significant difference (benchstat, p ≥ 0.05).\n\n```\n%s```\n\n", stat)
	if build != "" {
		fmt.Fprintf(&b, "## Build, binary size, and memory\n\n%s\n", build)
	}
	check(os.WriteFile(filepath.Join(*outDir, "report.md"), []byte(b.String()), 0o644))
	step("wrote " + filepath.Join(*outDir, "report.md"))
}

// buildLyth builds the compiler from the parent module.
func buildLyth(root string) string {
	step("building lyth")
	bin := filepath.Join(root, *outDir, "bin", "lyth")
	run(filepath.Dir(root), nil, "go", "build", "-o", bin, "./cmd/lyth")
	return bin
}

func step(msg string) { fmt.Fprintf(os.Stderr, "==> %s\n", msg) }

func run(dir string, env []string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "PWD="+dir), env...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
}

func output(dir string, env []string, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "PWD="+dir), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		fatalf("%s %s: %v\n%s%s", name, strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String() + errb.String()
}

func check(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "benchrun: "+format+"\n", args...)
	os.Exit(1)
}
