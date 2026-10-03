package proxy

import (
	"fmt"
	"strings"
	"testing"
)

// The status of these tests: 5 cells at 34-38 of a terminal 40 wide.
const (
	lineShow = "\x1b7\x1b[35G\x1b[2mctx-m\x1b[0m\x1b8"
	lineHide = "\x1b7\x1b[35G\x1b[K\x1b8"
)

// shown and hidden are lineShow and lineHide from up lines below the status.
func shown(up int) string  { return fmt.Sprintf("\x1b7\x1b[%dA\x1b[35G\x1b[2mctx-m\x1b[0m\x1b8", up) }
func hidden(up int) string { return fmt.Sprintf("\x1b7\x1b[%dA\x1b[35G\x1b[K\x1b8", up) }

// atPrompt is the line after cmd-end, bash's prompt and the first key.
func atPrompt(t *testing.T) *inputLine {
	t.Helper()
	l := newInputLine(40, 24, "ctx-m", "\x1b[2m")
	if got := string(l.draw()); got != lineShow {
		t.Fatalf("draw %q", got)
	}
	if got := string(l.feed([]byte("\x1b[?2004h$ "))); got != "\x1b[?2004h$ " {
		t.Fatalf("prompt %q", got)
	}
	l.typed()
	return l
}

type step struct{ in, want string }

func feedSteps(t *testing.T, name string, l *inputLine, steps []step) {
	t.Helper()
	for i, s := range steps {
		if got := string(l.feed([]byte(s.in))); got != s.want {
			t.Errorf("%s, step %d %q:\n got %q\nwant %q", name, i, s.in, got, s.want)
		}
	}
}

// The status's bytes on cmd-end are the ones the proxy always printed.
func TestInputLineDraw(t *testing.T) {
	text, color := "ctx-m", "\x1b[2m"
	old := fmt.Sprintf("\x1b7\x1b[%dG%s%s\x1b[0m\x1b8", 40-len(text), color, text)
	if got := string(newInputLine(40, 24, text, color).draw()); got != old {
		t.Errorf("draw %q, want %q", got, old)
	}
}

// What readline prints (bash 5.3, PS1='$ ', 40 columns) as the line gets
// text and loses it: the status goes with the first text and comes back
// with the output that leaves the line empty.
func TestInputLineReadline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"typing, Backspace", []step{
			{"a", "a" + lineHide},
			{"bc", "bc"},
			{"\b\x1b[K", "\b\x1b[K"},
			{"\b\x1b[K", "\b\x1b[K"},
			{"\b\x1b[K", "\b\x1b[K" + lineShow},
			{"\a", "\a"}, // Backspace on the empty line
		}},
		{"Ctrl+U, Ctrl+W", []step{
			{"abc", "abc" + lineHide},
			{"\b\b\b\x1b[K", "\b\b\b\x1b[K" + lineShow},
		}},
		{"Ctrl+A, Ctrl+K", []step{
			{"abc", "abc" + lineHide},
			{"\b\b\b", "\b\b\b"},
			{"\x1b[K", "\x1b[K" + lineShow},
		}},
		{"Ctrl+A, Ctrl+D", []step{
			{"abc", "abc" + lineHide},
			{"\b\b\b", "\b\b\b"},
			{"\x1b[1P", "\x1b[1P"},
			{"\x1b[1Pc\b", "\x1b[1Pc\b"},
			{"\x1b[K", "\x1b[K" + lineShow},
		}},
		{"Ctrl+A, insert", []step{
			{"abc", "abc" + lineHide},
			{"\b\b\b", "\b\b\b"},
			{"\x1b[1@x", "\x1b[1@x"},
			{"\x1b[C", "\x1b[C"},
		}},
		{"history", []step{
			{"echo hello", "echo hello" + lineHide},
			{"\r\x1b[C\x1b[C\x1b[K", "\r\x1b[C\x1b[C\x1b[K" + lineShow},
		}},
		{"Ctrl+R, Ctrl+G", []step{
			{"\r(reverse-i-search)`': ", "\r(reverse-i-search)`': " + lineHide},
			{"\b\b\be': echo h\x1b[7me\x1b[27mllo\b\b\b\b", "\b\b\be': echo h\x1b[7me\x1b[27mllo\b\b\b\b"},
			{"\r$ \x1b[K", "\r$ \x1b[K" + lineShow},
		}},
		{"paste, Ctrl+U", []step{
			{"\x1b[7mls -l\x1b[27m", "\x1b[7mls -l\x1b[27m" + lineHide},
			{"\r\x1b[C\x1b[C\x1b[K", "\r\x1b[C\x1b[C\x1b[K" + lineShow},
		}},
		{"bell", []step{
			{"\a", "\a"},
		}},
		{"a long line in one go", []step{
			// Erased before the character that would land on it.
			{strings.Repeat("x", 45), strings.Repeat("x", 32) + lineHide + strings.Repeat("x", 13)},
			{"\x1b[A\r\x1b[C\x1b[C\x1b[K\r\n\r\x1b[K\x1b[A\x1b[C\x1b[C", "\x1b[A\r\x1b[C\x1b[C\x1b[K\r\n\r\x1b[K\x1b[A\x1b[C\x1b[C" + lineShow},
		}},
	} {
		feedSteps(t, tc.name, atPrompt(t), tc.steps)
	}
}

