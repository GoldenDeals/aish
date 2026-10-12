// Package agentsmd sends AGENTS.md and CLAUDE.md files as one llm.system fragment once, on Start.
// Files are found by glob patterns, relative to the working directory, "~/" is the home directory;
// a file that repeats an earlier one, like CLAUDE.md linked to AGENTS.md, is skipped.
//
//	context.agents.md:
//	  patterns: [~/.agents/AGENTS.md, ~/.claude/CLAUDE.md, AGENTS.md, CLAUDE.md, .agents/AGENTS.md, .claude/CLAUDE.md]
package agentsmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/modules/context/internal/patterns"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

// Global files first: the project ones come later in the prompt and take precedence.
var defaults = []string{"~/.agents/AGENTS.md", "~/.claude/CLAUDE.md", "AGENTS.md", "CLAUDE.md", ".agents/AGENTS.md", ".claude/CLAUDE.md"}

type mod struct {
	b    bus.Bus
	text string
}

func init() { module.Register("context.agents.md", &mod{}) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	files, err := patterns.Files(cfg.Map(), defaults)
	if err != nil {
		return err
	}
	var parts []string
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("agents.md: %w", err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		parts = append(parts, "Instructions from "+f+":\n\n"+text)
	}
	m.b, m.text = b, strings.Join(parts, "\n\n")
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	if m.text == "" {
		return nil // the API rejects empty text blocks
	}
	slog.Info("agents.md", "module", "context.agents.md", "len", len(m.text))
	return m.b.Dispatch(ctx, event.New(llm.System, m.text))
}

func (m *mod) Stop(context.Context) error { return nil }
