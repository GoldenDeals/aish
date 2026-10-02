package main

import (
	"testing"

	"github.com/inebotov/aish/internal/session"
)

func TestSessionModel(t *testing.T) {
	for _, c := range []struct {
		name string
		info session.Info
		want string
	}{
		{"profile", session.Info{Profile: "local", Model: "qwen3:8b"}, "local · qwen3:8b"},
		{"top level", session.Info{TopLevel: true, Model: "claude-opus-5"}, "root · claude-opus-5"},
		// A state saved before there were profiles: its requests go to
		// the profile the shell has, which the list cannot name.
		{"before profiles", session.Info{Model: "claude-opus-5"}, "claude-opus-5"},
		{"nothing saved", session.Info{}, ""},
		{"no model", session.Info{Profile: "local"}, ""},
	} {
		if got := sessionModel(c.info); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
