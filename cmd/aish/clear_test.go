package main

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// `aish clear save` is gone: every session is saved, and `aish new NAME`
// names the next one.
func TestClearArgs(t *testing.T) {
	got := fakeProxy(t, map[string]any{
		rpc.MethodInfo:  rpc.Info{SessionID: "a", Saved: true},
		rpc.MethodClear: rpc.Info{SessionID: "b"},
	})
	for _, args := range [][]string{{"save"}, {"save", "x"}} {
		code, _, stderr := captured(t, func() int { return clearCmd(args) })
		if code == 0 || !strings.Contains(stderr, "usage: aish clear") {
			t.Errorf("%q: exit %d, %q", args, code, stderr)
		}
	}
	if len(got) > 0 {
		t.Errorf("rpc called: %v", got)
	}
	code, stdout, _ := captured(t, func() int { return newCmd([]string{"log", "triage"}) })
	if code != 0 || string(got[rpc.MethodClear]) != `{"name":"log triage"}` || stdout != "left a, new session log triage (b)\n" {
		t.Errorf("aish new: exit %d, %s, %q", code, got[rpc.MethodClear], stdout)
	}
}

func TestStartedOver(t *testing.T) {
	empty, saved, named, info := rpc.Info{SessionID: "a"}, rpc.Info{SessionID: "a", Saved: true},
		rpc.Info{SessionID: "a", Saved: true, Name: "deploy"}, rpc.Info{SessionID: "b"}
	for _, c := range []struct {
		cp   rpc.ClearParams
		old  rpc.Info
		want string
	}{
		{rpc.ClearParams{}, empty, "new session b"},
		{rpc.ClearParams{}, saved, "left a, new session b"},
		{rpc.ClearParams{}, named, "left deploy (a), new session b"},
		{rpc.ClearParams{Name: "work"}, empty, "new session work (b)"},
		{rpc.ClearParams{Name: "work"}, saved, "left a, new session work (b)"},
	} {
		if got := startedOver(c.cp, c.old, info); got != c.want {
			t.Errorf("%+v %+v: %q, want %q", c.cp, c.old, got, c.want)
		}
	}
}
