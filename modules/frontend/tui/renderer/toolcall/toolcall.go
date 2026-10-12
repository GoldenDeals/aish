// Package toolcall registers with the renderer core how a tool call (contracts/tui/renderer.Tool) looks:
// "● name(text)" with the first text value of its input, then the first lines of its output under it.
// The bullet is faint while the tool runs, green once it is done and red when it failed, as is the output then.
package toolcall

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	contract "github.com/GoldenDeals/aish-core/contracts/tui/renderer"
	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

const shown = 3 // lines of the output under a call

var (
	running = uv.Style{Attrs: uv.AttrFaint}
	done    = uv.Style{Fg: ansi.Green}
	failed  = uv.Style{Fg: ansi.Red}
	name    = uv.Style{Attrs: uv.AttrBold}

	// text is the first key with a string value and the value, quoted: the command, path or query of most tools.
	text = regexp.MustCompile(`"(?:[^"\\]|\\.)*"\s*:\s*("(?:[^"\\]|\\.)*")`)
)

func init() { renderer.Kind(contract.Tool, draw) }

func draw(src string, width int) string {
	head, result, _ := strings.Cut(src, "\n")
	tool, input, _ := strings.Cut(head, " ")
	status, output, _ := strings.Cut(result, "\n")
	bullet, out := running, running
	switch status {
	case "ok":
		bullet = done
	case "error":
		bullet, out = failed, failed
	}
	call := renderer.Styled(bullet, "●") + " " + renderer.Styled(name, tool)
	if s := summary(input); s != "" {
		call += "(" + s + ")"
	}
	lines := []string{ansi.Truncate(call, width, "…")}
	if status == "" {
		return lines[0]
	}
	rows := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if rows[0] == "" && len(rows) == 1 {
		rows[0] = "(no output)"
	}
	for i, r := range rows[:min(len(rows), shown)] {
		prefix := "    "
		if i == 0 {
			prefix = "  ⎿ "
		}
		lines = append(lines, renderer.Styled(out, ansi.Truncate(prefix+r, width, "…")))
	}
	if n := len(rows) - shown; n > 0 {
		lines = append(lines, renderer.Styled(running, ansi.Truncate(fmt.Sprintf("    … +%d lines", n), width, "…")))
	}
	return strings.Join(lines, "\n")
}

// summary is the first text value of the input on one line; the JSON may escape control characters, they go too.
func summary(input string) string {
	m := text.FindStringSubmatch(input)
	var s string
	if m == nil || json.Unmarshal([]byte(m[1]), &s) != nil {
		return ""
	}
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
}
