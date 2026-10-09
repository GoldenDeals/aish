package agent

import (
	"strings"
	"testing"
)

// TestSystemNoCdIntoCwd keeps the rule against cd'ing into the directory the
// shell is already in: the system prompt's text began as someone else's
// prompt, which has no such rule, and losing it in a rewrite would go
// unnoticed.
func TestSystemNoCdIntoCwd(t *testing.T) {
	if !hasCdRule(system("", "")) {
		t.Error("no list item of the system prompt tells not to cd into the cwd the shell is already in")
	}
}

// hasCdRule tells whether a list item of prompt tells not to cd into the
// cwd the shell is already in.
func hasCdRule(prompt string) bool {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, " - ") && strings.Contains(line, "`cd") &&
			strings.Contains(line, "already in") && strings.Contains(line, "cwd") {
			return true
		}
	}
	return false
}
