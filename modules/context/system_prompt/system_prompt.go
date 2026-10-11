// Package systemprompt sends the system prompt as an llm.system fragment once, on Start.
// It is the text from the config, the contents of a file, or the built-in default.md.
//
//	context.system_prompt:
//	  text: |
//	    You are ...
//
//	context.system_prompt:
//	  file: prompts/aish.md # relative to the working directory
package systemprompt

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

//go:embed default.md
var defaultPrompt string

type mod struct {
	b    bus.Bus
	text string
}

func init() { module.Register("context.system_prompt", &mod{}) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	c := cfg.Map()
	text, _ := c["text"].(string)
	file, _ := c["file"].(string)
	switch {
	case text != "" && file != "":
		return errors.New("text and file are both set, keep one")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("system prompt: %w", err)
		}
		text = string(b)
	case text == "":
		text = defaultPrompt
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("system prompt is empty") // the API rejects empty text blocks
	}
	m.b, m.text = b, text
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	slog.Info("system prompt", "module", "context.system_prompt", "len", len(m.text))
	return m.b.Dispatch(ctx, event.New(llm.System, m.text))
}

func (m *mod) Stop(context.Context) error { return nil }
