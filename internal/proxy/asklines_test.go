package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// interrupted plays what a question drew on a vt cols wide below before,
// then Ctrl+C as the shell sees it: the key goes to the shell, the terminal
// echoes ^C, the client gives up (cancel), and the question's call returns
// on res. It gives the screen with the question and the screen after.
func interrupted(t *testing.T, p *Proxy, out *terminal, cols int, before string, cancel func(), res <-chan error) (asked, left *vt) {
	t.Helper()
	drawn := out.String()
	asked = newVT(cols)
	asked.play(before + drawn)
	if got := p.key([]byte{0x03}); string(got) != "\x03" {
		t.Errorf("Ctrl+C did not reach the shell: %q", got)
	}
	p.mu.Lock()
	p.emit([]byte("^C"))
	p.mu.Unlock()
	cancel()
	select {
	case err := <-res:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("interrupted question: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question stayed after its request")
	}
	left = newVT(cols)
	left.play(before + out.String())
	return asked, left
}

// A reason with line breaks in it, from a hook or a policy, or a path
// with one that a rule names: each line of the question starts at the left
// edge, whatever the break, and Ctrl+C takes all the rows it drew off the
// screen, however many the lines wrap to.
func TestAskReasonLines(t *testing.T) {
	const call = "\x1b[36m❯\x1b[39m \x1b[1mtouch /srv/x\x1b[0m\r\n"
	for _, tc := range []struct {
		name   string
		cols   int
		reason string
		want   []string // the rows of the question
	}{
		{"\\n", 80, "first line\nsecond line", []string{"first line", "second line — allow? [ Yes ]   No"}},
		{"\\r\\n", 80, "first line\r\nsecond line", []string{"first line", "second line — allow? [ Yes ]   No"}},
		{"\\r", 80, "first line\rsecond line", []string{"first line", "second line — allow? [ Yes ]   No"}},
		{"an empty line", 80, "first line\n\nthird line", []string{"first line", "", "third line — allow? [ Yes ]   No"}},
		// A staircase would wrap the second line here: it would start
		// where the first one ends.
		{"a line to the edge", 40, strings.Repeat("x", 40) + "\nsecond line",
			[]string{strings.Repeat("x", 40), "second line — allow? [ Yes ]   No"}},
		{"lines that wrap", 20, "a line wider than the terminal\nand one more",
			[]string{"a line wider than th", "e terminal", "and one more — allow", "?", "[ Yes ]   No"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, out := termProxy(t)
			p.size = func() (int, int) { return tc.cols, 24 }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := make(chan error, 1)
			go func() {
				_, err := p.askUser(ctx, "\x1b[1m"+tc.reason+" — allow?\x1b[0m")
				res <- err
			}()
			waitOpen(t, p, func() bool { return p.ask != nil })
			asked, left := interrupted(t, p, out, tc.cols, call, cancel, res)
			want := append([]string{"❯ touch /srv/x"}, tc.want...)
			if got := asked.lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("the question:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if got := left.lines(); len(got) != 2 || got[0] != "❯ touch /srv/x" || got[1] != "" {
				t.Errorf("screen after Ctrl+C:\n%s", strings.Join(got, "\n"))
			}
			if left.row != 1 || left.col != 0 {
				t.Errorf("the prompt starts at row %d, column %d, not below the call", left.row, left.col)
			}
		})
	}
}

// The question of aish yolo is two lines, the first wider than most
// terminals: the second starts at the left edge, and Ctrl+C takes both off
// the screen. After output that left the cursor off the first column the
// question starts on the line below it, and the erase leaves that output.
func TestYoloQuestionLines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string // the screen before the question
		off    bool
		keep   []string // the rows the erase leaves
	}{
		{"at the left edge", "$ aish yolo\r\n", false, []string{"$ aish yolo", ""}},
		{"after output", "$ printf abc; aish yolo\r\nabc", true, []string{"$ printf abc; aish yolo", "abc", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const cols = 80
			p, out, _ := hosted(t, &scripted{})
			p.mu.Lock()
			p.size = func() (int, int) { return cols, 24 }
			p.col.off = tc.off
			p.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := askYolo(t, p, ctx)
			asked, left := interrupted(t, p, out, cols, tc.before, cancel, res)

			rows := asked.lines()
			first := len(tc.keep) - 1
			if len(rows) < first+3 || strings.Join(rows[:first], "\n") != strings.Join(tc.keep[:first], "\n") {
				t.Fatalf("the question:\n%s", strings.Join(rows, "\n"))
			}
			if !strings.HasPrefix(rows[first], "aish yolo: till this shell exits") {
				t.Errorf("the question starts with %q", rows[first])
			}
			if last := rows[len(rows)-1]; last != "Turn the checks off?   Yes   [ No ]" {
				t.Errorf("its last line: %q", last)
			}
			if got := left.lines(); strings.Join(got, "\n") != strings.Join(tc.keep, "\n") {
				t.Errorf("screen after Ctrl+C:\n%s", strings.Join(got, "\n"))
			}
			if left.row != first || left.col != 0 {
				t.Errorf("the prompt starts at row %d, column %d, not at row %d", left.row, left.col, first)
			}
		})
	}
}
