package capture

import (
	"os"
	"strings"
	"testing"
)

// text writes stream to a text buffer n bytes at a time, as a PTY hands
// it over, and returns what Clean reads of it.
func text(stream string, n int) string {
	b := NewText(64<<10, 64<<10)
	for len(stream) > n {
		b.Write([]byte(stream[:n]))
		stream = stream[n:]
	}
	b.Write([]byte(stream))
	return Clean(b.Bytes())
}

// sessionStream is an ssh session the way the user's terminal gets it: a
// remote shell's prompt, commands and their output, vim on the alternate
// screen in between.
const sessionStream = "Last login: Fri Oct  9 12:00:00 2026\r\n" +
	"\x1b]0;user@remote: ~\a\x1b[1;32muser@remote\x1b[0m:~$ ls\r\n" +
	"a.txt  b.txt\r\n" +
	"\x1b]0;user@remote: ~\a\x1b[1;32muser@remote\x1b[0m:~$ vim a.txt\r\n" +
	"\x1b[?1049h\x1b[H\x1b[2Jline one\r\n~\r\n~\x1b[24;1H\"a.txt\" 1L, 9B\x1b[1;1H" +
	"\x1b[24;1H:wq\r\x1b[?1049l" +
	"\x1b]0;user@remote: ~\a\x1b[1;32muser@remote\x1b[0m:~$ ls -l\r\n" +
	"total 8\r\n-rw-r--r-- 1 user user 9 Oct  9 12:01 a.txt\r\n" +
	"\x1b]0;user@remote: ~\a\x1b[1;32muser@remote\x1b[0m:~$ exit\r\n" +
	"logout\r\nConnection to remote closed.\r\n"

const sessionText = "Last login: Fri Oct  9 12:00:00 2026\n" +
	"user@remote:~$ ls\n" +
	"a.txt  b.txt\n" +
	"user@remote:~$ vim a.txt\n" +
	FullScreen + "\n" +
	"user@remote:~$ ls -l\n" +
	"total 8\n-rw-r--r-- 1 user user 9 Oct  9 12:01 a.txt\n" +
	"user@remote:~$ exit\n" +
	"logout\nConnection to remote closed."

// What an ssh session leaves is its text, with one line where vim was,
// however the stream is cut into writes.
func TestTextSession(t *testing.T) {
	for n := 1; n <= len(sessionStream); n++ {
		if got := text(sessionStream, n); got != sessionText {
			t.Fatalf("written %d bytes at a time:\n%s\nwant:\n%s", n, got, sessionText)
		}
	}
	if got := Clean([]byte(sessionStream)); got != sessionText {
		t.Errorf("Clean of the whole stream:\n%s\nwant:\n%s", got, sessionText)
	}
}

func TestTextFullScreen(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"vim and nothing else", "\x1b[?1049h\x1b[Hfile\r\n~\r\n\x1b[?1049l", FullScreen},
		{"never left", "$ top\r\n\x1b[?1049h\x1b[Htop - 12:00:00 up 1 day\r\n", "$ top\n" + FullScreen},
		{"never left, at once", "\x1b[?1049h", FullScreen},
		{"older switches", "a\r\n\x1b[?47hx\x1b[?47lb\r\n\x1b[?1047hy\x1b[?1047lc", "a\n" + FullScreen + "\nb\n" + FullScreen + "\nc"},
		// A program that redraws the screen in and out, or a loop of
		// them, is one line; text between them parts them.
		{"in and out", strings.Repeat("\x1b[?1049hx\x1b[?1049l", 100) + "done\r\n", FullScreen + "\ndone"},
		{"blank lines between", "\x1b[?1049hx\x1b[?1049l\r\n\r\n\x1b[?1049hy\x1b[?1049l", FullScreen},
		{"text between", "\x1b[?1049hx\x1b[?1049l$ ls\r\n\x1b[?1049hy\x1b[?1049l", FullScreen + "\n$ ls\n" + FullScreen},
		// Started in the middle of a line, it goes below it; the main
		// screen goes on below it.
		{"mid-line", "abc\x1b[?1049hx\x1b[?1049ldef\r\n", "abc\n" + FullScreen + "\ndef"},
		// A switch back with no switch there stays a private mode.
		{"leave only", "a\x1b[?1049lb", "ab"},
	} {
		if got := text(c.in, len(c.in)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if got := Clean([]byte(c.in)); got != c.want {
			t.Errorf("%s, Clean of the stream: %q, want %q", c.name, got, c.want)
		}
	}
}

// What a full-screen program draws takes no room in the buffer: the text
// around an hour of htop is there whole.
func TestTextFullScreenRoom(t *testing.T) {
	b := NewText(16, 16)
	b.Write([]byte("$ htop\r\n"))
	b.Write([]byte("\x1b[?1049h"))
	for range 1000 {
		b.Write([]byte("\x1b[H CPU [|||||     50%]\r\n Mem [||||     40%]"))
	}
	b.Write([]byte("\x1b[?1049l$ ls\r\n"))
	if !b.AltScreen() {
		t.Error("the alternate screen is not told")
	}
	if got, want := Clean(b.Bytes()), "$ htop\n"+FullScreen+"\n$ ls"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// A real remote shell, recorded in a PTY: a colored prompt with the window
// title, the line edited (a typo erased, a letter inserted in the middle,
// a line from history changed, a search in it, completion with its list,
// a comment made with Ctrl+A), a line longer than the terminal, less and
// vim, clear. Each prompt is the line as entered.
func TestTextRecordedBash(t *testing.T) {
	raw, err := os.ReadFile("testdata/bash-session.raw")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 100)
	want := strings.Join([]string{
		"user@remote:~$ cd dir",
		"user@remote:~/dir$ ls",
		"sub1  sub2",
		"user@remote:~/dir$ echo hello world",
		"hello world",
		"user@remote:~/dir$ echo hello there",
		"hello there",
		"user@remote:~/dir$ echo hello there",
		"hello there",
		"user@remote:~/dir$ ls s",
		"sub1/ sub2/",
		"user@remote:~/dir$ ls sub1/",
		"user@remote:~/dir$ #echo one; e",
		"user@remote:~/dir$ echo " + long,
		long,
		"user@remote:~/dir$ less ../notes.txt",
		FullScreen,
		"user@remote:~/dir$ vim -u NONE -N ../notes.txt",
		FullScreen,
		"user@remote:~/dir$ clear",
		`user@remote:~/dir$ printf "%s\n" done`,
		"done",
		"user@remote:~/dir$ exit",
		"exit",
	}, "\n")
	for _, n := range []int{len(raw), 1, 3, 7, 64} {
		if got := text(string(raw), n); got != want {
			t.Errorf("written %d bytes at a time:\n%s\nwant:\n%s", n, got, want)
		}
	}
}

// The same of zsh: the mark of a line without a newline (PROMPT_SP), the
// first letter typed drawn again, erasing with backspace and space.
func TestTextRecordedZsh(t *testing.T) {
	raw, err := os.ReadFile("testdata/zsh-session.raw")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"user@remote:~% cd dir",
		"user@remote:~/dir% ls",
		"sub1  sub2",
		"user@remote:~/dir% echo hello world",
		"hello world",
		"user@remote:~/dir% echo hello there",
		"hello there",
		"user@remote:~/dir% less ../notes.txt",
		FullScreen,
		"user@remote:~/dir% exit",
	}, "\n")
	if got := text(string(raw), 5); got != want {
		t.Errorf("\n%s\nwant:\n%s", got, want)
	}
}
