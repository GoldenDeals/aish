// Package catalog finds the skills for the context.skills modules: SKILL.md files by glob patterns,
// with the name and description from each file's YAML frontmatter.
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/GoldenDeals/aish-core/modules/context/internal/patterns"
)

// Project skills first: they win over the user's ones with the same name.
var defaults = []string{".agents/skills/*/SKILL.md", ".claude/skills/*/SKILL.md", "~/.agents/skills/*/SKILL.md", "~/.claude/skills/*/SKILL.md"}

type Skill struct {
	Name        string
	Description string // on one line
	Path        string // of its SKILL.md
}

// Find globs c["patterns"], or the defaults. Without a name a skill is named after its directory,
// and of two skills with one name the first wins.
func Find(c map[string]any) ([]Skill, error) {
	files, err := patterns.Files(c, defaults)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("skill: %w", err)
		}
		var front struct{ Name, Description string }
		if rest, ok := strings.CutPrefix(string(data), "---\n"); ok {
			yml, _, _ := strings.Cut(rest, "\n---")
			if err := yaml.Unmarshal([]byte(yml), &front); err != nil {
				return nil, fmt.Errorf("skill %s: %w", f, err)
			}
		}
		if front.Name == "" {
			front.Name = filepath.Base(filepath.Dir(f))
		}
		if seen[front.Name] {
			continue
		}
		seen[front.Name] = true
		skills = append(skills, Skill{front.Name, strings.Join(strings.Fields(front.Description), " "), f})
	}
	return skills, nil
}
