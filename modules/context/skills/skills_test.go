package skills

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

func TestSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		review = ".agents/skills/review/SKILL.md"
		plain  = ".claude/skills/plain/SKILL.md"
		dup    = ".claude/skills/other/SKILL.md"
	)
	for name, tc := range map[string]struct {
		files map[string]string
		want  []string // nil with files means Init fails
	}{
		"none": {nil, nil},
		"index": {
			map[string]string{
				review: "---\nname: review\ndescription: >\n  Review a change:\n  is it right.\n---\nbody",
				plain:  "no frontmatter",
				dup:    "---\nname: review\ndescription: loses to the project one\n---\n",
			},
			[]string{header + "\n\n- review: Review a change: is it right. (" + review + ")\n- plain (" + plain + ")"},
		},
		"broken": {map[string]string{review: "---\ndescription: a: b\n---\n"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for f, body := range tc.files {
				if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m, b := &mod{}, corebus.New()
			err := m.Init(b, cfg{})
			if tc.files != nil && tc.want == nil {
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
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
