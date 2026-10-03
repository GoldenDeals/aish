package main

import (
	"fmt"

	"github.com/inebotov/aish/internal/session"
)

// modelLine is the line aish session show puts before a reply that came
// from another provider, model or profile than prev, the reply before it:
// where they changed is where Raw stopped being replayed and the replies
// may read differently. "" for the rest, and for entries that name no
// model: old journals, replies the agent wrote itself.
func modelLine(prev, e session.Entry) string {
	if e.Kind != session.KindAssistant || e.Model == "" {
		return ""
	}
	if e.Provider == prev.Provider && e.Model == prev.Model && e.Profile == prev.Profile {
		return ""
	}
	model := e.Model
	if e.Provider != "" {
		model = e.Provider + "/" + e.Model
	}
	// The profile always, config.Root for an entry's "": the list of
	// sessions leaves out the one config.toml selects, but here the
	// profile is part of what changed.
	where := profileName(e.Profile) + " · " + model
	return fmt.Sprintf("\x1b[2m%s model %s\x1b[0m", e.Time.Format("15:04:05"), where)
}
