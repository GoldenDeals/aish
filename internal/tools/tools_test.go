package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// external loads a tool that prints its argv one word per line, then
// AISH_ARG_B and its standard input.
func external(t *testing.T) Tool {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"# aish:desc Echo the arguments\n" +
		"# aish:arg a string First\n" +
		"# aish:arg b? integer Optional, between required ones\n" +
		"# aish:arg c string Second required\n" +
		"# aish:arg v? boolean Verbose\n" +
		"# aish:arg body? stdin Text\n" +
		"for x; do printf '%s\\n' \"$x\"; done\n" +
		"printf 'B=%s\\n' \"${AISH_ARG_B-unset}\"\n" +
		"cat\n"
	if err := os.WriteFile(filepath.Join(dir, "echo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := loadExternal(dir)
	if len(ts) != 1 {
		t.Fatalf("loaded %d tools", len(ts))
	}
	return ts[0]
}

func TestExternalArgs(t *testing.T) {
	tool := external(t)
	if u := tool.Usage(); u != "echo A [--b B] C [--v] [BODY|-]" {
		t.Errorf("usage %q", u)
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		// From the model: JSON numbers, and null for an argument not given.
		{map[string]any{"a": "x", "c": "y z"}, "x\ny z\nB=unset\n"},
		{map[string]any{"a": "x", "b": nil, "c": "y z", "v": false}, "x\ny z\nB=unset\n"},
		{map[string]any{"a": "x", "b": 3.0, "c": "y z", "v": true, "body": "text\n"},
			"x\ny z\n--b\n3\n--v\nB=3\ntext\n"},
		{map[string]any{"a": "--b", "b": -1.0, "c": "--", "v": "true"}, "--b\n--\n--b\n-1\n--v\nB=-1\n"},
	} {
		out, err := tool.Execute(context.Background(), tc.args, nil)
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
		}
		if out != tc.want {
			t.Errorf("%v: got %q, want %q", tc.args, out, tc.want)
		}
	}
	if _, err := tool.Execute(context.Background(), map[string]any{"a": "x", "b": 1.0}, nil); err == nil ||
		!strings.Contains(err.Error(), "missing argument c") {
		t.Errorf("missing c: %v", err)
	}
}

// The tool gets the command line `aish tool` reads, so parsing what it got
// gives back the arguments typed.
func TestExternalRoundTrip(t *testing.T) {
	tool := external(t)
	for _, argv := range [][]string{
		{"x", "y"},
		{"--b", "7", "x", "y"},
		{"x", "--v", "y", "--b=7", "body"},
	} {
		args, err := tool.ParseCLI(argv, strings.NewReader("stdin"))
		if err != nil {
			t.Fatalf("%q: %v", argv, err)
		}
		out, err := tool.Execute(context.Background(), args, nil)
		if err != nil {
			t.Fatalf("%q: %v", argv, err)
		}
		got := strings.Split(out, "\n")
		got = got[:len(got)-2] // B=…, then stdin with no newline
		back, err := tool.ParseCLI(got, strings.NewReader(args["body"].(string)))
		if err != nil {
			t.Fatalf("%q: parsing %q: %v", argv, got, err)
		}
		if !reflect.DeepEqual(back, args) {
			t.Errorf("%q: the tool got %q, which parses as %v, not %v", argv, got, back, args)
		}
	}
}
