package proxy

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// A hidden command that waits for input, a prompt it wrote on /dev/tty
// standing still, is shown below the agent's line, which stops turning;
// what the user types goes there, and the agent goes on with its line
// below once the command is done.
func TestHiddenPromptStill(t *testing.T) {
	t.Parallel()
	p, out, u := spinning(t)
	p.output([]byte("Password: "))
	time.Sleep(promptWait / 2)
	if got := strings.TrimPrefix(out.String(), agentLine); spinDraws.ReplaceAllString(got, "") != "" {
		t.Fatalf("before %v of quiet: %q", promptWait, got)
	}
	time.Sleep(promptWait) // 1.5s since the prompt
	drawn := strings.TrimPrefix(out.String(), agentLine)
	if !spinStopped(p) || spinDraws.ReplaceAllString(drawn, "") != "\r\nPassword: " {
		t.Fatalf("stopped %v, drew %q", spinStopped(p), drawn)
	}
	shown := out.String()
	time.Sleep(3 * spinTick)
	if got := strings.TrimPrefix(out.String(), shown); got != "" {
		t.Errorf("after the prompt: %q", got)
	}

	if b := p.key([]byte("x\r")); string(b) != "x\r" {
		t.Errorf("the keys went to the shell as %q", b)
	}
	p.output([]byte("x\r\ngot-x\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	u.Write([]byte("\r⠙ Running 1 command…\x1b[K"))
	u.Write([]byte("\r● Ran 1 command\x1b[K\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	rows := screenRows(out.String())
	want := []string{"Password: x", "got-x", "● Ran 1 command", ""}
	if len(rows) != 5 || !strings.HasSuffix(rows[0], " Running 1 command… 80") || !slices.Equal(rows[1:], want) {
		t.Errorf("screen %q", rows)
	}
	if want := []Fold{{Title: "❯ sleep 5", Text: "Password: x\r\ngot-x\r\n"}}; !slices.Equal(p.folds, want) {
		t.Errorf("folds %+v", p.folds)
	}
}

// A key the user types while a hidden command runs shows it at once: the
// key goes to the command. Not a key that signals it: Ctrl+C cuts it
// short, and the prompt ends the agent's line as before.
func TestHiddenPromptKey(t *testing.T) {
	for _, tc := range []struct {
		name, output, key, want string
	}{
		{"prompt", "Password: ", "s", "\r\nPassword: "},
		{"prompt ended", "Password:\r\n", "s", "\r\nPassword:\r\n"},
		{"no output", "", "\r", "\r\n"},
		{"paste", "Password: ", "\x1b[200~secret\x1b[201~", "\r\nPassword: "},
	} {
		p, out, _ := spinning(t)
		p.output([]byte(tc.output))
		if b := p.key([]byte(tc.key)); string(b) != tc.key {
			t.Errorf("%s: the keys went to the shell as %q", tc.name, b)
		}
		if got := strings.TrimPrefix(out.String(), agentLine); got != tc.want || !spinStopped(p) {
			t.Errorf("%s: stopped %v, drew %q, want %q", tc.name, spinStopped(p), got, tc.want)
		}
	}

	for _, key := range []string{"\x03", "\x1c", "\x1a"} {
		p, out, _ := spinning(t)
		p.output([]byte("Password: "))
		p.key([]byte(key))
		if got := strings.TrimPrefix(out.String(), agentLine); spinDraws.ReplaceAllString(got, "") != "" || spinStopped(p) {
			t.Errorf("%q: stopped %v, drew %q", key, spinStopped(p), got)
		}
	}
	p, out, _ := spinning(t)
	p.output([]byte("Password: "))
	p.key([]byte("\x03"))
	p.output([]byte("^C"))
	p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})
	if rows := screenRows(out.String()); len(rows) != 2 || !strings.HasSuffix(rows[0], "  (interrupted)") || strings.Contains(rows[0], "Password") {
		t.Errorf("Ctrl+C: %q", rows)
	}
}

// What a command printed before its prompt shows by its end: a long output
// would push the agent's line off the screen.
func TestHiddenPromptTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 15; i++ {
		lines = append(lines, fmt.Sprintf("line %d\r\n", i))
	}
	all := strings.Join(lines, "")
	for _, tc := range []struct {
		name, output, want string
	}{
		{"none", "", ""},
		{"prompt", "Password: ", "Password: "},
		{"fewer", strings.Join(lines[:9], "") + "Password: ", strings.Join(lines[:9], "") + "Password: "},
		{"as many, ended", strings.Join(lines[:10], ""), strings.Join(lines[:10], "")},
		{"more", all + "Password: ", dim + "… (+6 lines)" + reset + "\r\n" + strings.Join(lines[6:], "") + "Password: "},
		{"one more, ended", strings.Join(lines[:11], ""), dim + "… (+1 line)" + reset + "\r\n" + strings.Join(lines[1:11], "")},
	} {
		f := newQuiet("❯ ssh host")
		f.write([]byte(tc.output))
		if got := string(f.expand()); got != "\r\n"+tc.want {
			t.Errorf("%s: shows\n%q, want\n%q", tc.name, got, "\r\n"+tc.want)
		}
		if got := string(f.write([]byte("yes\r\n"))); got != "yes\r\n" {
			t.Errorf("%s: then shows %q", tc.name, got)
		}
	}
}

// Output that does not wait stays hidden, as before: lines coming without
// a pause, even made in parts; a progress bar redrawn over its line, or a
// sequence after the last line, standing still.
func TestHiddenOutputStill(t *testing.T) {
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
			p, out, _ := spinning(t)
			for _, w := range tc.writes {
				p.output([]byte(w))
				time.Sleep(promptWait / 4)
			}
			time.Sleep(promptWait + 3*spinTick)
			drawn := strings.TrimPrefix(out.String(), agentLine)
			if spinDraws.ReplaceAllString(drawn, "") != "" || spinStopped(p) {
				t.Errorf("stopped %v, drew %q", spinStopped(p), drawn)
			}
		})
	}
}
