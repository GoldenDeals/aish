package agent

import (
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
)

// ContextMessages are the messages the next turn of an agent with cfg sends
// for the journal es: what request builds, without the system prompt and
// the tools. Secrets are masked and output truncated as the model gets
// them, so `aish context` shows no more than the model is sent.
func ContextMessages(es []session.Entry, cfg config.Config) ([]llm.Message, error) {
	mask, err := NewMasker(cfg.MaskDefaults, cfg.Mask)
	if err != nil {
		return nil, err
	}
	return Messages(ownRaw(es, cfg.Profile), cfg.MaxOutputBytes, mask), nil
}
