package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A summary made because the API refused the context as too long starts
// its line from the window, not from the estimate: the estimate starts
// from the last turn the API took, and what did not fit is past it.
func TestCompactedBeforeWindowFull(t *testing.T) {
	a, _, _, ui, cwd := newRejected(t, map[int]error{0: errTooLong()},
		nil, &llm.Response{Text: "the user looked around"}, &llm.Response{Text: "it is 42"})
	a.Cfg.ContextWindow = 200_000
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	out := ui.String()
	if !strings.Contains(out, "compacted: ≥200k → ") || strings.Contains(out, "compacted: 1.0k") {
		t.Errorf("terminal:\n%s", out)
	}
}

// A summary past compact_at says the estimate as it is.
func TestCompactedBeforeThreshold(t *testing.T) {
	a, j, _, ui, cwd := newRejected(t, nil, &llm.Response{Text: "the user looked around"}, &llm.Response{Text: "it is 42"})
	a.Cfg.ContextWindow = 32000
	j.es[1].InputTokens = 30000
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	out := ui.String()
	if !strings.Contains(out, "of the window, compacting") || !strings.Contains(out, "compacted: 30k → ") {
		t.Errorf("terminal:\n%s", out)
	}
}
