package check

import (
	"os"
	"regexp"
	"testing"
)

// TestCollNames checks that the hints for collection operations name every
// exported function of std/coll and nothing else.
func TestCollNames(t *testing.T) {
	src, err := os.ReadFile("../../std/coll/coll.lyth")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^func \S+\.([A-Z]\w*)\[`).FindAllSubmatch(src, -1) {
		declared[string(m[1])] = true
	}
	for name := range declared {
		if collNames[name] != name {
			t.Errorf("std/coll declares %s, which collNames lacks", name)
		}
	}
	for name, target := range collNames {
		if !declared[target] {
			t.Errorf("collNames maps %s to %s, which std/coll does not declare", name, target)
		}
	}
}
