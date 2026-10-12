package skilltool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish-core/contracts/tool"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

func TestSkill(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".agents/skills/review", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".agents/skills/review/SKILL.md", []byte("---\nname: review\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := corebus.New()
	specs, results := make(chan tool.Spec, 1), make(chan tool.Output, 2)
	bus.Sync(b, 0, event.ByName(tool.Declare), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Spec]) error {
		specs <- ev.Data()
		return nil
	})
	bus.Sync(b, 0, event.ByName(tool.Result), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Output]) error {
		results <- ev.Data()
		return nil
	})
	m := &mod{}
	if err := m.Init(b, cfg{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := <-specs; s.Name != "skill" || !strings.Contains(string(s.Schema), `"enum":["review"]`) {
		t.Errorf("spec = %+v", s)
	}

	call := func(input string) tool.Output {
		t.Helper()
		if err := b.Dispatch(context.Background(), event.New(tool.Call, tool.Use{ID: "1", Name: "skill", Input: []byte(input)})); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-results:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("no result")
			panic("unreachable")
		}
	}
	dir, _ := filepath.Abs(".agents/skills/review")
	if r := call(`{"name":"review"}`); r.Error || r.Text != "Base directory of the skill: "+dir+"\n\n---\nname: review\n---\nbody" {
		t.Errorf("review: %+v", r)
	}
	if r := call(`{"name":"nope"}`); !r.Error {
		t.Errorf("nope: %+v", r)
	}
}
