// Package skilltool gives the model a skill tool: by a skill's name, the contents of its SKILL.md and the directory
// its other files are in. Skills are found as in context.skills, which puts their index into the system prompt;
// the tool lists only their names. Without skills there is no tool.
//
//	context.skills.tool:
//	  patterns: [.agents/skills/*/SKILL.md, .claude/skills/*/SKILL.md, ~/.agents/skills/*/SKILL.md, ~/.claude/skills/*/SKILL.md]
package skilltool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/modules/context/skills/internal/catalog"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

const description = "Load a skill: the instructions in its SKILL.md and the directory its other files are in. " +
	"Load one when a task matches its description in the skills index, then follow it."

type mod struct {
	b     bus.Bus
	paths map[string]string // skill name → SKILL.md
	spec  tool.Spec
}

func init() { module.Register("context.skills.tool", &mod{}) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	skills, err := catalog.Find(cfg.Map())
	if err != nil {
		return err
	}
	m.b, m.paths = b, map[string]string{}
	var names []string
	for _, s := range skills {
		m.paths[s.Name] = s.Path
		names = append(names, s.Name)
	}
	enum, _ := json.Marshal(names)
	m.spec = tool.Spec{Name: "skill", Description: description, Schema: []byte(`{"type":"object","properties":{` +
		`"name":{"type":"string","enum":` + string(enum) + `,"description":"The skill's name"}},"required":["name"]}`)}
	bus.Async(b, func(ev event.Any) bool {
		u, ok := ev.(event.Event[tool.Use])
		return ok && u.Name() == tool.Call && u.Data().Name == m.spec.Name
	}, m.call)
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	if len(m.paths) == 0 {
		return nil // an empty enum is no schema
	}
	return m.b.Dispatch(ctx, event.New(tool.Declare, m.spec))
}

func (m *mod) Stop(context.Context) error { return nil }

func (m *mod) call(ctx context.Context, ev event.Event[tool.Use]) {
	u := ev.Data()
	text, err := m.load(u.Input)
	slog.Info("tool call", "module", "context.skills.tool", "id", u.ID, "input", string(u.Input)) // the name only
	if err != nil {
		slog.Warn("tool error", "module", "context.skills.tool", "id", u.ID, "err", err)
		text = err.Error()
	}
	_ = m.b.Dispatch(ctx, event.New(tool.Result, tool.Output{ID: u.ID, Text: text, Error: err != nil})) // a failed tx has nowhere to report to
}

func (m *mod) load(input []byte) (string, error) {
	var in struct{ Name string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	path, ok := m.paths[in.Name]
	if !ok {
		return "", fmt.Errorf("no skill %q", in.Name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return "Base directory of the skill: " + dir + "\n\n" + string(data), nil
}
