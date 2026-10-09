package proxy

import (
	"bytes"
	"strings"
	"testing"
)

// otherModeOf is the modifyOtherKeys mode s leaves the terminal in: "on",
// "off", or "" if s sets none.
func otherModeOf(s string) string {
	on, off := strings.LastIndex(s, string(otherKeysOn)), strings.LastIndex(s, string(otherKeysOff))
	switch {
	case on > off:
		return "on"
	case off > on:
		return "off"
	}
	return ""
}

// prompted is keysProxy at the prompt, readline having taken the terminal.
func prompted(t *testing.T) *Proxy {
	t.Helper()
	p := keysProxy(t)
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.output([]byte("$ \x1b[?2004h"))
	return p
}

// TestShiftEnterMode follows the shell from prompt to prompt: the terminal
// is in modifyOtherKeys only once readline has it at the prompt, and only
// while the keys are the shell's. There Shift+Enter reaches the PTY as
// Ctrl+V Ctrl+J, a newline readline puts in the line, and Enter as Enter.
// A command, the viewer, a request and the shell gone have the terminal
// as it was, and their keys go as they come.
func TestShiftEnterMode(t *testing.T) {
	const shiftEnter = "\x1b[27;2;13~"
	p := keysProxy(t)
	out := p.out.(*terminal)
	step := func(what, mode, keys, want string) {
		t.Helper()
		if m := otherModeOf(out.String()); m != mode {
			t.Errorf("%s: the mode %q, want %q", what, m, mode)
		}
		if keys == "" {
			return
		}
		if got := string(p.key([]byte(keys))); got != want {
			t.Errorf("%s: %q reached the PTY as %q, want %q", what, keys, got, want)
		}
	}

	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	step("cmd-end, before readline", "", shiftEnter, shiftEnter)
	p.output([]byte("$ \x1b[?2004h"))
	step("readline at the prompt", "on", shiftEnter, "\x16\n")
	step("Enter at the prompt", "on", "\r", "\r")
	step("CSI u", "on", "ls\x1b[13;2u-l\r", "ls\x16\n-l\r")

	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("Ctrl+O opened no viewer")
	}
	step("the viewer", "off", shiftEnter, "")
	p.key([]byte("q"))
	if p.view != nil {
		t.Fatal("q left the viewer open")
	}
	step("the viewer closed", "on", "", "")
	p.marker(Marker{Kind: "at-prompt"})
	step("a marker printed at the prompt", "on", shiftEnter, "\x16\n")

	p.marker(Marker{Kind: "cmd-start", Payload: "vim"})
	step("a command", "off", shiftEnter, shiftEnter)
	p.output([]byte("\x1b[?2004h\x1b[>4;2m"))
	if s := out.String(); !strings.HasSuffix(s, "\x1b[?2004h\x1b[>4;2m") {
		t.Errorf("the command's own mode was answered: %q", s[len(s)-20:])
	}
	step("a command turning bracketed paste on", "off", shiftEnter, shiftEnter)

	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.output([]byte("\x1b[?2004h"))
	step("the next prompt", "on", "", "")
	p.marker(Marker{Kind: "ask-start"})
	step("a request", "off", shiftEnter, shiftEnter)

	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.output([]byte("\x1b[?2004h"))
	step("the prompt after the request", "on", "", "")
	p.restoreScreen()
	step("the shell gone", "off", "", "")
	p.key([]byte("x"))
	step("a key after the shell", "off", "", "")
}

