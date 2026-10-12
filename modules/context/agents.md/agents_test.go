package agentsmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

func TestAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, tc := range map[string]struct {
		files map[string]string // "->target" makes a symlink
		cfg   cfg
		want  []string
	}{
		"none": {nil, cfg{}, nil},
		"symlink and blank": {
			map[string]string{"AGENTS.md": "be brief\n", "CLAUDE.md": "->AGENTS.md", ".claude/CLAUDE.md": " \n"},
			cfg{},
			[]string{"Instructions from AGENTS.md:\n\nbe brief"},
		},
		"patterns": {
			map[string]string{"AGENTS.md": "a", "docs/x.md": "x"},
			cfg{"patterns": []any{"docs/*.md", "AGENTS.md"}},
			[]string{"Instructions from docs/x.md:\n\nx\n\nInstructions from AGENTS.md:\n\na"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for f, body := range tc.files {
				if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
					t.Fatal(err)
				}
				var err error
				if target, ok := strings.CutPrefix(body, "->"); ok {
					err = os.Symlink(target, f)
				} else {
					err = os.WriteFile(f, []byte(body), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			m, b := &mod{}, corebus.New()
			if err := m.Init(b, tc.cfg); err != nil {
				t.Fatal(err)
			}
			var got []string
			bus.Sync(b, 0, event.ByName(llm.System), func(_ context.Context, _ tx.Tx, ev event.Event[string]) error {
				got = append(got, ev.Data())
				return nil
			})
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
