// Package appendprompt adds text to the system prompt as an llm.system fragment once, on Start,
// next to the one from context.system_prompt. It is the text from the config or the contents of a file.
// It runs only with its block in the config.
//
//	context.append:
//	  text: |
//	    Answer in Russian.
//
//	context.append:
//	  file: prompts/extra.md # relative to the working directory
package appendprompt

import (
	"context"
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

type mod struct {
	b    bus.Bus
	text string
}

func init() { module.Register("context.append", &mod{}, module.OptIn()) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	c := cfg.Map()
	text, _ := c["text"].(string)
	file, _ := c["file"].(string)
	if (text == "") == (file == "") {
		return errors.New("set one of text and file")
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("append: %w", err)
		}
		text = string(b)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("append is empty") // the API rejects empty text blocks
	}
	m.b, m.text = b, text
	return nil
}

func (m *mod) Start(ctx context.Context) error {
	slog.Info("append", "module", "context.append", "len", len(m.text))
	return m.b.Dispatch(ctx, event.New(llm.System, m.text))
}

func (m *mod) Stop(context.Context) error { return nil }
