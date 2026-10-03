package agent

import (
	"strings"
	"testing"
)

// TestSystemNoCdIntoCwd keeps the rule against cd'ing into the directory the
// shell is already in: systemPrompt is rebuilt from time to time from
// someone else's prompt, which has no such rule, and losing it would go
// unnoticed.
func TestSystemNoCdIntoCwd(t *testing.T) {
	for _, line := range strings.Split(systemPrompt, "\n") {
		if strings.HasPrefix(line, " - ") && strings.Contains(line, "`cd") &&
			strings.Contains(line, "already in") && strings.Contains(line, "cwd") {
			return
		}
	}
	t.Error("no list item of the system prompt tells not to cd into the cwd the shell is already in")
}
