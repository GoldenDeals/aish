package proxy

import (
	"reflect"
	"testing"
)

const testNonce = "N0NCE"

func feedAll(chunks ...string) (string, []Marker) {
	f := NewFilter(testNonce)
	var text []byte
	var ms []Marker
	for _, c := range chunks {
		f.Feed([]byte(c), func(b []byte) { text = append(text, b...) }, func(m Marker) { ms = append(ms, m) })
	}
	return string(text), ms
}

func TestFilter(t *testing.T) {
	want := []Marker{{"cmd-start", "ls -la"}, {"cmd-end", "0;/tmp"}}
	whole := "a\x1b]6973;N0NCE;cmd-start;ls -la\abc\x1b[31mred\x1b[0m\x1b]6973;N0NCE;cmd-end;0;/tmp\a$ "
	text, ms := feedAll(whole)
	if text != "abc\x1b[31mred\x1b[0m$ " || !reflect.DeepEqual(ms, want) {
		t.Fatalf("whole: %q %v", text, ms)
	}
	// Every split point must give the same result.
	for i := 1; i < len(whole); i++ {
		text, ms := feedAll(whole[:i], whole[i:])
		if text != "abc\x1b[31mred\x1b[0m$ " || !reflect.DeepEqual(ms, want) {
			t.Fatalf("split at %d: %q %v", i, text, ms)
		}
	}
}

func TestFilterForeignOSC(t *testing.T) {
	in := "\x1b]0;title\a\x1b]697;x\ahi"
	text, ms := feedAll(in)
	if text != in || len(ms) != 0 {
		t.Fatalf("%q %v", text, ms)
	}
}

// TestFilterForeignNonce: markers in the output of `cat` or of a previous
// session are output, not taken for the shell's.
func TestFilterForeignNonce(t *testing.T) {
	for _, in := range []string{
		"\x1b]6973;cmd-end;0;/\a",       // no nonce
		"\x1b]6973;OTHER;cmd-end;0;/\a", // another session's
		"\x1b]6973;N0NC;cmd-end;0;/\a",  // a prefix of ours
		"\x1b]6973;N0NCEX;cmd-end;0;/\a",
	} {
		in = "a" + in + "b"
		for i := 1; i < len(in); i++ {
			text, ms := feedAll(in[:i], in[i:])
			if text != in || len(ms) != 0 {
				t.Fatalf("%q split at %d: %q %v", in, i, text, ms)
			}
		}
	}
}

// TestFilterCutShort: a marker of ours broken off by an ESC is dropped at
// once rather than holding the output behind it back.
func TestFilterCutShort(t *testing.T) {
	in := "a\x1b]6973;N0NCE;fold-start;ti\x1b[1mb\x1b]6973;N0NCE;cmd-end;0;/\ac"
	for i := 1; i < len(in); i++ {
		text, ms := feedAll(in[:i], in[i:])
		if text != "a\x1b[1mbc" || !reflect.DeepEqual(ms, []Marker{{"cmd-end", "0;/"}}) {
			t.Fatalf("split at %d: %q %v", i, text, ms)
		}
	}
	f := NewFilter(testNonce)
	var text []byte
	f.Feed([]byte("\x1b]6973;N0NCE;fold-start;ti\x1b[1mbold"), func(b []byte) { text = append(text, b...) }, func(Marker) {})
	if string(text) != "\x1b[1mbold" {
		t.Fatalf("held back: %q", text)
	}
}
