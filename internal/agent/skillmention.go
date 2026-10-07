package agent

import (
	"path/filepath"
	"strings"

	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/skills"
)

// parseSkillMentions returns the skills text mentions as /name, in order and
// without repeats, and the arguments: the text without the mentions. Only a
// known name is a mention, so /tmp or /usr/bin/env stay words of the text.
func parseSkillMentions(text string, known []skills.Skill) (used []skills.Skill, args string) {
	byName := make(map[string]skills.Skill, len(known))
	for _, s := range known {
		byName[s.Name] = s
	}
	seen := map[string]bool{}
	var rest []string
	for _, w := range strings.Fields(text) {
		if strings.HasPrefix(w, "/") {
			// Punctuation after a mention belongs to the sentence: "use /fix-issue."
			name := strings.TrimRight(w[1:], ".,;:!?)]}»\"'")
			if s, ok := byName[name]; ok {
				if !seen[name] {
					seen[name] = true
					used = append(used, s)
				}
				continue
			}
		}
		rest = append(rest, w)
	}
	return used, strings.Join(rest, " ")
}

// skillMentions reads the instructions of the skills text mentions, with the
// arguments put in; a skill that cannot be read becomes an error entry.
// Skills kept from the model count too: the user calls them by name.
func skillMentions(text, cwd string) []session.Entry {
	if !strings.Contains(text, "/") {
		return nil
	}
	found, _ := skills.Find(cwd)
	used, args := parseSkillMentions(text, found)
	var out []session.Entry
	for _, s := range used {
		e := session.Entry{Kind: session.KindSkill, Path: s.File(), About: s.Name}
		if inst, err := s.Instructions(args); err != nil {
			e.Text, e.IsError = err.Error(), true
		} else {
			e.Text = inst
		}
		out = append(out, e)
	}
	return out
}

// skillNote is the dim line shown under the request for one skill.
func skillNote(e session.Entry, cwd string) string {
	if e.IsError {
		return "/" + e.About + ": " + e.Text
	}
	p := e.Path
	if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		p = rel
	} else {
		p = tildePath(p)
	}
	return "/" + e.About + " (" + p + ")"
}

func skillsBlock(es []session.Entry) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\nThe user invoked these skills with / in the request below " +
		"(a skill typed as a command arrives the same way). Their instructions are loaded for you: " +
		"follow them for this request instead of calling the skill tool again.\n")
	for _, e := range es {
		if e.IsError {
			b.WriteString("\n/" + e.About + ": " + e.Text + "\n")
			continue
		}
		b.WriteString("\nSkill /" + e.About + ":\n" + e.Text)
		if !strings.HasSuffix(e.Text, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("</system-reminder>")
	return b.String()
}
