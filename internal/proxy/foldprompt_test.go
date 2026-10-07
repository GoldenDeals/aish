package proxy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// askCmd waits for input as sudo does: it asks on /dev/tty, its stdin
// being /dev/null.
const (
	askCmd   = "printf 'Password: ' >/dev/tty; read -r x </dev/tty; echo got-$x"
	callLine = "❯ " + askCmd
)

// folding is statusProxy, 120 columns wide, with the agent's askCmd handed
// to the shell, which runs it folded as fold_lines = limit says. With 0
// the agent leaves the line of the call open for the status.
func folding(t *testing.T, limit int) (*Proxy, *terminal) {
	t.Helper()
	p, out := statusProxy(t, 120)
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.stopWatch()
	})
	p.foldLines = limit
	u := &ui{p: p}
	if limit == 0 {
		u.Write([]byte(callLine))
		u.CommandAt(runewidth.StringWidth(callLine), false, 0)
	} else {
		u.Write([]byte(callLine + "\n"))
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c1;" + askCmd})
	return p, out
}

func watching(p *Proxy) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.watch != nil
}

// termRows is screenRows that also moves the cursor as the status at the
// right of a call is drawn: forward (C) and to a column (G).
func termRows(b string) []string {
	rows := [][]rune{nil}
	row, col := 0, 0
	rs := []rune(b)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '\r':
			col = 0
		case '\n':
			if row++; row == len(rows) {
				rows = append(rows, nil)
			}
		case 0x1b:
			if i+1 >= len(rs) || rs[i+1] != '[' {
				i++
				continue
			}
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j == len(rs) {
				break
			}
			param := string(rs[i+2 : j])
			n, _ := strconv.Atoi(param)
			switch rs[j] {
			case 'C':
				col += max(n, 1)
			case 'G':
				col = max(n, 1) - 1
			case 'K':
				if (param == "" || param == "0") && col < len(rows[row]) {
					rows[row] = rows[row][:col]
				}
			}
			i = j
		default:
			if c < 0x20 {
				continue
			}
			for len(rows[row]) <= col {
				rows[row] = append(rows[row], ' ')
			}
			rows[row][col] = c
			col++
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.TrimRight(string(r), " ")
	}
	return out
}

// folded reports whether rows are the call alone, its status at the right
// saying status.
func folded(rows []string, status string) bool {
	return len(rows) == 1 && strings.HasPrefix(rows[0], callLine+" ") && strings.HasSuffix(rows[0], "  ("+status+")")
}

// With fold_lines = 0 a command that waits for input, its prompt on
// /dev/tty standing still, is shown below its call, the status gone; what
// the user types goes there, and the command ends as one shown whole.
func TestFoldPromptStill(t *testing.T) {
	t.Parallel()
	p, out := folding(t, 0)
	p.output([]byte("Password: "))
	time.Sleep(promptWait / 2)
	if rows := termRows(out.String()); !folded(rows, "1 line · ctrl+o to expand") || !watching(p) {
		t.Fatalf("before %v of quiet: %q", promptWait, rows)
	}
	time.Sleep(promptWait) // 1.5s since the prompt
	if rows := termRows(out.String()); !slices.Equal(rows, []string{callLine, "Password:"}) || watching(p) {
		t.Fatalf("watching %v, screen %q", watching(p), rows)
	}
	if !strings.HasSuffix(out.String(), "\r\nPassword: ") {
		t.Errorf("the cursor is not after the prompt: %q", out.String())
	}
	shown := out.String()
	time.Sleep(promptWait / 4)
	if got := strings.TrimPrefix(out.String(), shown); got != "" {
		t.Errorf("after the prompt: %q", got)
	}

	if b := p.key([]byte("x\r")); string(b) != "x\r" {
		t.Errorf("the keys went to the shell as %q", b)
	}
	p.output([]byte("x\r\ngot-x\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	if rows, want := termRows(out.String()), []string{callLine, "Password: x", "got-x", ""}; !slices.Equal(rows, want) {
		t.Errorf("screen %q, want %q", rows, want)
	}
	if want := []Fold{{Title: callLine, Text: "Password: x\r\ngot-x\r\n"}}; !slices.Equal(p.folds, want) {
		t.Errorf("folds %+v", p.folds)
	}
	if o := p.done["c1"]; o.Output != "Password: x\ngot-x" {
		t.Errorf("recorded %+v", o)
	}
}

// A prompt that comes after the output stood still on a line it ended is
// shown as soon as it has stood still itself.
func TestFoldPromptLate(t *testing.T) {
	t.Parallel()
	p, out := folding(t, 0)
	p.output([]byte("Connecting…\r\n"))
	time.Sleep(promptWait + promptWait/5)
	p.output([]byte("Password: "))
	time.Sleep(promptWait / 2)
	if rows := termRows(out.String()); !folded(rows, "2 lines · ctrl+o to expand") {
		t.Fatalf("before %v of quiet: %q", promptWait, rows)
	}
	time.Sleep(promptWait/2 + promptWait/4)
	if rows, want := termRows(out.String()), []string{callLine, "Connecting…", "Password:"}; !slices.Equal(rows, want) {
		t.Errorf("screen %q, want %q", rows, want)
	}
}

// A key the user types while the command runs folded shows it at once:
// the key goes to the command. Not a key that signals it: Ctrl+C cuts it
// short, and its status ends the call as before.
func TestFoldPromptKey(t *testing.T) {
	for _, tc := range []struct {
		name, output, key string
		want              []string
	}{
		{"prompt", "Password: ", "s", []string{callLine, "Password:"}},
		{"prompt ended", "Password:\r\n", "s", []string{callLine, "Password:", ""}},
		{"no output", "", "\r", []string{callLine, ""}},
		{"paste", "Password: ", "\x1b[200~secret\x1b[201~", []string{callLine, "Password:"}},
	} {
		p, out := folding(t, 0)
		p.output([]byte(tc.output))
		if b := p.key([]byte(tc.key)); string(b) != tc.key {
			t.Errorf("%s: the keys went to the shell as %q", tc.name, b)
		}
		if rows := termRows(out.String()); !slices.Equal(rows, tc.want) || watching(p) {
			t.Errorf("%s: watching %v, screen %q, want %q", tc.name, watching(p), rows, tc.want)
		}
	}

	for _, key := range []string{"\x03", "\x1c", "\x1a"} {
		p, out := folding(t, 0)
		p.output([]byte("Password: "))
		p.key([]byte(key))
		if rows := termRows(out.String()); !folded(rows, "1 line · ctrl+o to expand") || !watching(p) {
			t.Errorf("%q: watching %v, screen %q", key, watching(p), rows)
		}
	}
	p, out := folding(t, 0)
	p.output([]byte("Password: "))
	p.key([]byte("\x03"))
	p.output([]byte("^C"))
	p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})
	if rows := termRows(out.String()); len(rows) != 2 || !strings.HasSuffix(rows[0], "(1 line · interrupted · ctrl+o to expand)") || watching(p) {
		t.Errorf("Ctrl+C: watching %v, screen %q", watching(p), rows)
	}
}

