// Package edit gives the model an edit tool: it replaces an exact piece of a file, which must occur once,
// or every occurrence of it.
package edit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/modules/tools/internal/serve"
)

var spec = tool.Spec{
	Name: "edit",
	Description: "Replace old_string with new_string in a file. old_string is matched exactly, whitespace included, " +
		"and must occur once: add surrounding lines to make it unique, or set replace_all to replace every occurrence. " +
		"Copy it from what read showed, without the line numbers. Relative paths start at the working directory.",
	Schema: []byte(`{"type":"object","properties":{` +
		`"path":{"type":"string","description":"The file to change"},` +
		`"old_string":{"type":"string","description":"The exact text to replace"},` +
		`"new_string":{"type":"string","description":"The text to put in its place"},` +
		`"replace_all":{"type":"boolean","description":"Replace every occurrence; default false"}},` +
		`"required":["path","old_string","new_string"]}`),
}

func init() { serve.Register("tools.edit", spec, run) }

type input struct {
	Path       string
	Old        string `json:"old_string"`
	New        string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func run(_ context.Context, in input) (string, error) {
	if in.Path == "" || in.Old == "" {
		return "", errors.New("path and old_string must not be empty")
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return "", err
	}
	text := string(data)
	n := strings.Count(text, in.Old)
	switch {
	case n == 0:
		return "", errors.New("old_string is not in the file")
	case n > 1 && !in.ReplaceAll:
		return "", fmt.Errorf("old_string occurs %d times: make it unique or set replace_all", n)
	}
	if err := os.WriteFile(in.Path, []byte(strings.ReplaceAll(text, in.Old, in.New)), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", n, in.Path), nil
}
