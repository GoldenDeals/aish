package main

import (
	"strings"
	"testing"
)

func TestWordList(t *testing.T) {
	got := wordList(UserCommands)
	if !strings.HasSuffix(usage, got) {
		t.Errorf("usage does not end with the list of UserCommands:\n%s", got)
	}
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if len(l) > 80 || !strings.HasPrefix(l, "  ") {
			t.Errorf("line %q: not indented by two or over 80 columns", l)
		}
	}
	words := strings.Fields(strings.ReplaceAll(got, ",", ""))
	if strings.Join(words, " ") != strings.Join(UserCommands, " ") {
		t.Errorf("wordList = %q, want every one of %q in order", got, UserCommands)
	}
	if got := wordList([]string{"a"}); got != "  a\n" {
		t.Errorf("wordList(a) = %q", got)
	}
}
