package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillDefersToLythGuide keeps Lyth code out of the Claude Code skill:
// an installed skill outlives the Lyth version it was written for, so it
// sends agents to lyth guide instead of teaching the language itself.
func TestSkillDefersToLythGuide(t *testing.T) {
	data, err := os.ReadFile("../skills/lyth/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := string(data)
	for _, code := range []string{"```", "func ", ":=", "throws", "${", "`struct", "?:"} {
		if strings.Contains(skill, code) {
			t.Errorf("skills/lyth/SKILL.md contains %q; language rules belong in docs/guide/writing-lyth.md", code)
		}
	}
	for _, cmd := range []string{"`lyth guide`", "`lyth check`", "`lyth fix`"} {
		if !strings.Contains(skill, cmd) {
			t.Errorf("skills/lyth/SKILL.md does not mention %s", cmd)
		}
	}

	var market struct {
		Plugins []struct {
			Skills []string `json:"skills"`
		} `json:"plugins"`
	}
	data, err = os.ReadFile("../.claude-plugin/marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &market); err != nil {
		t.Fatal(err)
	}
	if len(market.Plugins) != 1 || len(market.Plugins[0].Skills) != 1 {
		t.Fatalf("marketplace.json should have one plugin with one skill: %+v", market)
	}
	if _, err := os.Stat(filepath.Join("..", market.Plugins[0].Skills[0], "SKILL.md")); err != nil {
		t.Errorf("marketplace.json names a missing skill: %v", err)
	}
}