// Output that does not wait stays folded, as before: lines coming without
// a pause, even made in parts; a progress bar redrawn over its line, or a
// sequence after the last line, standing still.
func TestFoldOutputStill(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes []string
	}{
		{"lines", []string{"step 1: ", "ok\r\n", "step 2: ", "ok\r\n", "step 3: ", "ok\r\n", "step 4: ", "ok\r\n"}},
		{"progress", []string{"Receiving objects:  10% (1/10)", "\rReceiving objects:  20% (2/10)"}},
		{"progress after lines", []string{"Cloning into 'x'...\r\n", "\r 10%", "\r 20%"}},
		{"sequence", []string{"done\r\n\x1b[?25h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, out := folding(t, 0)
			for _, w := range tc.writes {
				p.output([]byte(w))
				time.Sleep(promptWait / 4)
			}
			time.Sleep(promptWait + promptWait/5)
			if rows := termRows(out.String()); len(rows) != 1 || !watching(p) {
				t.Errorf("watching %v, screen %q", watching(p), rows)
			}
			p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
			if watching(p) {
				t.Error("watching after agent-end")
			}
		})
	}
}

// With fold_lines = 3 a prompt past the lines shown opens the fold: the
// status line gives way to the end of what was hidden, after "… (+N
// lines)" if there was more. A prompt among the lines shown is on the
// screen already: the fold stays, keys or not.
func TestFoldPromptPastLines(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	for _, tc := range []struct {
		name string
		n    int // lines before the prompt
		want []string
	}{
		{"ten", 10, slices.Concat(lines[:10], []string{"Password:"})},
		{"twenty", 20, slices.Concat(lines[:3], []string{"… (+8 lines)"}, lines[11:], []string{"Password:"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, out := folding(t, 3)
			p.output([]byte(strings.Join(lines[:tc.n], "\r\n") + "\r\nPassword: "))
			rows := termRows(out.String())
			if want := slices.Concat([]string{callLine}, lines[:3], []string{fmt.Sprintf("  (%d lines · ctrl+o to expand)", tc.n-2)}); !slices.Equal(rows, want) {
				t.Fatalf("folded: screen %q, want %q", rows, want)
			}
			time.Sleep(promptWait + promptWait/2)
			if rows, want := termRows(out.String()), append([]string{callLine}, tc.want...); !slices.Equal(rows, want) {
				t.Errorf("screen %q, want %q", rows, want)
			}
		})
	}

	t.Run("shown", func(t *testing.T) {
		t.Parallel()
		p, out := folding(t, 3)
		p.output([]byte("Password: "))
		time.Sleep(promptWait + promptWait/2)
		p.key([]byte("x\r"))
		p.output([]byte("x\r\n" + strings.Join(lines[:5], "\r\n") + "\r\n"))
		want := []string{callLine, "Password: x", "line 1", "line 2", "  (3 lines · ctrl+o to expand)"}
		if rows := termRows(out.String()); !slices.Equal(rows, want) || !watching(p) {
			t.Errorf("watching %v, screen %q, want %q", watching(p), rows, want)
		}
	})
}
