package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// aish yolo turns the checks off and aish yolo off on, by the proxy, with
// config.toml broken on disk too; what the proxy refuses is the command's
// error.
func TestYoloCmd(t *testing.T) {
	diskConfig(t, "max_steps = -1\n")
	got := fakeProxy(t, map[string]any{rpc.MethodYolo: nil})
	for _, tc := range []struct {
		args   []string
		params string
		out    string
	}{
		{[]string{"yolo"}, `{"on":true}`, "yolo: no policies"},
		{[]string{"yolo", "on"}, `{"on":true}`, "yolo: no policies"},
		{[]string{"yolo", "off"}, `{"on":false}`, "yolo off"},
	} {
		code, stdout, stderr := captured(t, func() int { return run(tc.args) })
		if code != 0 || stderr != "" || !strings.HasPrefix(stdout, tc.out) || string(got[rpc.MethodYolo]) != tc.params {
			t.Errorf("%q: exit %d, stdout %q, stderr %q, params %s", tc.args, code, stdout, stderr, got[rpc.MethodYolo])
		}
	}
	delete(got, rpc.MethodYolo)
	if code, _, stderr := captured(t, func() int { return run([]string{"yolo", "of"}) }); code == 0 || !strings.Contains(stderr, "usage: aish yolo [off]") {
		t.Errorf("aish yolo of: exit %d, stderr %q", code, stderr)
	}
	if _, ok := got[rpc.MethodYolo]; ok {
		t.Error("a wrong argument reached the proxy")
	}

	const refused = "yolo is switched by the user, not by the assistant"
	fakeProxy(t, map[string]any{rpc.MethodYolo: errors.New(refused)})
	if code, stdout, stderr := captured(t, func() int { return run([]string{"yolo"}) }); code == 0 || stdout != "" || !strings.Contains(stderr, refused) {
		t.Errorf("refused: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