// A long line typed key by key: readline wraps it with " \r", and the
// status is not back while the first line is still taken.
func TestInputLineWrapped(t *testing.T) {
	l := atPrompt(t)
	steps := []step{{"y", "y" + lineHide}}
	for range 36 {
		steps = append(steps, step{"y", "y"})
	}
	steps = append(steps, step{"y \r", "y \r"}) // the last column: the cursor goes to the next line
	for range 7 {
		steps = append(steps, step{"y", "y"})
	}
	for range 6 {
		steps = append(steps, step{"\b\x1b[K", "\b\x1b[K"})
	}
	steps = append(steps,
		step{"\r\x1b[K", "\r\x1b[K"},
		step{"\x1b[A\x1b[C\x1b[C\x1b[K\r\n\r\x1b[K\x1b[A\x1b[C\x1b[C", "\x1b[A\x1b[C\x1b[C\x1b[K\r\n\r\x1b[K\x1b[A\x1b[C\x1b[C" + lineShow},
	)
	feedSteps(t, "45 keys", l, steps)
}

// With a prompt of two lines the status is on the last, the one typed
// on: the new line takes it down from the first.
func TestInputLineTwoLinePrompt(t *testing.T) {
	l := newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "prompt", l, []step{{"top\r\n$ ", "top\r" + lineHide + "\n$ " + lineShow}})
	l.typed()
	feedSteps(t, "two lines", l, []step{
		{"ab", "ab" + lineHide},
		{"\b\b\x1b[K", "\b\b\x1b[K" + lineShow},
		{"\a", "\a"},
	})

	// A prompt that goes down without a new line leaves the status above
	// it: erasing and drawing it go up there.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "moved down", l, []step{{"top\x1b[B\r$ ", "top\x1b[B\r$ "}})
	l.typed()
	feedSteps(t, "status above", l, []step{
		{"ab", "ab" + hidden(1)},
		{"\b\b\x1b[K", "\b\b\x1b[K" + shown(1)},
	})

	// A character in the last column waits there to wrap: \e8 may cancel
	// that, so nothing is added till the line goes on.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	l.feed([]byte("top\x1b[B\r$ "))
	l.typed()
	feedSteps(t, "last column", l, []step{
		{strings.Repeat("x", 38), strings.Repeat("x", 38)},
		{" \r", " \r" + hidden(2)},
	})
}

// What PROMPT_COMMAND prints after cmd-end goes above the prompt: the
// status goes down with every new line before the first key and stays on
// the prompt's line, however many lines that takes.
func TestInputLinePromptCommand(t *testing.T) {
	l := newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "hello", l, []step{
		{"hello\r\n", "hello\r" + lineHide + "\n" + lineShow},
		{"$ ", "$ "},
	})
	l.typed()
	feedSteps(t, "typing", l, []step{
		{"a", "a" + lineHide},
		{"\b\x1b[K", "\b\x1b[K" + lineShow},
	})

	// More lines than the screen has: the status is not lost with the
	// line cmd-end found the cursor on.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	out := l.feed([]byte(strings.Repeat("line\r\n", 30) + "$ "))
	if want := "line\r" + lineHide + "\n" + strings.Repeat("line\r\n", 29) + "$ " + lineShow; string(out) != want {
		t.Errorf("30 lines:\n got %q\nwant %q", out, want)
	}
	l.typed()
	feedSteps(t, "after 30 lines", l, []step{
		{"a", "a" + lineHide},
		{"\b\x1b[K", "\b\x1b[K" + lineShow},
	})

	// A line that reaches the status erases it; the next one has room.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "long line", l, []step{
		{strings.Repeat("-", 40), strings.Repeat("-", 34) + lineHide + strings.Repeat("-", 6)},
		{"\r\n$ ", "\r\n$ " + lineShow},
	})

	// After the first key a new line is readline's, not the prompt's.
	l = atPrompt(t)
	feedSteps(t, "after the first key", l, []step{
		{"\r\n", "\r\n"},
		{"ab", "ab" + hidden(1)},
	})
}

