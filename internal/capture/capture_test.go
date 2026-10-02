package capture

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClean(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"\x1b[1;32mok\x1b[0m\r\n", "ok"},
		{"progress 10%\rprogress 100%\r\ndone\r\n", "progress 100%\ndone"},
		{"abc\b\bX\r\n", "aXc"},
		{"\x1b]0;title\a\x1b(Bhello  \r\n", "hello"},
		{"a\r\n\r\nb", "a\n\nb"},
	} {
		if got := Clean([]byte(c.in)); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAltScreen(t *testing.T) {
	b := NewBuffer(16, 16)
	b.Write([]byte("x\x1b[?10"))
	b.Write([]byte("49h"))
	if !b.AltScreen() {
		t.Fatal("alt screen split across writes not detected")
	}
}

func TestBufferKeepsHeadAndTail(t *testing.T) {
	b := NewBuffer(4, 4)
	b.Write([]byte("HEAD"))
	b.Write([]byte(strings.Repeat("-", 100)))
	b.Write([]byte("TAIL"))
	s := string(b.Bytes())
	if !strings.HasPrefix(s, "HEAD") || !strings.HasSuffix(s, "TAIL") || !strings.Contains(s, "omitted") {
		t.Fatalf("%q", s)
	}
}

func TestTruncate(t *testing.T) {
	s := strings.Repeat("я", 1000)
	got := Truncate(s, 100)
	if len(got) > 200 || !utf8.ValidString(got) || !strings.Contains(got, "omitted") {
		t.Fatalf("%d %q", len(got), got)
	}
	if Truncate("short", 100) != "short" {
		t.Fatal("short string changed")
	}
}