// TestShiftEnterEarly: before the first prompt the keys are held, and the
// mode comes once readline has the terminal; with no terminal, never.
func TestShiftEnterEarly(t *testing.T) {
	p := keysProxy(t)
	out := p.out.(*terminal)
	var pty bytes.Buffer
	p.holdEarly(&pty)
	p.key([]byte("Say"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	if m := otherModeOf(out.String()); m != "" {
		t.Errorf("before readline: the mode %q", m)
	}
	p.output([]byte("$ \x1b[?2004h"))
	if m := otherModeOf(out.String()); m != "on" || pty.String() != "Say" {
		t.Errorf("readline at the first prompt: the mode %q, the PTY got %q", m, pty.String())
	}

	// Not inside a sequence the shell's output cut short.
	p = keysProxy(t)
	out = p.out.(*terminal)
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.output([]byte("$ \x1b[?2004h\x1b"))
	if m := otherModeOf(out.String()); m != "" {
		t.Errorf("inside a sequence: the mode %q", m)
	}
	p.output([]byte("[K"))
	if s := out.String(); !strings.HasSuffix(s, "\x1b[K"+string(otherKeysOn)) {
		t.Errorf("after the sequence: %q", s)
	}

	p = keysProxy(t)
	p.size = nil
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.output([]byte("$ \x1b[?2004h"))
	if m := otherModeOf(p.out.(*terminal).String()); m != "" {
		t.Errorf("no terminal: the mode %q", m)
	}
}

// TestShiftEnterKeys: at the prompt the keys modifyOtherKeys changed, and
// their CSI u form, reach readline as the terminal sends them without it,
// Shift+Enter as Ctrl+V Ctrl+J; any other sequence, a key cut short and
// what is inside a paste, cut by the reads too, go as they came.
func TestShiftEnterKeys(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"Shift+Enter", "\x1b[27;2;13~", "\x16\n"},
		{"Shift+Enter, CSI u", "\x1b[13;2u", "\x16\n"},
		{"Shift+Enter, Caps Lock on", "\x1b[13;66u", "\x16\n"},
		{"in the line", "Say\x1b[27;2;13~next\r", "Say\x16\nnext\r"},
		{"Ctrl+Enter", "\x1b[27;5;13~", "\r"},
		{"Ctrl+Shift+Enter", "\x1b[27;6;13~", "\r"},
		{"Alt+Enter", "\x1b[27;3;13~", "\x1b\r"},
		{"Enter, CSI u", "\x1b[13u", "\r"},
		{"Ctrl+1", "\x1b[27;5;49~", "1"},
		{"Ctrl+1, CSI u", "\x1b[49;5u", "1"},
		{"Ctrl+2", "\x1b[27;5;50~", "\x00"},
		{"Ctrl+/", "\x1b[27;5;47~", "\x1f"},
		{"Ctrl+Shift+A", "\x1b[27;6;65~", "\x01"},
		{"Ctrl+Alt+a", "\x1b[27;7;97~", "\x1b\x01"},
		{"Shift+a", "\x1b[27;2;97~", "A"},
		{"Ctrl+Tab", "\x1b[27;5;9~", "\t"},
		{"Ctrl+Shift+Tab", "\x1b[27;6;9~", "\x1b[Z"},
		{"Alt+ж", "\x1b[27;3;1078~", "\x1bж"},
		{"Esc", "\x1b", "\x1b"},
		{"an arrow", "\x1b[A\x1b[1;5A", "\x1b[A\x1b[1;5A"},
		{"Delete", "\x1b[3~", "\x1b[3~"},
		{"cut short", "\x1b[27;2;13", "\x1b[27;2;13"},
		{"no modifiers", "\x1b[27;0;13~", "\x1b[27;0;13~"},
		{"too many", "\x1b[27;2;13;1~", "\x1b[27;2;13;1~"},
		{"a paste", "\x1b[200~a\x1b[27;5;99~\x1b[13;2u\x1b[201~\x1b[27;2;13~", "\x1b[200~a\x1b[27;5;99~\x1b[13;2u\x1b[201~\x16\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := prompted(t)
			if got := string(p.key([]byte(c.in))); got != c.want {
				t.Errorf("%q reached the PTY as %q, want %q", c.in, got, c.want)
			}
		})
	}
	p := prompted(t)
	var got strings.Builder
	for _, r := range []string{"\x1b[20", "0~a\x1b[27;5;99~", "\x1b[2", "01~\x1b[27;2;13~"} {
		got.Write(p.key([]byte(r)))
	}
	if want := "\x1b[200~a\x1b[27;5;99~\x1b[201~\x16\n"; got.String() != want {
		t.Errorf("a paste cut by the reads reached the PTY as %q, want %q", got.String(), want)
	}
}
