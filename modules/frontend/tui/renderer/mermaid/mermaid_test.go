package mermaid

import (
	"io"
	"math"
	"os"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

func shown(s string) string {
	b := renderer.New(contract.Markdown)
	b.Write(s)
	_, h := b.Size(80, math.MaxInt)
	scr := uv.NewWindow(80, h, nil)
	b.Draw(scr, scr.Bounds())
	return scr.String()
}

func TestDiagram(t *testing.T) {
	stderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	got := shown("```mermaid\ngraph LR\nA((Идея 💡)) -- Да --> B[План]\nstyle A fill:#f9f\n```")
	bad := shown("```mermaid\njourney\ntitle x\n```")
	w.Close()
	os.Stderr = stderr
	for _, want := range []string{
		"┌─────────┐", // as wide as the label: Cyrillic and the emoji counted in cells
		"│ Идея 💡 ├",  // the circle drawn as a box
		"Да",          // the -- label --> form
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	pause := strings.Split(shown("```mermaid\ngraph LR\nA[⏸️ Пауза]\n```"), "\n")
	if top, label := pause[0], pause[2]; ansi.StringWidth(top) != ansi.StringWidth(label) {
		t.Errorf("the box doesn't close on the label:\n%s\n%s", top, label)
	}
	if strings.Contains(got, "style") {
		t.Errorf("styling drawn as a node:\n%s", got)
	}
	if bad != "  journey\n  title x" { // a diagram it can't draw: the source
		t.Errorf("journey: %q", bad)
	}
	if out, _ := io.ReadAll(r); len(out) != 0 {
		t.Errorf("stderr: %q", out)
	}
	if open := shown("```mermaid\ngraph LR\n"); open != "  graph LR" { // not closed yet: the source
		t.Errorf("open: %q", open)
	}
}

func TestConverted(t *testing.T) {
	for src, want := range map[string][]string{
		"stateDiagram-v2\n[*] --> Idle\nstate Busy {\n  [*] --> Work\n}\nIdle --> Busy : go()\nWork --> [*]": {
			"●", "◉", "go()", "Busy", "Work", "Idle",
		},
		"classDiagram\nclass Animal {\n  <<interface>>\n  +List~Food~ eats\n}\nAnimal <|-- Dog\nZoo \"1\" o-- \"*\" Animal": {
			"«interface»", "+List<Food> eats", "△", "* ◇", // the arrow to the whole: Animal's cardinality first
		},
	} {
		got := shown("```mermaid\n" + src + "\n```")
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("no %q in\n%s", w, got)
			}
		}
		for _, raw := range []string{"[*]", "_start_", "_end_", "~", "<<"} {
			if strings.Contains(got, raw) {
				t.Errorf("%q drawn in\n%s", raw, got)
			}
		}
	}
}

func TestDrawn(t *testing.T) {
	if got := shown("```mermaid\nmindmap\n  root((Корень))\n    A\n      B:::big\n    C[Ц]\n```"); got != "  Корень\n  ├── A\n  │   └── B\n  └── Ц" {
		t.Errorf("mindmap:\n%s", got)
	}

	pie := strings.Split(shown("```mermaid\npie showData title Время\n\"Код\" : 3\n\"Ревью\" : 1\n```"), "\n")
	if len(pie) != 3 || pie[0] != "  Время" || ansi.StringWidth(pie[1]) != ansi.StringWidth(pie[2]) ||
		!strings.HasSuffix(pie[1], "75.0% (3)") || strings.Count(pie[1], "█") != 3*strings.Count(pie[2], "█")+1 {
		t.Errorf("pie: the largest slice the full width, rows aligned:\n%s", strings.Join(pie, "\n"))
	}

	gantt := shown("```mermaid\ngantt\ndateFormat YYYY-MM-DD\nsection Этап\nA :done, a, 2024-01-01, 2d\nB :crit, after a, 2024-01-05\nM :milestone, after a, 0d\n```")
	lines := strings.Split(gantt, "\n")
	col := func(l, bar string) int { return ansi.StringWidth(l[:strings.Index(l, bar)]) }
	if len(lines) != 5 || lines[1] != "  Этап" || col(lines[3], "█") != col(lines[2], "░")+strings.Count(lines[2], "░") ||
		col(lines[4], "◆") != col(lines[3], "█") {
		t.Errorf("gantt: B and M after A:\n%s", gantt)
	}
	if got := shown("```mermaid\ngantt\ndateFormat DD.MM.YYYY\nA : 01.01.2024, 2d\n```"); got != "  gantt\n  dateFormat DD.MM.YYYY\n  A : 01.01.2024, 2d" {
		t.Errorf("gantt in a date format it doesn't read: %q", got)
	}
}

func TestGitGraph(t *testing.T) {
	got := shown("```mermaid\ngitGraph\n  commit id: \"a\"\n  branch dev\n  commit id: \"b\"\n  checkout main\n  branch hot\n" +
		"  commit id: \"c\"\n  checkout main\n  merge hot tag: \"v1\"\n  checkout dev\n  commit id: \"d\"\n  checkout main\n" +
		"  merge dev\n  branch next\n  commit\n```")
	want := `  ●        a
  ├──┐     dev
  │  ●     b
  ├──┼──┐  hot
  │  │  ●  c
  ●──┼──┘  merge hot (v1)
  │  ●     d
  ●──┘     merge dev
  └──┐     next
     ●` // next takes dev's column, done by then
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if bad := shown("```mermaid\ngitGraph\n  checkout nope\n```"); bad != "  gitGraph\n    checkout nope" {
		t.Errorf("checkout of a branch that isn't there: %q", bad)
	}
}
