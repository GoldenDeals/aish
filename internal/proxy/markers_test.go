package proxy

import (
	"reflect"
	"testing"
)

func feedAll(chunks ...string) (string, []Marker) {
	var f Filter
	var text []byte
	var ms []Marker
	for _, c := range chunks {
		f.Feed([]byte(c), func(b []byte) { text = append(text, b...) }, func(m Marker) { ms = append(ms, m) })
	}
	return string(text), ms
}

func TestFilter(t *testing.T) {
	want := []Marker{{"cmd-start", "ls -la"}, {"cmd-end", "0;/tmp"}}
	whole := "a\x1b]6973;cmd-start;ls -la\abc\x1b[31mred\x1b[0m\x1b]6973;cmd-end;0;/tmp\a$ "
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
