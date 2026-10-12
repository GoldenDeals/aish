// Package skills sends an index of SKILL.md files as one llm.system fragment once, on Start:
// the name and description from each file's YAML frontmatter, and its path.
// Files are found by glob patterns, relative to the working directory, "~/" is the home directory;
// without a name the skill is named after its directory, and of two skills with one name the first wins.
//
//	context.skills:
//	  patterns: [.agents/skills/*/SKILL.md, .claude/skills/*/SKILL.md, ~/.agents/skills/*/SKILL.md, ~/.claude/skills/*/SKILL.md]
package skills

import (
	"context"
	"log/slog"
	"strings"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/modules/context/skills/internal/catalog"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

const header = "Skills are instructions for specific tasks, each in its own SKILL.md. " +
	"When a task matches a skill's description, load it with the skill tool or read its file, and follow it."

type mod struct {
	b    bus.Bus
	text string
}

func init() { module.Register("context.skills", &mod{}) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	m.b = b
	skills, err := catalog.Find(cfg.Map())
	if err != nil || len(skills) == 0 {
		return err
	}
	lines := []string{header, ""}
	for _, s := range skills {
		line := "- " + s.Name
		if s.Description != "" {
			line += ": " + s.Description
		}
		lines = append(lines, line+" ("+s.Path+")")
	}
	m.text = strings.Join(lines, "\n")
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	if m.text == "" {
		return nil // the API rejects empty text blocks
	}
	slog.Info("skills", "module", "context.skills", "len", len(m.text))
	return m.b.Dispatch(ctx, event.New(llm.System, m.text))
}

func (m *mod) Stop(context.Context) error { return nil }
