// Command logagg_go aggregates generated access-log lines (hand-written Go).
// It is used to compare binary size, build time, and memory with logagg_lyth.
package main

import (
	"fmt"
	"os"
	"strconv"

	"lythbench/internal/fixtures"
	"lythbench/logagg/goimpl"
)

func main() {
	n := 200_000
	if len(os.Args) > 1 {
		v, err := strconv.Atoi(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		n = v
	}
	stats, err := goimpl.Aggregate(fixtures.LogLines(n))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var total int64
	for _, s := range stats {
		total += s.TotalMs
	}
	fmt.Println(len(stats), total)
}
