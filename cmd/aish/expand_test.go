package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/rpc"
)

func TestPrintFold(t *testing.T) {
	const title, reset = "\x1b[36m", "\x1b[0m"
	tests := []struct {
		name string
		fold rpc.Fold
		want []string
	}{
		{
			name: "multiline title without output",
			fold: rpc.Fold{Title: "❯ cat > f <<EOF\na\tb\nEOF"},
			want: []string{
				title + "❯ cat > f <<EOF" + reset,
				title + "  a        b" + reset,
				title + "  EOF" + reset,
			},
		},
		{
			name: "output captured from the PTY",
			fold: rpc.Fold{Title: "❯ printf 'x\\ny\\n'", Text: "x\r\ny\r\n"},
			want: []string{title + "❯ printf 'x\\ny\\n'" + reset, "x", "y"},
		},
		{
			name: "output without a final newline",
			fold: rpc.Fold{Title: "❯ printf x", Text: "x"},
			want: []string{title + "❯ printf x" + reset, "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			printFold(&b, tt.fold)
			out := b.String()
			if !strings.HasSuffix(out, "\n"+reset) {
				t.Fatalf("output %q does not end with a newline and a reset", out)
			}
			// A reset in front of a line only guards against the previous fold.
			out = strings.TrimSuffix(out, "\n"+reset)
			var got []string
			for _, l := range strings.Split(out, "\n") {
				got = append(got, strings.TrimPrefix(l, reset))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("printFold(%q) lines:\n got %q\nwant %q", tt.fold, got, tt.want)
			}
		})
	}
}
