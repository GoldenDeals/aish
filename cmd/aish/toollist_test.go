package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/tools"
)

// The list shows what aish tool NAME runs: bash is typed in the shell
// itself, and ask_user is the model's.
func TestListTools(t *testing.T) {
	var reg tools.Registry
	for _, tool := range tools.Builtins() {
		reg.Add(tool)
	}
	var b bytes.Buffer
	listTools(&b, &reg)
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		name, _, _ := strings.Cut(line, " ")
		listed[name] = true
	}
	for _, name := range []string{"bash", "ask_user"} {
		if listed[name] {
			t.Errorf("%s is listed, but aish tool %s says no tool:\n%s", name, name, b.String())
		}
	}
	if !listed["read_file"] {
		t.Errorf("read_file is not listed:\n%s", b.String())
	}
	for _, tool := range reg.All() {
		if listed[tool.Name()] != runnable(tool) {
			t.Errorf("%s: listed %v, runnable %v", tool.Name(), listed[tool.Name()], runnable(tool))
		}
	}
}
