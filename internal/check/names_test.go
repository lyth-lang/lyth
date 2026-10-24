package check

import "testing"

// TestRequalify checks that only whole package paths are rewritten in the
// names go/types writes: myapp is not qualified by the package app.
func TestRequalify(t *testing.T) {
	quals := map[string]string{"app": "", "app/myapp": "myapp.", "gopkg.in/yaml.v3": "yaml."}
	for word, want := range map[string]string{
		"app.mem":               "mem",
		"app/myapp.Store":       "myapp.Store",
		"app/myapp.Store.Get":   "myapp.Store.Get",
		"gopkg.in/yaml.v3.Node": "yaml.Node",
		"myapp.Store":           "myapp.Store",
		"other/app.T":           "other/app.T",
		"...int":                "...int",
	} {
		if got := requalify(word, quals); got != want {
			t.Errorf("requalify(%q) = %q, want %q", word, got, want)
		}
	}
}
