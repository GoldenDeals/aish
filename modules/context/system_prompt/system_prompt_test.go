package systemprompt

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

func TestPrompt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(file, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		cfg  cfg
		want string // "" means Init fails
	}{
		"text":    {cfg{"text": "from text"}, "from text"},
		"file":    {cfg{"file": file}, "from file"},
		"default": {cfg{}, defaultPrompt},
		"both":    {cfg{"text": "x", "file": file}, ""},
		"missing": {cfg{"file": file + ".nope"}, ""},
		"blank":   {cfg{"text": " \n"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			m, b := &mod{}, corebus.New()
			err := m.Init(b, tc.cfg)
			if tc.want == "" {
				if err == nil {
					t.Error("Init succeeded")
				}
				return
			}
			if err != nil {
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
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