// Sequences and characters cut between writes come out the same, what the
// status needs added where the cut closes.
func TestInputLineCut(t *testing.T) {
	l := atPrompt(t)
	feedSteps(t, "CSI", l, []step{
		{"a", "a" + lineHide},
		{"\b\x1b[", "\b"},
		{"K", "\x1b[K" + lineShow},
		{"b", "b" + lineHide},
		{"\b\x1b", "\b"},
		{"[K", "\x1b[K" + lineShow},
		{"\xd0", ""},
		{"\xb6", "ж" + lineHide},
	})

	// Before the first key the line is the prompt's: the text stays next
	// to the status till it reaches it.
	l = newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "wide", l, []step{
		{"$ " + strings.Repeat("x", 31), "$ " + strings.Repeat("x", 31)},
		{"日"[:2], ""},
		{"日"[2:], lineHide + "日"},
		{"\b\b\x1b[K", "\b\b\x1b[K" + lineShow},
	})

	l = atPrompt(t)
	feedSteps(t, "OSC", l, []step{
		{"a", "a" + lineHide},
		{"\b\x1b[K\x1b]0;ti", "\b\x1b[K\x1b]0;ti"},
		{"tle\a", "tle\a" + lineShow},
		{"\x1b]7;file://h/tmp\x1b", "\x1b]7;file://h/tmp"},
		{"\\", "\x1b\\"},
	})
}

// Ctrl+L and the like are past the model: the status is erased first and
// does not come back till the next prompt.
func TestInputLineLost(t *testing.T) {
	l := atPrompt(t)
	feedSteps(t, "Ctrl+L", l, []step{
		{"\x1b[H\x1b[J$ ", lineHide + "\x1b[H\x1b[J$ "},
		{"abc", "abc"},
		{"\b\b\b\x1b[K", "\b\b\b\x1b[K"},
	})
	l = atPrompt(t)
	feedSteps(t, "alternate screen", l, []step{
		{"a", "a" + lineHide},
		{"\x1b[?1049h", "\x1b[?1049h"},
		{"\x1b[?1049l\b\x1b[K", "\x1b[?1049l\b\x1b[K"},
	})

	// The status scrolled off the screen: nothing is added any more.
	l = atPrompt(t)
	var steps []step
	for range 24 {
		steps = append(steps, step{"\r\n", "\r\n"})
	}
	steps = append(steps, step{"abc", "abc"}, step{"\b\b\b\x1b[K", "\b\b\b\x1b[K"})
	feedSteps(t, "scrolled", l, steps)
}

// Text before the first key, as typed ahead while a command ran, is taken
// for the prompt's.
func TestInputLineTypeahead(t *testing.T) {
	l := newInputLine(40, 24, "ctx-m", "\x1b[2m")
	l.draw()
	feedSteps(t, "typed ahead", l, []step{{"$ abc", "$ abc"}})
	l.typed()
	feedSteps(t, "then", l, []step{
		{"d", "d" + lineHide},
		{"\b\x1b[K", "\b\x1b[K" + lineShow},
	})
}

// The shell's own \e7 owns the terminal's one saved cursor till its \e8:
// the status is erased before it and drawn only after.
func TestInputLineSavedCursor(t *testing.T) {
	for _, tc := range []struct{ save, restore string }{
		{"\x1b7", "\x1b8"},
		{"\x1b[s", "\x1b[u"},
		{"\x1b[?1048h", "\x1b[?1048l"},
		{"\x1b[?2004;1048h", "\x1b[?1048;2004l"},
	} {
		l := newInputLine(40, 24, "ctx-m", "\x1b[2m")
		l.draw()
		feedSteps(t, "right prompt "+tc.save, l, []step{
			{tc.save, lineHide + tc.save},
			{"\x1b[20G12:00", "\x1b[20G12:00"},
			{tc.restore + "$ ", tc.restore + "$ " + lineShow},
		})
		l.typed()
		feedSteps(t, "typing after "+tc.save, l, []step{
			{"a", "a" + lineHide},
			{"\b\x1b[K", "\b\x1b[K" + lineShow},
		})
	}
	l := atPrompt(t)
	feedSteps(t, "deleting", l, []step{
		{"\x1b[P", lineHide + "\x1b[P" + lineShow},
		{"\x1b[2@", lineHide + "\x1b[2@" + lineShow},
	})
}
