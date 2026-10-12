// Package write gives the model a write tool: it creates a file, with its directories, or replaces its contents.
package write

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/modules/tools/internal/serve"
)

var spec = tool.Spec{
	Name: "write",
	Description: "Write a file: create it, with its directories, or replace all its contents. " +
		"Relative paths start at the working directory. To change a part of a file, use edit.",
	Schema: []byte(`{"type":"object","properties":{` +
		`"path":{"type":"string","description":"The file to write"},` +
		`"content":{"type":"string","description":"The whole new contents"}},"required":["path","content"]}`),
}

func init() { serve.Register("tools.write", spec, run) }

func run(_ context.Context, in struct{ Path, Content string }) (string, error) {
	if in.Path == "" {
		return "", errors.New("path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil { // an existing file keeps its mode
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}
