package toolcall

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDraw(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"bash {\"command\":\"ls -la\"}\n", "● bash(ls -la)"},
		{"read {\"limit\":5,\"path\":\"a.go\"}\nok\n1\n2\n3\n4\n5\n", "● read(a.go)\n  ⎿ 1\n    2\n    3\n    … +2 lines"},
		{"write {\"path\":\"a\"}\nok\n", "● write(a)\n  ⎿ (no output)"},
		{"edit {\"path\":\"a\"}\nerror\nnot in the file", "● edit(a)\n  ⎿ not in the file"},
		{"t {\"q\":\"a\\nb\\u001b[31mc\"}\n", "● t(a b [31mc)"}, // the input's control characters don't reach the terminal
		{"t {\"n\":1}\n", "● t"},
		{"bash {\"command\":\"" + "0123456789012345678901234567890" + "\"}\nok\na long line of the output, cut\n", "● bash(0123456789012345678901…\n  ⎿ a long line of the output…"},
	} {
		if got := ansi.Strip(draw(tc.src, 30)); got != tc.want {
			t.Errorf("draw(%q) =\n%s\nwant\n%s", tc.src, got, tc.want)
		}
	}
}
