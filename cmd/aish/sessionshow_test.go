package main

import (
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/session"
)

func TestModelLine(t *testing.T) {
	at := time.Date(2026, 10, 2, 14, 5, 9, 0, time.Local)
	reply := func(provider, model, profile string) session.Entry {
		return session.Entry{Kind: session.KindAssistant, Time: at, Text: "ok", Provider: provider, Model: model, Profile: profile}
	}
	first := reply("anthropic", "claude-opus-5", "")
	local := reply("openai", "qwen3:8b", "local")
	for _, c := range []struct {
		name    string
		prev, e session.Entry
		want    string // "" for no line
	}{
		{name: "first reply", e: first, want: "14:05:09 model root · anthropic/claude-opus-5"},
		{name: "same model", prev: first, e: reply("anthropic", "claude-opus-5", "")},
		{name: "profile switched", prev: first, e: reply("anthropic", "claude-opus-5", "work"), want: "model work · anthropic/claude-opus-5"},
		{name: "model switched", prev: first, e: reply("anthropic", "claude-sonnet-5", ""), want: "model root · anthropic/claude-sonnet-5"},
		// The same model through another API keeps no Raw either.
		{name: "provider switched", prev: reply("openai", "gpt-5", ""), e: reply("openai-responses", "gpt-5", ""), want: "model root · openai-responses/gpt-5"},
		{name: "back to the top level", prev: local, e: first, want: "model root · anthropic/claude-opus-5"},
		{name: "user", e: session.Entry{Kind: session.KindUser, Time: at, Text: "Hi"}},
		// Journals older than the model in the entry, and replies the
		// agent wrote itself (interrupted, out of steps).
		{name: "no model", prev: local, e: session.Entry{Kind: session.KindAssistant, Time: at, Text: "(stopped after 30 steps)"}},
		{name: "no provider", e: reply("", "claude-opus-5", ""), want: "model root · claude-opus-5"},
	} {
		got := undim(modelLine(c.prev, c.e))
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: %q, want no line", c.name, got)
		case !strings.Contains(got, c.want):
			t.Errorf("%s: %q, want %q in it", c.name, got, c.want)
		}
	}
}

// undim drops the escape sequences of the dim style.
func undim(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}
