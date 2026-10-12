// Package read gives the model a read tool: the lines of a text file with their numbers, 2000 at a time.
package read

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/modules/tools/internal/serve"
)

const lines = 2000

var spec = tool.Spec{
	Name: "read",
	Description: "Read a text file: its lines, each after its number and a tab, up to 2000 from offset. " +
		"Relative paths start at the working directory. Read a file before you edit it.",
	Schema: []byte(`{"type":"object","properties":{` +
		`"path":{"type":"string","description":"The file to read"},` +
		`"offset":{"type":"integer","description":"The first line to read, from 1; default 1"},` +
		`"limit":{"type":"integer","description":"How many lines to read; default 2000"}},"required":["path"]}`),
}

func init() { serve.Register("tools.read", spec, run) }

type input struct {
	Path   string
	Offset int
	Limit  int
}

// shortcut: reads the whole file to show a part of it; stream it if huge logs come up
func run(_ context.Context, in input) (string, error) {
	if in.Path == "" {
		return "", errors.New("path is empty")
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return "", err
	}
	all := strings.SplitAfter(string(data), "\n")
	if all[len(all)-1] == "" {
		all = all[:len(all)-1] // after the last newline
	}
	if len(all) == 0 {
		return "(the file is empty)", nil
	}
	from := max(in.Offset, 1)
	to := min(from-1+cmp.Or(max(in.Limit, 0), lines), len(all))
	if from > len(all) {
		return "", fmt.Errorf("offset %d is past the end: the file has %d lines", from, len(all))
	}
	var b strings.Builder
	for i := from - 1; i < to; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, strings.TrimSuffix(all[i], "\n"))
	}
	if to < len(all) {
		fmt.Fprintf(&b, "(%d more lines: read on from offset %d)\n", len(all)-to, to+1)
	}
	return b.String(), nil
}
